package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The conversation records earlier units committed are removed from the dev branch; the
// rest of .einsteinOS (plan, port maps, verify scripts) stays.
func TestConvRecordsAreRemovedFromTheDevBranch(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	for _, f := range []string{".einsteinOS/convs/u1.md", ".einsteinOS/convs/u2.md", ".einsteinOS/port/u1.tsv", ".einsteinOS/verify/u1/build.sh", "src/a.cu"} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0o644)
	}
	run("init", "-q")
	run("add", "-A")
	run("commit", "-qm", "c")
	if n := devRemoveConvRecords(dir); n != 2 {
		t.Fatalf("removed %d", n)
	}
	if _, err := os.Stat(filepath.Join(dir, ".einsteinOS/convs")); !os.IsNotExist(err) {
		t.Fatal("records still on disk")
	}
	for _, keep := range []string{".einsteinOS/port/u1.tsv", ".einsteinOS/verify/u1/build.sh", "src/a.cu"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Fatalf("%s removed", keep)
		}
	}
	if n := devRemoveConvRecords(dir); n != 0 {
		t.Fatalf("second sweep removed %d", n)
	}
}
