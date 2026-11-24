package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitMT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// 11/23 20:27:45 git show origin/<wip>-cont:<path> > <path> left 14 files empty in the mirror,
// whose clone had no such ref: after a park the mirror has the parked commit by that name
func TestTheMirrorHasTheParkedCommitByItsRemoteName(t *testing.T) {
	clone, mirror := t.TempDir(), t.TempDir()
	gitMT(t, clone, "init", "-q")
	os.WriteFile(filepath.Join(clone, "build.sh"), []byte("nvcc a.cu\n"), 0o644)
	gitMT(t, clone, "add", "-A")
	gitMT(t, clone, "commit", "-qm", "park")
	sha := gitMT(t, clone, "rev-parse", "HEAD")
	gitMT(t, mirror, "init", "-q")
	branch := "einsteinOS/dev-c880662efd-wip-uintegration01529aaa82b9-cont"
	if err := mirrorKnowsPark(mirror, clone, branch, sha); err != nil {
		t.Fatal(err)
	}
	if got := gitMT(t, mirror, "show", "origin/"+branch+":build.sh"); got != "nvcc a.cu" {
		t.Fatalf("%q", got)
	}
}
