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
	gitPushMaxRun = 120 * time.Second
	// attempts for clone/fetch. A person needs ~9 manual tries on a bad day; at
	// 10 s per hung attempt, 30 gives about five minutes.
	gitDefaultAttempts = 30
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
func gitGuard(gitCmd, workDir string, startWindow, maxRun time.Duration) gitGuardResult {
	cmd := exec.Command("bash", "-c", gitCmd)
	cmd.Dir = workDir
	setProcGroup(cmd) // git spawns git-remote-https; kill them all, not just bash
	cmd.WaitDelay = 2 * time.Second
	var out lockedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return gitGuardResult{Code: -1} // could not even start: same as "not started", the caller retries
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	start := time.Now()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	sawOutput := false
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
			if el >= maxRun {
				killProcGroup(cmd)
				<-done
				return gitGuardResult{Out: out.String(), Code: -1, Started: true, TimedOut: true}
			}
		}
	}
}

// gitStartGuard is the shape the push / create-PR callers use: (output, started).
// started=false means "hung, or failed with nothing to show", i.e. retry. A
// command that exits 0 with no output has started (a silent success).
func gitStartGuard(gitCmd string, workDir string) (string, bool) {
	r := gitGuard(gitCmd, workDir, gitStartWindow, gitMaxRun)
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
