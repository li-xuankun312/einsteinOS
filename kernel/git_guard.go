package kernel

// git_guard.go — run a git/curl command, but give up on a connection that never
// comes alive so the caller can start a fresh process (what a person does with
// Ctrl+C on a stuck `git pull`: a new process opens a new connection through the
// proxy, and that one often works).
//
// What the old shell guard got wrong: it declared "no output in 10 s" a hang. A
// `git fetch` of a repository that is already up to date SUCCEEDS with exit 0 and
// prints nothing at all, so every such fetch was killed-and-retried: ten times,
// ten failures, at 10 s each. The logs show it twice — a fresh clone (which prints
// "Cloning into ...") worked on attempt 1, while fetches of the existing clone went
// 0 for 10 on 10/28 16:32 and 0 for 10 on 10/30 01:06 — and the first attempt of
// each could never have succeeded.
//
// Liveness here is the process itself: it is hung only if it is still running,
// with no output, when the start window ends. A process that exits — with any
// output or none — has started.

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// no output and still running after this long: the connection is stuck.
	gitStartWindow = 10 * time.Second
	// after it has started, a command may run this long before it is killed.
	gitMaxRun      = 180 * time.Second
	gitCloneMaxRun = 300 * time.Second
	// a push that is genuinely transferring data (confirmed by --progress output,
	// so it is not stuck at "no connection") gets longer before giving up — a
	// large diff over a slow proxy legitimately takes a while.
	// a push that keeps producing genuinely new --progress content is no longer
	// killed at a flat ceiling (see gitPushStallTimeout below) — this is now just
	// the absolute backstop for a push that somehow keeps "progressing" forever
	// without ever finishing. Large on purpose: it should practically never fire
	// for a real push; gitPushStallTimeout is what actually ends a dead one.
	gitPushMaxRun = 15 * time.Minute
	// once a push has shown at least one real progress line, this is how long it
	// may go with NO NEW distinct progress line before it's considered stalled
	// (not "never connected" — that's startWindow's job — but "connected, then
	// stopped moving") and killed for a fresh retry.
	gitPushStallTimeout = 45 * time.Second
	// how often a long-running push reports its latest --progress line, once it
	// has started producing output. Independent of the 50ms internal poll —
	// that's for responsiveness to the process exiting/timing out, this is for
	// how chatty the log gets.
	progressInterval = 5 * time.Second
	// attempts for clone/fetch. A person needs ~9 manual tries on a bad day; at
	// 10 s per hung attempt, 30 gives about five minutes.
	gitDefaultAttempts = 30
	// 3x gitDefaultAttempts: a real run saw 20/20 independent push attempts fail
	// with zero bytes in a row, well past what the clone/fetch ceiling needed in
	// its worst observed case (8 attempts). At 10s per zero-byte failure this is
	// a 15-minute worst-case wall time if the connection truly never comes up —
	// bounded, and acceptable for a background pipeline step.
	gitPushDefaultAttempts = 90
)

// gitNetAttempts is the attempt budget for clone/fetch; EINSTEIN_GIT_ATTEMPTS overrides it.
func gitNetAttempts() int {
	if v := strings.TrimSpace(os.Getenv("EINSTEIN_GIT_ATTEMPTS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return gitDefaultAttempts
}

// gitPushAttempts: how many independent connection attempts a push gets. This
// is a SEPARATE budget from gitNetAttempts (clone/fetch) because push has shown
// a markedly worse zero-byte ("never connected within the 10s start window")
// failure rate on this project's network path — a real run hit 20/20 straight
// zero-byte failures (3m10s, no partial connection, no progress) even though
// manual single retries typically succeed within 1-2 tries. That gap matters:
// the stall-timeout/live-progress work (git_ops.go's GitPushRetry) only helps
// once a connection partially works; it does nothing for a connection that
// never starts at all, which is what needs MORE independent rolls of the dice,
// not more patience within one roll.
func gitPushAttempts() int {
	if v := strings.TrimSpace(os.Getenv("EINSTEIN_GIT_PUSH_ATTEMPTS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return gitPushDefaultAttempts
}

// gitGuardResult is what one guarded command did.
type gitGuardResult struct {
	Out      string
	Code     int  // exit code; -1 when killed
	Started  bool // false: no sign of life within the start window; killed
	TimedOut bool // started, but still running at maxRun; killed
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Len()
}
func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode() // -1 when killed by a signal
	}
	return -1
}

// gitGuard runs gitCmd under bash in workDir.
//
// onTick, if non-nil, is called roughly every progressInterval while the
// command is still running and has produced at least one byte of output — the
// live equivalent of --progress's "Counting objects... Writing objects..."
// updates, for a caller that wants to show its operator the command is
// actually connected and moving, not just silently wait up to maxRun for one
// final verdict. It receives the elapsed time and the most recent non-empty
// line of output seen so far (see lastProgressLine). Pass nil for the
// clone/fetch callers, which don't need this chatter.
//
// stallTimeout, if nonzero, changes what "stuck" means once real progress has
// been seen: a command that keeps producing genuinely NEW progress lines is
// kept running past maxRun's old role as a flat ceiling — it is only killed
// once stallTimeout passes with NO new progress content (the same distinct
// line repeating, or nothing at all). A push that connects and is advancing
// should not be killed-and-restarted from zero at a fixed clock; a push that
// connects, shows one line, then goes quiet, should not be allowed to sit for
// the full (now much larger) maxRun before anyone notices it died. maxRun
// remains the absolute backstop either way — pass 0 to disable stall-based
// killing entirely (clone/fetch's case: unchanged flat-ceiling behavior).
func gitGuard(gitCmd, workDir string, startWindow, maxRun, stallTimeout time.Duration, onTick func(elapsed time.Duration, lastLine string)) gitGuardResult {
	cmd := exec.Command("bash", "-c", gitCmd)
	cmd.Dir = workDir
	setProcGroup(cmd) // git spawns git-remote-https; kill them all, not just bash
	cmd.WaitDelay = 2 * time.Second
	var out lockedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return gitGuardResult{Code: -1} // could not even start: same as "not started", the caller retries
	}
	// this command's own process group is isolated from the terminal's (see
	// proc_registry.go) — track it so a Ctrl-C to THIS program can still kill it.
	TrackProcessGroup(cmd.Process.Pid)
	defer UntrackProcessGroup(cmd.Process.Pid)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	start := time.Now()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	sawOutput := false
	lastReported := "" // onTick's own cadence (progressInterval) — how often we LOG
	var lastReportAt time.Duration
	lastSeenLine := "" // stall detection — checked every poll, independent of onTick's
	// slower logging cadence: WHEN we give up depends on real content changing, not
	// on how often we chose to tell the operator about it
	var lastProgressAt time.Duration
	for {
		select {
		case err := <-done:
			return gitGuardResult{Out: out.String(), Code: exitCodeOf(err), Started: true}
		case <-tick.C:
			el := time.Since(start)
			if !sawOutput && out.Len() > 0 {
				sawOutput = true
			}
			if !sawOutput && el >= startWindow {
				killProcGroup(cmd)
				<-done
				return gitGuardResult{Code: -1}
			}
			if line := lastProgressLine(out.String()); line != lastSeenLine {
				lastSeenLine = line
				lastProgressAt = el
			}
			stalled := stallTimeout > 0 && sawOutput && el-lastProgressAt >= stallTimeout
			if el >= maxRun || stalled {
				killProcGroup(cmd)
				<-done
				return gitGuardResult{Out: out.String(), Code: -1, Started: true, TimedOut: true}
			}
			if onTick != nil && sawOutput && el-lastReportAt >= progressInterval {
				if line := lastProgressLine(out.String()); line != "" && line != lastReported {
					onTick(el, line)
					lastReported = line
				}
				lastReportAt = el
			}
		}
	}
}

// gitStartGuard is the shape the push / create-PR callers use: (output, started).
// started=false means "hung, or failed with nothing to show", i.e. retry. A
// command that exits 0 with no output has started (a silent success).
func gitStartGuard(gitCmd string, workDir string) (string, bool) {
	r := gitGuard(gitCmd, workDir, gitStartWindow, gitMaxRun, 0, nil)
	if !r.Started {
		return "", false
	}
	if strings.TrimSpace(r.Out) == "" && r.Code != 0 {
		return "", false // e.g. `curl -s` that failed without a word: retry as before
	}
	return r.Out, true
}

// lastProgressLine returns the last non-empty line of git --progress output,
// which uses \r to overwrite a line in a real terminal; when redirected (as we
// do) those \r-separated updates land as one blob. Splitting on both \r and \n
// and taking the last non-empty piece gives the most recent status (e.g.
// "Writing objects: 73% (12/16)") instead of the raw multi-kilobyte blob.
func lastProgressLine(s string) string {
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}
