package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A wip branch someone else rewrote is set back to the commit the kernel pushed before the
// next attempt starts from it: 11/13 04:26:55 a dev conversation pushed a new root commit
// to einsteinOS/dev-af57b5e66f-wip-u7f1ccea730 and 105 commits after it.
func TestAWipBranchHoldsWhatTheKernelPushed(t *testing.T) {
	bare, clone := setupRemote(t)
	wip := devWipBranch("einsteinOS/dev-7", "u1")
	mirror := t.TempDir()
	write(t, mirror, "src/unit.cu", "the kernel's verified code")
	if err := commitDelta(clone, "o/r", "einsteinOS/dev-7", wip, mirror, []string{"src/unit.cu"}, nil, "unit u1"); err != nil {
		t.Fatal(err)
	}
	sha := KernelPushed("o/r", wip)
	if sha == "" || strings.TrimSpace(git(t, clone, "rev-parse", "HEAD")) != sha {
		t.Fatalf("the kernel's push is recorded: %q", sha)
	}
	// a conversation's push: a new root, nothing in common with the dev branch
	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "init", "-q", "-b", "x", other)
	write(t, other, "junk.md", "Commit 1: …")
	git(t, other, "add", "-A")
	git(t, other, "commit", "-q", "-m", "u7f1ccea730: port TSV(24), build+test scripts")
	git(t, other, "push", "-q", "-f", bare, "x:refs/heads/"+wip)

	if !keepKernelWip(clone, "o/r", wip, sha) {
		t.Fatal("the rewritten wip branch was not set back")
	}
	if got := strings.TrimSpace(git(t, bare, "rev-parse", "refs/heads/"+wip)); got != sha {
		t.Fatalf("wip at %s, the kernel pushed %s", got, sha)
	}
	if keepKernelWip(clone, "o/r", wip, sha) {
		t.Fatal("a wip branch at the kernel's commit is left alone")
	}
}

// 11/24 02:41:15 the operator's commit on top of the kernel's, made on this machine to restore
// 13 emptied files, was set back: commits on top of the kernel's push, each committed here, are
// kept; one committed elsewhere (a conversation's sandbox) is still set back
func TestTheOperatorsCommitOnTheKernelsIsKept(t *testing.T) {
	bare, clone := setupRemote(t)
	wip := devWipBranch("einsteinOS/dev-7", "u2")
	mirror := t.TempDir()
	write(t, mirror, "src/unit.cu", "x")
	if err := commitDelta(clone, "o/r", "einsteinOS/dev-7", wip, mirror, []string{"src/unit.cu"}, nil, "unit u2"); err != nil {
		t.Fatal(err)
	}
	sha := KernelPushed("o/r", wip)
	old := machineHost
	machineHost = func() string { return "cc-1c97a71e-0" }
	defer func() { machineHost = old }()
	op := filepath.Join(t.TempDir(), "op")
	git(t, filepath.Dir(op), "clone", "-q", "--branch", wip, bare, op)
	commitAs := func(email, msg string) {
		write(t, op, "src/unit.cu", msg)
		c := exec.Command("git", "commit", "-qam", msg)
		c.Dir = op
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=root", "GIT_AUTHOR_EMAIL="+email, "GIT_COMMITTER_NAME=root", "GIT_COMMITTER_EMAIL="+email)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		git(t, op, "push", "-q", "origin", "HEAD:refs/heads/"+wip)
	}
	commitAs("root@cc-1c97a71e-0.cc-1c97a71e.svc.cluster.local", "restore")
	tip := strings.TrimSpace(git(t, op, "rev-parse", "HEAD"))
	if keepKernelWip(clone, "o/r", wip, sha) {
		t.Fatal("the operator's commit was set back")
	}
	if KernelPushed("o/r", wip) != tip {
		t.Fatal("the kept commit is not what the next check compares with")
	}
	commitAs("claude@sandbox-1234", "a conversation's")
	if !keepKernelWip(clone, "o/r", wip, tip) {
		t.Fatal("a commit made elsewhere was kept")
	}
}
