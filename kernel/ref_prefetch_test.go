package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 11/06 03:08:30 a fresh blobless clone fetched 219 blobs one `git show` at a time; one
// batched request fetches them all
func TestACommitsBlobsAreFetchedInOneRequest(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	run := func(dir string, args ...string) string {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_TERMINAL_PROMPT=0")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	os.MkdirAll(src, 0o755)
	run(src, "init", "-q")
	for i := 0; i < 20; i++ {
		os.WriteFile(filepath.Join(src, "f"+string(rune('a'+i))+".cpp"), []byte(strings.Repeat("x", i+1)), 0o644)
	}
	run(src, "add", "-A")
	run(src, "commit", "-qm", "c")
	run(src, "config", "uploadpack.allowFilter", "true")
	run(src, "config", "uploadpack.allowAnySHA1InWant", "true")
	sha := run(src, "rev-parse", "HEAD")
	bare := filepath.Join(root, "ref.git")
	run(root, "clone", "-q", "--bare", "--filter=blob:none", "file://"+src, bare)
	missing := func() int {
		out, _ := exec.Command("git", "--git-dir", bare, "rev-list", "--objects", "--missing=print", sha).Output()
		return strings.Count(string(out), "\n?")
	}
	if missing() == 0 {
		t.Skip("this git did not make a blobless clone")
	}
	prefetchCommitBlobs(bare, sha)
	if n := missing(); n != 0 {
		t.Fatalf("%d blobs still missing", n)
	}
}
