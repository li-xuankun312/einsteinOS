package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A unit works from a clone of its own, made from the kernel's: a checkout in it leaves the
// kernel's clone and every other unit's as they were, and it fetches and pushes where the
// kernel's clone does.
func TestAUnitWorksFromACloneOfItsOwn(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "ref_store_baloon")
	run := func(dir string, args ...string) string {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	os.MkdirAll(main, 0o755)
	run(main, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(main, "a.txt"), []byte("a"), 0o644)
	run(main, "add", "-A")
	run(main, "commit", "-qm", "a")
	run(main, "branch", "other")
	run(main, "remote", "add", "origin", "git@github.com:zeng-bin3123/baloon.git")

	old := devRepoLocalDirHook
	devRepoLocalDirHook = func(string) (string, error) { return main, nil }
	defer func() { devRepoLocalDirHook = old }()

	a, err := devUnitCloneDir("zeng-bin3123/baloon", "u316bd6b9ec")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := devUnitCloneDir("zeng-bin3123/baloon", "u52d156705c")
	if a == b || a == main {
		t.Fatalf("%q %q %q", a, b, main)
	}
	if got := run(a, "remote", "get-url", "origin"); got != "git@github.com:zeng-bin3123/baloon.git" {
		t.Fatalf("origin of the unit's clone: %q", got)
	}
	run(a, "checkout", "-q", "other")
	if got := run(main, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("a unit's checkout moved the kernel's clone to %q", got)
	}
	if got := run(b, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("a unit's checkout moved another unit's clone to %q", got)
	}
	if again, _ := devUnitCloneDir("zeng-bin3123/baloon", "u316bd6b9ec"); again != a || run(a, "rev-parse", "--abbrev-ref", "HEAD") != "other" {
		t.Fatalf("the unit's clone was made again")
	}
}
