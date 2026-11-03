package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// a bare "remote" with main and einsteinOS/theory, and a clone one commit behind on theory
func fetchFixture(t *testing.T) (remote, clone string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	clone = filepath.Join(root, "clone")
	gitT(t, root, "init", "-q", "--bare", "-b", "main", remote)
	gitT(t, root, "clone", "-q", remote, work)
	gitT(t, work, "commit", "-q", "--allow-empty", "-m", "init")
	gitT(t, work, "push", "-q", "origin", "HEAD:main")
	gitT(t, work, "push", "-q", "origin", "HEAD:refs/heads/einsteinOS/theory")
	gitT(t, root, "clone", "-q", remote, clone)
	gitT(t, work, "commit", "-q", "--allow-empty", "-m", "chunk 3 merged")
	gitT(t, work, "push", "-q", "origin", "HEAD:refs/heads/einsteinOS/theory")
	return remote, clone
}

func withRemotes(t *testing.T, sshAvail bool, ssh, https string) {
	t.Helper()
	oldR, oldS := gitRemoteFn, gitSSHFn
	t.Cleanup(func() { gitRemoteFn, gitSSHFn = oldR, oldS })
	gitSSHFn = func() bool { return sshAvail }
	gitRemoteFn = func(_ string, useSSH bool) (string, string) {
		if useSSH {
			return ssh, ""
		}
		return https, ""
	}
}

func TestGitFetchRefsBringsTheNamedBranchesUpToDate(t *testing.T) {
	remote, clone := fetchFixture(t)
	withRemotes(t, true, remote, remote)
	missing, err := gitFetchRefs(clone, "o/r", "einsteinOS/theory", "main", "einsteinOS/theory")
	if err != nil || len(missing) != 0 {
		t.Fatalf("%v %v", missing, err)
	}
	if gitT(t, clone, "rev-parse", "origin/einsteinOS/theory") != gitT(t, remote, "rev-parse", "einsteinOS/theory") {
		t.Fatal("origin/einsteinOS/theory not at the remote's tip")
	}
}

func TestGitFetchRefsReportsAMissingBranchWithoutRetrying(t *testing.T) {
	remote, clone := fetchFixture(t)
	withRemotes(t, false, "", remote)
	missing, err := gitFetchRefs(clone, "o/r", "einsteinOS/dev-new", "einsteinOS/theory")
	if err != nil || !reflect.DeepEqual(missing, []string{"einsteinOS/dev-new"}) {
		t.Fatalf("missing=%v err=%v", missing, err)
	}
	if gitT(t, clone, "rev-parse", "origin/einsteinOS/theory") != gitT(t, remote, "rev-parse", "einsteinOS/theory") {
		t.Fatal("the branch the remote has was not fetched")
	}
}

func TestGitFetchRefsGoesToHTTPSWhenSSHKeepsFailing(t *testing.T) {
	remote, clone := fetchFixture(t)
	withRemotes(t, true, filepath.Join(t.TempDir(), "no-such.git"), remote)
	if _, err := gitFetchRefs(clone, "o/r", "einsteinOS/theory"); err != nil {
		t.Fatalf("after %d SSH failures the HTTPS remote should have answered: %v", gitSSHFetchFailuresBeforeHTTPS, err)
	}
}

func TestOriginOwnerRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gitT(t, dir, "init", "-q")
	gitT(t, dir, "remote", "add", "origin", "x")
	for url, want := range map[string]string{
		"https://x-token@github.com/zeng-bin3123/baloon.git": "zeng-bin3123/baloon",
		"git@github.com:zeng-bin3123/baloon.git":             "zeng-bin3123/baloon",
		"https://github.com/o/r":                             "o/r",
		"/tmp/some/local.git":                                "",
	} {
		gitT(t, dir, "remote", "set-url", "origin", url)
		if got := originOwnerRepo(dir); got != want {
			t.Errorf("%s → %q want %q", url, got, want)
		}
	}
}

// what decides a new attempt is progress: never alive, or alive and then no new
// --progress line for gitPushStallTimeout — not a fixed wait
func TestGitFetchRefsRestartsOnNoProgressNotOnATimer(t *testing.T) {
	withRemotes(t, false, "", "https://example.invalid/o/r.git")
	old := gitGuardFn
	t.Cleanup(func() { gitGuardFn = old })
	var calls []string
	results := []gitGuardResult{{Code: -1}, {Started: true, TimedOut: true, Code: -1}, {Started: true, Code: 0}}
	gitGuardFn = func(cmd, dir string, start, maxRun, stall time.Duration, tick func(time.Duration, string)) gitGuardResult {
		if start != gitStartWindow || stall != gitPushStallTimeout || !strings.Contains(cmd, "fetch --progress") {
			t.Fatalf("not judged by progress: start=%v stall=%v cmd=%s", start, stall, cmd)
		}
		calls = append(calls, cmd)
		return results[len(calls)-1]
	}
	if _, err := gitFetchRefs(t.TempDir(), "o/r", "einsteinOS/theory"); err != nil || len(calls) != 3 {
		t.Fatalf("calls=%d err=%v", len(calls), err)
	}
}
