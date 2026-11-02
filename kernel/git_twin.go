package kernel

// git_twin.go — keep the local machine's copy of what a child cloned in its
// sandbox, without running the child's git commands.
//
// The shadow never ran git commands locally (they need the child's credentials,
// they hang on prompts, and their output — truncated to 2000 characters — used to
// overwrite the sandbox's complete result). But a skipped `git clone` leaves the
// local machine without the directory, and the next command that reads it runs
// locally, fails with "No such file or directory", and that failure is written
// back as the real output: in the 2026/11/02 run three theory conversations lost
// their Lean proof examples (/tmp/openai_math) that way, and a fourth could not
// read a previous chunk (/tmp/devrepo).
//
// The rule now, for a command the shadow would skip because it contains git:
//
//   - `git clone URL [DIR]`: if DIR already holds a clone with the same origin and
//     its HEAD equals what `git ls-remote` reports, do nothing (no re-clone per
//     chunk). Otherwise the KERNEL clones — shallow, with its own credentials. The
//     child's URL is only parsed: its embedded token is dropped, never used.
//   - checkout / switch / pull / fetch / reset / merge / rebase / submodule: replayed
//     only inside a directory this code cloned (marked by .git/einsteinos_twin).
//   - everything else (push, commit, add, config, remote, init, log...) is ignored:
//     the kernel pushes, and the sandbox has the full output of the rest.
//
// A directory that is not ours is never touched, and never one of the kernel's own
// (the dev mirror must stay free of .git; see dev_verify.go).

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const twinMarker = "einsteinos_twin"

// twinAction is one thing the kernel should do for a git command.
type twinAction struct {
	Kind   string // "clone" | "read"
	URL    string // clone: as the child wrote it (may carry a token: never used)
	Dir    string // absolute directory: the clone target, or the repo a read applies to
	Branch string // clone -b
	Args   []string
}

// ---- parsing --------------------------------------------------------------------------

// splitSegments splits a command at unquoted &&, ||, ;, | and newlines, and stops
// at the first heredoc (`<<`): what follows is a body, not commands.
func splitSegments(cmd string) []string {
	cmd = strings.ReplaceAll(cmd, "\r\n", "\n")
	var segs []string
	var cur strings.Builder
	var quote rune
	rs := []rune(cmd)
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segs = append(segs, s)
		}
		cur.Reset()
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote != 0:
			cur.WriteRune(r)
			if r == '\\' && quote == '"' && i+1 < len(rs) {
				i++
				cur.WriteRune(rs[i])
			} else if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
			cur.WriteRune(r)
		case r == '<' && i+1 < len(rs) && rs[i+1] == '<':
			flush()
			return segs
		case r == '&' && i+1 < len(rs) && rs[i+1] == '&', r == '|' && i+1 < len(rs) && rs[i+1] == '|':
			flush()
			i++
		case r == ';' || r == '|' || r == '\n':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return segs
}

var envAssignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// redirRe: a token that is (or starts with) a redirection: >, >>, <, 2>, 2>&1, &>, >&2 ...
var redirRe = regexp.MustCompile(`^([0-9]*[<>]|&>)`)

// stripRedirections removes redirections from a segment's tokens, and the file name
// that follows a bare one (`> file`, `2> file`).
func stripRedirections(toks []string) []string {
	var out []string
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if redirRe.MatchString(t) {
			// "2>&1", ">file": the target is attached; ">", "2>>", "<": it is the next token
			bare := strings.Trim(t, "0123456789<>&")
			if bare == "" && !strings.Contains(t, "&") && i+1 < len(toks) {
				i++
			}
			continue
		}
		out = append(out, t)
	}
	return out
}

// cloneOptWithValue: git clone options whose next token is their value.
var cloneOptWithValue = map[string]bool{
	"-b": true, "--branch": true, "--depth": true, "-c": true, "--config": true, "-o": true, "--origin": true,
	"--reference": true, "--template": true, "-j": true, "--jobs": true, "--separate-git-dir": true,
	"--filter": true, "--shallow-since": true, "--shallow-exclude": true, "-u": true, "--upload-pack": true,
}

var twinReadCmds = map[string]bool{
	"checkout": true, "switch": true, "pull": true, "fetch": true, "reset": true,
	"merge": true, "rebase": true, "submodule": true,
}

// resolveDir joins a directory token onto cwd; "" means it cannot be known.
func resolveDir(cwd, d string) string {
	switch {
	case d == "" || strings.ContainsAny(d, "$`<>()&|;") || strings.HasPrefix(d, "~") || d == "-":
		// shell metacharacters are not part of a directory name: "2>&1" read as a clone
		// target made /home/claude/2>&1, and "baloon)" made a directory with a bracket in it
		return ""
	case strings.HasPrefix(d, "/"):
		return path.Clean(d)
	case cwd != "":
		return path.Join(cwd, d)
	}
	return ""
}

// parseGitTwinActions reads the git commands of a command line, following cd.
func parseGitTwinActions(command, workDir string) []twinAction {
	cwd := workDir
	var acts []twinAction
	for _, seg := range splitSegments(command) {
		// a subshell or a brace group around the command: "(git clone u d)"
		seg = strings.TrimSpace(strings.Trim(strings.TrimSpace(seg), "(){}"))
		toks := stripRedirections(splitArgs(seg))
		for len(toks) > 0 && envAssignRe.MatchString(toks[0]) {
			toks = toks[1:]
		}
		if len(toks) == 0 {
			continue
		}
		if toks[0] == "cd" {
			if len(toks) >= 2 {
				cwd = resolveDir(cwd, toks[1])
			}
			continue
		}
		if toks[0] != "git" {
			continue
		}
		toks = toks[1:]
		repoDir := cwd
		skip := false
		for len(toks) > 0 && strings.HasPrefix(toks[0], "-") {
			switch {
			case toks[0] == "-C" && len(toks) >= 2:
				repoDir = resolveDir(repoDir, toks[1])
				toks = toks[2:]
			case toks[0] == "-c" && len(toks) >= 2:
				toks = toks[2:]
			case strings.HasPrefix(toks[0], "--git-dir"), strings.HasPrefix(toks[0], "--work-tree"):
				skip = true
				toks = toks[1:]
			default:
				toks = toks[1:]
			}
		}
		if skip || len(toks) == 0 {
			continue
		}
		sub, rest := toks[0], toks[1:]
		switch {
		case sub == "clone":
			var pos []string
			branch := ""
			for i := 0; i < len(rest); i++ {
				t := rest[i]
				switch {
				case strings.HasPrefix(t, "--branch="):
					branch = strings.TrimPrefix(t, "--branch=")
				case t == "-b" || t == "--branch":
					if i+1 < len(rest) {
						branch = rest[i+1]
						i++
					}
				case strings.HasPrefix(t, "-"):
					if cloneOptWithValue[t] && i+1 < len(rest) {
						i++
					}
				default:
					pos = append(pos, t)
				}
			}
			if len(pos) == 0 {
				continue
			}
			url := pos[0]
			target := ""
			if len(pos) >= 2 {
				target = pos[1]
			} else {
				base := path.Base(strings.TrimRight(url, "/"))
				if i := strings.LastIndex(base, ":"); i >= 0 {
					base = base[i+1:]
				}
				target = strings.TrimSuffix(base, ".git")
			}
			dir := resolveDir(cwd, target)
			if dir == "" {
				continue
			}
			acts = append(acts, twinAction{Kind: "clone", URL: url, Dir: dir, Branch: branch})
		case twinReadCmds[sub] && repoDir != "":
			acts = append(acts, twinAction{Kind: "read", Dir: repoDir, Args: append([]string{sub}, rest...)})
		}
	}
	return acts
}

// ---- URLs and directories -----------------------------------------------------------------

var scpURLRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]+@([A-Za-z0-9_.\-]+):(.+)$`)

// normTwinURL returns the URL the KERNEL clones from, and a key for comparing
// origins. The child's credentials are dropped. A GitHub repo that is one of the
// run's dev repos is cloned with the kernel's own credentials (CloneURL); any other
// is cloned anonymously, so a child's request never reaches further than what is
// public.
func normTwinURL(raw string) (cloneURL, key string, ok bool) {
	raw = strings.TrimSpace(strings.Trim(raw, `"'`))
	if raw == "" {
		return "", "", false
	}
	if m := scpURLRe.FindStringSubmatch(raw); m != nil && !strings.Contains(raw, "://") {
		raw = "https://" + m[1] + "/" + m[2]
	}
	if strings.HasPrefix(raw, "ssh://") {
		raw = "https://" + strings.TrimPrefix(raw, "ssh://")
		if i := strings.Index(raw, "@"); i >= 0 {
			raw = "https://" + raw[i+1:]
		}
	}
	switch {
	case strings.HasPrefix(raw, "https://"), strings.HasPrefix(raw, "http://"):
		rest := strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
		if i := strings.Index(rest, "@"); i >= 0 && i < strings.Index(rest, "/") {
			rest = rest[i+1:] // userinfo (a token) dropped
		}
		rest = strings.TrimSuffix(rest, "/")
		key = strings.ToLower(strings.TrimSuffix(rest, ".git"))
		cloneURL = "https://" + rest
		if !strings.HasSuffix(cloneURL, ".git") {
			cloneURL += ".git"
		}
		if strings.HasPrefix(key, "github.com/") {
			ownerRepo := strings.TrimPrefix(key, "github.com/")
			for _, dr := range GetDevRepos() {
				if strings.EqualFold(dr, ownerRepo) {
					cloneURL = CloneURL(dr)
				}
			}
		}
		return cloneURL, key, true
	case strings.HasPrefix(raw, "file://"):
		return raw, strings.TrimSuffix(strings.TrimPrefix(raw, "file://"), ".git"), true
	}
	return "", "", false
}

// originKey normalizes an origin URL read from a clone, for comparison.
func originKey(origin string) string {
	_, key, ok := normTwinURL(origin)
	if !ok {
		return strings.TrimSuffix(strings.TrimSpace(origin), ".git")
	}
	return key
}

// twinDirAllowed: where the kernel may keep a twin. Only under /tmp or
// /home/claude, never the kernel's own directories: the dev mirror (which must
// never hold a .git: devRepoLocalDir would take it for the kernel's clone), the
// kernel's clones and chunk scratch, or the theory outputs directory.
func twinDirAllowed(dir string) bool {
	dir = path.Clean(dir)
	if !strings.HasPrefix(dir, "/") {
		return false
	}
	underTmp := strings.HasPrefix(dir, "/tmp/") && strings.Count(dir, "/") >= 2
	underHome := strings.HasPrefix(dir, "/home/claude/") && strings.Count(dir, "/") >= 3
	if !underTmp && !underHome {
		return false
	}
	for _, p := range []string{"/tmp/ref_store_", "/tmp/chunk_", "/tmp/einsteinos_"} {
		if strings.HasPrefix(dir, p) {
			return false
		}
	}
	if dir == TheoryOutputsDir || strings.HasPrefix(dir, TheoryOutputsDir+"/") {
		return false
	}
	for _, dr := range GetDevRepos() {
		m := devMirrorDir(dr)
		if dir == m || strings.HasPrefix(dir, m+"/") {
			return false
		}
	}
	return true
}

// ---- running git -----------------------------------------------------------------------------

// twinGit runs one git command line and returns its exit code and output. A
// variable so a test can count network operations.
var twinGit = func(dir, cmdline string, timeout time.Duration) (int, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, "bash", "-c", cmdline)
	if dir != "" {
		c.Dir = dir
	}
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true")
	out, err := c.CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), string(out)
		}
		return -1, string(out) + err.Error()
	}
	return 0, string(out)
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

var (
	twinMu       sync.Mutex          // guards twinDirs and dirLocks only: never held across git
	twinDirs     = map[string]bool{} // directories this process keeps (or is making) twins in
	dirLocks     = map[string]*sync.Mutex{}
	gitTwinWG    sync.WaitGroup // background work, for tests and a clean exit
	twinCloneMax = 90 * time.Second
	twinFetchMax = 60 * time.Second
	twinProbeMax = 30 * time.Second
)

// lockDir serialises the operations on ONE directory without blocking the others.
func lockDir(dir string) func() {
	twinMu.Lock()
	m := dirLocks[dir]
	if m == nil {
		m = &sync.Mutex{}
		dirLocks[dir] = m
	}
	twinMu.Unlock()
	m.Lock()
	return m.Unlock
}

func setTwinDir(dir string, on bool) {
	twinMu.Lock()
	if on {
		twinDirs[dir] = true
	} else {
		delete(twinDirs, dir)
	}
	twinMu.Unlock()
}

func isTwin(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git", twinMarker))
	return err == nil
}

func firstField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ensureTwin makes dir a current shallow clone of url, doing as little as it can.
// It holds only dir's own lock while it runs git, so a slow clone blocks nothing else.
func ensureTwin(a twinAction) string {
	cloneURL, key, ok := normTwinURL(a.URL)
	if !ok {
		return fmt.Sprintf("clone %s: not a URL the kernel clones from, ignored", truncStr(a.URL, 60))
	}
	if !twinDirAllowed(a.Dir) {
		return fmt.Sprintf("clone into %s refused (not a place the kernel keeps twins)", a.Dir)
	}
	unlock := lockDir(a.Dir)
	defer unlock()

	ref := "HEAD"
	if a.Branch != "" {
		ref = "refs/heads/" + a.Branch
	}

	st, err := os.Stat(a.Dir)
	exists := err == nil && st.IsDir()
	if exists {
		if _, gerr := os.Stat(filepath.Join(a.Dir, ".git")); gerr != nil {
			if ents, _ := os.ReadDir(a.Dir); len(ents) > 0 {
				return fmt.Sprintf("%s exists and is not a git repository, left alone", a.Dir)
			}
			exists = false // an empty directory is a place to clone into
		}
	}
	if exists {
		_, origin := twinGit(a.Dir, "git remote get-url origin", twinProbeMax)
		if originKey(strings.TrimSpace(origin)) != key {
			return fmt.Sprintf("%s exists with a different origin (%s), left alone", a.Dir, truncStr(strings.TrimSpace(origin), 60))
		}
		code, remote := twinGit(a.Dir, "git ls-remote "+shq(cloneURL)+" "+shq(ref), twinProbeMax)
		_, local := twinGit(a.Dir, "git rev-parse HEAD", twinProbeMax)
		rsha, lsha := firstField(remote), firstField(local)
		if code == 0 && rsha != "" && rsha == lsha {
			setTwinDir(a.Dir, true)
			return fmt.Sprintf("%s is at the remote HEAD (%s), skipped", a.Dir, rsha[:minInt(8, len(rsha))])
		}
		if !isTwin(a.Dir) {
			return fmt.Sprintf("%s is not a twin of ours and is behind, left alone", a.Dir)
		}
		if code != 0 {
			return fmt.Sprintf("%s: ls-remote failed (exit %d), kept as is", a.Dir, code)
		}
		fetchRef := "HEAD"
		if a.Branch != "" {
			fetchRef = a.Branch
		}
		c, out := twinGit(a.Dir, "git fetch --depth 1 origin "+shq(fetchRef)+" && git reset --hard FETCH_HEAD", twinFetchMax)
		if c != 0 {
			return fmt.Sprintf("%s: update failed (exit %d): %s", a.Dir, c, truncStr(strings.TrimSpace(out), 120))
		}
		setTwinDir(a.Dir, true)
		return fmt.Sprintf("%s updated to the remote HEAD", a.Dir)
	}

	if err := os.MkdirAll(path.Dir(a.Dir), 0o755); err != nil {
		setTwinDir(a.Dir, false)
		return fmt.Sprintf("clone into %s: %v", a.Dir, err)
	}
	cmdline := "git clone --depth 1 "
	if a.Branch != "" {
		cmdline += "-b " + shq(a.Branch) + " "
	}
	cmdline += shq(cloneURL) + " " + shq(a.Dir)
	code, out := twinGit("/tmp", cmdline, twinCloneMax)
	if code != 0 {
		os.RemoveAll(a.Dir)
		setTwinDir(a.Dir, false) // not a twin after all: local commands run normally again
		return fmt.Sprintf("clone of %s failed (exit %d): %s", key, code, truncStr(strings.TrimSpace(redactSecrets(out)), 120))
	}
	os.WriteFile(filepath.Join(a.Dir, ".git", twinMarker), []byte(key+"\n"), 0o644)
	setTwinDir(a.Dir, true)
	return fmt.Sprintf("cloned %s into %s (shallow, kernel credentials)", key, a.Dir)
}

// replayRead runs a read-only git command in a twin; its output is discarded (the
// sandbox's own output is what the child sees).
func replayRead(a twinAction) string {
	if !twinDirAllowed(a.Dir) || !isTwin(a.Dir) {
		return ""
	}
	unlock := lockDir(a.Dir)
	defer unlock()
	quoted := make([]string, len(a.Args))
	for i, x := range a.Args {
		quoted[i] = shq(x)
	}
	code, _ := twinGit(a.Dir, "git "+strings.Join(quoted, " "), 2*twinFetchMax)
	return fmt.Sprintf("replayed git %s in %s (exit %d)", a.Args[0], a.Dir, code)
}

// GitTwinSync starts the local side of the git commands in a command line the shadow
// is about to skip. It returns at once: the work runs in the background, in order.
// A clone takes as long as the network lets it (a TLS handshake that never finishes
// held the whole shadow batch for two minutes), and nothing waits for it: a command
// that reads inside a twin is answered by the sandbox anyway (GitTwinTouches), and the
// directory is marked as a twin from the moment its clone starts.
func GitTwinSync(command, workDir string) []string {
	acts := parseGitTwinActions(command, workDir)
	if len(acts) == 0 {
		return nil
	}
	for _, a := range acts {
		if a.Kind == "clone" {
			if _, _, ok := normTwinURL(a.URL); ok && twinDirAllowed(a.Dir) {
				setTwinDir(a.Dir, true) // pending: reads inside it are the sandbox's to answer
			}
		}
	}
	gitTwinWG.Add(1)
	go func() {
		defer gitTwinWG.Done()
		for _, a := range acts {
			var n string
			switch a.Kind {
			case "clone":
				n = ensureTwin(a)
			case "read":
				n = replayRead(a)
			}
			if n != "" {
				log.Printf("git_twin: %s", n)
			}
		}
	}()
	return nil
}

// GitTwinTouches reports whether a command reads inside a directory the kernel keeps
// (or is making) a twin in. The twin exists so that local state is true, not so that a
// local run answers the child: such a read is better answered by the sandbox, whose
// output is complete (the local result is cut at LocalResultCap).
func GitTwinTouches(command string) bool {
	twinMu.Lock()
	defer twinMu.Unlock()
	for d := range twinDirs {
		if strings.Contains(command, d) {
			return true
		}
	}
	return false
}
