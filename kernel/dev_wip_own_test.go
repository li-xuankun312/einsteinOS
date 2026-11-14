package kernel

import (
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
