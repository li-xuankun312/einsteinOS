package kernel

// live_exec.go — watch a slow command run, in a window of its own.
//
// A command the kernel runs on the real machine (a `make` of a few hundred .cu files,
// a test binary, the unit verification) used to be invisible until it finished: its
// output went into memory and came out as one result. Now it is also written, line by
// line, to a file; if the command is still running after a few seconds, a new window
// of the terminal multiplexer einsteinOS runs in opens and follows that file, and
// closes itself when the command ends.
//
//	EINSTEINOS_LIVE        off (default) | screen | tmux | auto
//	EINSTEINOS_LIVE_AFTER  how long a command runs before its window opens   (3s)
//	EINSTEINOS_LIVE_MAX    most windows open at once                        (4)
//	EINSTEINOS_LIVE_LINGER how long a finished window stays                 (3s)
//	EINSTEINOS_LIVE_DIR    where the follow-files live        (/tmp/einsteinos_live)
//
// Rules that keep it from being a nuisance:
//   - only a command that is still running after LIVE_AFTER gets a window: a run has
//     hundreds of `ls` and `sed`, and none of them open anything;
//   - at most LIVE_MAX at a time; the rest are only in the file;
//   - a command that FAILS leaves its window open (titled FAIL) until you press Enter,
//     up to a few; past that failures close like successes;
//   - the window opens in the background: it does not take the focus from the window
//     you are looking at;
//   - everything written is passed through the secret redaction first;
//   - outside screen and tmux nothing opens, and nothing else changes: the command's
//     result and exit code are exactly what they were.

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type liveCfg struct {
	mode   string // "screen" | "tmux" | "" (off)
	after  time.Duration
	max    int
	linger time.Duration
	dir    string
}

const liveKeepFailedMax = 6 // failed windows that wait for Enter; more than this close on their own

var liveWarnOnce sync.Once

// liveConfig reads the environment every time, so a test (or a person) can change it.
func liveConfig() liveCfg {
	c := liveCfg{after: 3 * time.Second, max: 4, linger: 3 * time.Second, dir: "/tmp/einsteinos_live"}
	if d, err := time.ParseDuration(os.Getenv("EINSTEINOS_LIVE_AFTER")); err == nil && d >= 0 {
		c.after = d
	}
	if d, err := time.ParseDuration(os.Getenv("EINSTEINOS_LIVE_LINGER")); err == nil && d >= 0 {
		c.linger = d
	}
	c.max = envInt("EINSTEINOS_LIVE_MAX", c.max)
	if d := os.Getenv("EINSTEINOS_LIVE_DIR"); d != "" {
		c.dir = d
	}
	want := strings.ToLower(strings.TrimSpace(os.Getenv("EINSTEINOS_LIVE")))
	inScreen, inTmux := os.Getenv("STY") != "", os.Getenv("TMUX") != ""
	switch want {
	case "screen":
		if inScreen {
			c.mode = "screen"
		}
	case "tmux":
		if inTmux {
			c.mode = "tmux"
		}
	case "auto":
		switch {
		case inScreen:
			c.mode = "screen"
		case inTmux:
			c.mode = "tmux"
		}
	}
	if want != "" && want != "off" && c.mode == "" {
		liveWarnOnce.Do(func() {
			log.Printf("live: EINSTEINOS_LIVE=%s but this process is not inside that multiplexer ($STY / $TMUX empty): no live windows", want)
		})
	}
	return c
}

// ---- the follow-file ---------------------------------------------------------------------

// liveWriter writes whole redacted lines to the follow-file; a partial line waits for
// its newline. Safe for the two goroutines (stdout, stderr) that exec starts.
type liveWriter struct {
	mu   sync.Mutex
	f    *os.File
	part bytes.Buffer
}

func (w *liveWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return len(p), nil
	}
	w.part.Write(p)
	for {
		b := w.part.Bytes()
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			break
		}
		line := string(b[:i+1])
		w.part.Next(i + 1)
		w.f.WriteString(redactSecrets(line))
	}
	return len(p), nil
}

// flush writes what is left of a last line that had no newline.
func (w *liveWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f != nil && w.part.Len() > 0 {
		w.f.WriteString(redactSecrets(w.part.String()) + "\n")
		w.part.Reset()
	}
}

func (w *liveWriter) close() {
	w.flush()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f != nil {
		w.f.Close()
		w.f = nil
	}
}

// ---- a session: one command's follow-file and window ------------------------------------------

type liveSession struct {
	cfg    liveCfg
	pid    int64
	title  string
	file   string
	w      *liveWriter
	mu     sync.Mutex
	timer  *time.Timer
	opened bool
	done   bool
}

var (
	liveMu          sync.Mutex
	liveOpenCount   int
	liveFailedKept  int
	liveCleanupOnce sync.Once
)

// liveOpenWindow opens the window; a variable so tests can see what would be opened.
var liveOpenWindow = openMuxWindow

var liveVerifyRe = regexp.MustCompile(`\.einsteinOS/verify/(u[0-9a-f]+)/(build|test)\.sh`)

// liveLabel is the window title: what the command is, in a few characters.
func liveLabel(pid int64, label, command string) string {
	if label == "" {
		if m := liveVerifyRe.FindStringSubmatch(command); m != nil {
			label = "verify " + m[1] + " " + m[2]
		} else {
			label = strings.Join(strings.Fields(command), " ")
		}
	}
	var b strings.Builder
	for _, r := range label {
		switch {
		case r < 32 || r == 127:
			r = ' '
		case r == '$' || r == '`' || r == '\\':
			r = '_' // screen expands $VAR in the arguments of -X commands
		}
		b.WriteRune(r)
	}
	t := fmt.Sprintf("pid%d %s", pid, redactSecrets(strings.TrimSpace(b.String())))
	if len(t) > 40 { // room for "pid12345 verify u0123456789 build"
		t = t[:40]
	}
	return t
}

// liveBegin starts the follow-file for a command and arms the timer that opens its
// window. Returns nil when live is off: the caller then does nothing different.
func liveBegin(pid int64, label, command string) *liveSession {
	cfg := liveConfig()
	if cfg.mode == "" {
		return nil
	}
	if err := os.MkdirAll(cfg.dir, 0o700); err != nil {
		return nil
	}
	liveCleanupOnce.Do(func() { liveCleanup(cfg.dir, 24*time.Hour) })
	file := filepath.Join(cfg.dir, fmt.Sprintf("%d_%d.log", pid, time.Now().UnixNano()))
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil
	}
	s := &liveSession{cfg: cfg, pid: pid, file: file, w: &liveWriter{f: f}, title: liveLabel(pid, label, command)}
	hdr := redactSecrets(strings.Join(strings.Fields(command), " "))
	if len(hdr) > 400 {
		hdr = hdr[:400] + "…"
	}
	f.WriteString("$ " + hdr + "\n")
	s.timer = time.AfterFunc(cfg.after, s.openWindow)
	return s
}

func (s *liveSession) writer() io.Writer { return s.w }

// openWindow runs when the command has been running for LIVE_AFTER.
func (s *liveSession) openWindow() {
	s.mu.Lock()
	if s.done || s.opened {
		s.mu.Unlock()
		return
	}
	liveMu.Lock()
	if liveOpenCount >= s.cfg.max {
		liveMu.Unlock()
		s.mu.Unlock()
		return // too many windows already: this one is only in the file
	}
	liveOpenCount++
	keep := liveFailedKept < liveKeepFailedMax
	liveMu.Unlock()
	s.opened = true
	s.mu.Unlock()

	if err := liveOpenWindow(s.cfg, s.title, s.file, keep); err != nil {
		log.Printf("live: could not open a window for %s: %v", s.title, err)
		liveMu.Lock()
		liveOpenCount--
		liveMu.Unlock()
		s.mu.Lock()
		s.opened = false
		s.mu.Unlock()
	}
}

// finish is called when the command has ended.
func (s *liveSession) finish(exit int) {
	s.timer.Stop()
	s.w.close()
	s.mu.Lock()
	s.done = true
	opened := s.opened
	s.mu.Unlock()

	if !opened {
		if exit == 0 {
			os.Remove(s.file) // a fast command that worked leaves nothing behind
		}
		return
	}
	os.WriteFile(s.file+".exit", []byte(strconv.Itoa(exit)), 0o600) // the window is waiting for this
	liveMu.Lock()
	liveOpenCount--
	if exit != 0 && liveFailedKept < liveKeepFailedMax {
		liveFailedKept++
	}
	liveMu.Unlock()
}

// liveCleanup removes follow-files older than maxAge.
func liveCleanup(dir string, maxAge time.Duration) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > maxAge {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// ---- the window --------------------------------------------------------------------------------

// liveWindowScript runs INSIDE the new window: $1 file, $2 linger seconds, $3 title,
// $4 keep (1: a failure waits for Enter; 0: it closes like a success).
// It follows the file until <file>.exit appears, then shows the exit code.
const liveWindowScript = `f="$1"; linger="$2"; title="$3"; keep="$4"
tail -n +1 -f "$f" 2>/dev/null &
t=$!
while [ ! -e "$f.exit" ]; do sleep 0.2; done
sleep 0.4
kill "$t" 2>/dev/null
c=$(cat "$f.exit" 2>/dev/null); c=${c:-?}
echo
if [ "$c" = 0 ] || [ "$keep" != 1 ]; then
  echo "[exit $c — closing in ${linger}s]"; sleep "$linger"
else
  [ -n "$STY" ] && printf '\033kFAIL %s\033\\' "$title"
  echo "[exit $c — FAILED — press Enter to close this window]"; read -r _
fi
`

func secs(d time.Duration) string { return strconv.Itoa(int((d + time.Second - 1) / time.Second)) }

// liveWindowScriptFile writes the window script next to the follow-files and returns
// its path. It is a file, not an argument: screen expands $VAR inside the arguments of
// `-X screen ...`, which turned the script's "$1" into nothing and left a window that
// never saw its file name.
func liveWindowScriptFile(dir string) (string, error) {
	p := filepath.Join(dir, "window.sh")
	if b, err := os.ReadFile(p); err == nil && string(b) == liveWindowScript {
		return p, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return p, os.WriteFile(p, []byte(liveWindowScript), 0o700)
}

// openMuxWindow opens the window in screen or tmux, in the background.
func openMuxWindow(cfg liveCfg, title, file string, keep bool) error {
	k := "0"
	if keep {
		k = "1"
	}
	script, err := liveWindowScriptFile(cfg.dir)
	if err != nil {
		return err
	}
	args := []string{"bash", script, file, secs(cfg.linger), title, k}
	switch cfg.mode {
	case "screen":
		sty := os.Getenv("STY")
		// remember the window the person is looking at: `screen` selects the new one,
		// and we put the focus back (best effort: a detached session has no display)
		cur := ""
		if out, err := exec.Command("screen", "-S", sty, "-Q", "number").Output(); err == nil {
			if f := strings.Fields(string(out)); len(f) > 0 {
				cur = f[0]
			}
		}
		a := append([]string{"-S", sty, "-X", "screen", "-t", title}, args...)
		if out, err := exec.Command("screen", a...).CombinedOutput(); err != nil {
			return fmt.Errorf("screen: %v: %s", err, strings.TrimSpace(string(out)))
		}
		if _, err := strconv.Atoi(cur); err == nil {
			exec.Command("screen", "-S", sty, "-X", "select", cur).Run()
		}
		return nil
	case "tmux":
		var q []string
		for _, x := range args {
			q = append(q, shq(x))
		}
		if out, err := exec.Command("tmux", "new-window", "-d", "-n", title, strings.Join(q, " ")).CombinedOutput(); err != nil {
			return fmt.Errorf("tmux: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return fmt.Errorf("no multiplexer")
}
