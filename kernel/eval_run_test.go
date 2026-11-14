package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A repository's own benchmark, run from its bare clone at a commit: checked out, its script
// run where the repository puts it, and the printed numbers read; a benchmark that fails to
// run contributes no metric.
func TestEvalRunsARepositorysOwnBenchmark(t *testing.T) {
	bootKernel() // the evaluation forks its builds through the kernel's process table
	work := t.TempDir()
	repo := filepath.Join(work, "r")
	os.MkdirAll(filepath.Join(repo, "benchmarks"), 0o755)
	os.WriteFile(filepath.Join(repo, "benchmarks/run.sh"), []byte("#!/bin/bash\necho 'decode: 42.5 tok/s'\necho 'latency: 7.25 ms'\n"), 0o755)
	os.WriteFile(filepath.Join(repo, "benchmarks/broken.sh"), []byte("#!/bin/bash\necho 'decode: 99 tok/s'\nexit 3\n"), 0o755)
	git := func(dir string, args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git(repo, "init", "-q")
	git(repo, "add", "-A")
	git(repo, "commit", "-qm", "x")
	bare := filepath.Join(work, "r.git")
	git(work, "clone", "-q", "--bare", repo, bare)

	benches, err := evalBenchmarks(bare, "HEAD")
	if err != nil || len(benches) != 2 {
		t.Fatalf("benchmarks: %v %v", benches, err)
	}
	run := evalRunRepo("o/r", bare, "HEAD", benches)
	if run.Err != "" || run.BuildExit != 0 {
		t.Fatalf("%+v", run)
	}
	got := map[string]float64{}
	for _, m := range run.Metrics() {
		got[m.Label+"|"+m.Unit] = m.Value
	}
	if len(got) != 2 || got["decode|tok/s"] != 42.5 || got["latency|ms"] != 7.25 {
		t.Fatalf("metrics of the benchmarks that exited 0: %v (%+v)", got, run.Benches)
	}
}

// A blobless clone's commit is fetched in batches before it is checked out. 11/10
// triton-lang/triton was taken and then not run: git archive fetched its blobs lazily over
// HTTPS, the TLS connection broke, and the record said "checkout: fatal: unable to access …".
func TestABloblessCloneIsPrefetchedBeforeItsCheckout(t *testing.T) {
	work := t.TempDir()
	src := filepath.Join(work, "repo")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "README.md"), []byte("published: 12 tok/s\n"), 0o644)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-qm", "c"}} {
		c := exec.Command("git", args...)
		c.Dir = src
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	bare := filepath.Join(work, "repo.git")
	if out, err := exec.Command("git", "clone", "-q", "--bare", src, bare).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	sha, _ := refGitFn(bare, "rev-parse", "HEAD")
	sha = strings.TrimSpace(sha)
	var asked []string
	old := evalPrefetchFn
	defer func() { evalPrefetchFn = old }()
	evalPrefetchFn = func(dir, s string) { asked = append(asked, dir+"@"+s) }
	run := evalRunRepo("o/repo", bare, sha, nil)
	if run.Err != "" || len(asked) != 1 || asked[0] != bare+"@"+sha {
		t.Fatalf("prefetched %v before the checkout; run %+v", asked, run)
	}
}
