package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
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
