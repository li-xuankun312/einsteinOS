package kernel

import (
	"os/exec"
	"strings"
	"testing"
)

// A batched fetch that fails directly is tried again through the machine's proxy, the one it
// names for every protocol (11/11 05:48: ALL_PROXY=socks5://127.0.0.1:1080).
func TestAReferenceFetchIsTriedThroughTheMachinesProxy(t *testing.T) {
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1080")
	r := refFetchRoutes()
	if len(r) != 2 || r[0] != "" || r[1] != "socks5h://127.0.0.1:1080" {
		t.Fatalf("routes: %q", r)
	}
	if a := strings.Join(refProxyArgs(r[1]), " "); a != "-c http.proxy=socks5h://127.0.0.1:1080" {
		t.Fatalf("args: %q", a)
	}
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	if r := refFetchRoutes(); len(r) != 1 || r[0] != "" || refProxyArgs("") != nil {
		t.Fatalf("no proxy: %q", r)
	}
}

// A file whose blob is local is read without the network; a missing one is asked for on every
// route before the read fails, and the error names the last route.
func TestAReferenceFileIsAskedForOnEveryRoute(t *testing.T) {
	dir := t.TempDir()
	work := dir + "/w"
	bare := dir + "/b.git"
	gitIn := func(d string, a ...string) string {
		out, err := exec.Command("git", append([]string{"-C", d}, a...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gitIn(dir, "init", "-q", "-b", "main", work)
	writeFileAll(t, work+"/a.cu", "__global__ void k() {}\n")
	gitIn(work, "add", "-A")
	gitIn(work, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "x")
	gitIn(dir, "clone", "-q", "--bare", work, bare)
	sha := gitIn(bare, "rev-parse", "HEAD")
	refMu.Lock()
	refRepos["t/proxy"] = &RefRepoState{CloneDir: bare}
	refMu.Unlock()
	defer func() { refMu.Lock(); delete(refRepos, "t/proxy"); refMu.Unlock() }()
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:9")
	if c, err := RefFileAt("t", "proxy", sha, "a.cu"); err != nil || !strings.Contains(c, "__global__") {
		t.Fatalf("local blob: %q %v", c, err)
	}
	_, err := RefFileAt("t", "proxy", sha, "absent.cu")
	if err == nil || !strings.Contains(err.Error(), "through socks5h://127.0.0.1:9") {
		t.Fatalf("err: %v", err)
	}
}
