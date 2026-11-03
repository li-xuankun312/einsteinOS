package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFetchCommandUsesTheDeployKeyOverSSHAndNeverTouchesOrigin(t *testing.T) {
	t.Setenv("EINSTEIN_GIT_SSH_KEY", "/root/.ssh/einsteinos_deploy")
	ssh := gitFetchCmd("/tmp/ref_store_baloon", "zeng-bin3123/baloon", true)
	for _, want := range []string{"GIT_SSH_COMMAND=", "einsteinos_deploy", "IdentitiesOnly=yes", "git@github.com:zeng-bin3123/baloon.git", "+refs/heads/*:refs/remotes/origin/*"} {
		if !strings.Contains(ssh, want) {
			t.Fatalf("ssh fetch lacks %q: %s", want, ssh)
		}
	}
	if strings.Contains(ssh, "remote set-url") || strings.Contains(ssh, " fetch origin") {
		t.Fatalf("origin must stay as it is (other fetches in this clone run without GIT_SSH_COMMAND): %s", ssh)
	}
	if https := gitFetchCmd("/tmp/x", "o/r", false); https != `git -C "/tmp/x" fetch origin` {
		t.Fatalf("the HTTPS form is the old one: %s", https)
	}
}

// GitCloneOrReuse against a scripted runner: what it tries, in what order, and when it gives SSH up
func TestFetchFallsBackToHTTPSAfterTwoSSHFailuresAndSaysWhichItUsed(t *testing.T) {
	keyDir := t.TempDir()
	key := filepath.Join(keyDir, "deploy")
	os.WriteFile(key, []byte("k"), 0o600)
	t.Setenv("EINSTEIN_GIT_SSH_KEY", key)
	dir := filepath.Join(t.TempDir(), "clone")
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)

	var cmds []string
	old := gitGuardFn
	gitGuardFn = func(cmd, wd string, start, max, stall time.Duration, tick func(time.Duration, string)) gitGuardResult {
		cmds = append(cmds, cmd)
		if strings.Contains(cmd, "git@github.com") { // every SSH fetch hangs
			return gitGuardResult{Started: false}
		}
		return gitGuardResult{Started: true, Code: 0}
	}
	t.Cleanup(func() { gitGuardFn = old })

	if err := GitCloneOrReuse("o/r", dir); err != nil {
		t.Fatalf("a reachable HTTPS fetch must succeed after SSH fails: %v", err)
	}
	if len(cmds) != 3 {
		t.Fatalf("2 SSH attempts then HTTPS: %d commands: %v", len(cmds), cmds)
	}
	if !strings.Contains(cmds[0], "git@github.com") || !strings.Contains(cmds[1], "git@github.com") || !strings.Contains(cmds[2], "fetch origin") {
		t.Fatalf("order: %v", cmds)
	}
}

func TestFetchStaysOnSSHWhenItWorksAndOnHTTPSWithoutAKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "clone")
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	var cmds []string
	old := gitGuardFn
	gitGuardFn = func(cmd, wd string, start, max, stall time.Duration, tick func(time.Duration, string)) gitGuardResult {
		cmds = append(cmds, cmd)
		return gitGuardResult{Started: true, Code: 0}
	}
	t.Cleanup(func() { gitGuardFn = old })

	t.Setenv("EINSTEIN_GIT_SSH_KEY", filepath.Join(t.TempDir(), "no-such-key"))
	GitCloneOrReuse("o/r", dir)
	if len(cmds) != 1 || !strings.Contains(cmds[0], "fetch origin") {
		t.Fatalf("without a deploy key the behaviour is exactly what it was: %v", cmds)
	}

	cmds = nil
	key := filepath.Join(t.TempDir(), "deploy")
	os.WriteFile(key, []byte("k"), 0o600)
	t.Setenv("EINSTEIN_GIT_SSH_KEY", key)
	GitCloneOrReuse("o/r", dir)
	if len(cmds) != 1 || !strings.Contains(cmds[0], "git@github.com:o/r.git") {
		t.Fatalf("with a deploy key one SSH fetch is enough: %v", cmds)
	}
}
