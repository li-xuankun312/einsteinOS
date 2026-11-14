package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Measured from text: a CMake bench test with its command, a bench executable without a
// test, a runnable script under benchmarks/, and nothing from a repo without them.
func TestEvalBenchmarksAreMeasured(t *testing.T) {
	files := map[string]string{
		"CMakeLists.txt":                    "add_executable(llama-bench tools/llama-bench/llama-bench.cpp)\nadd_executable(llama-perplexity tools/perplexity/perplexity.cpp)\nadd_executable(test-backend-ops tests/test-backend-ops.cpp)\nadd_test(NAME test-perf COMMAND test-backend-ops perf -o MUL_MAT)\nadd_executable(main main.cpp)\nadd_executable(gdn_parity bench/micro/gdn_parity.cpp)\nadd_executable(ghost_bench bench/micro/ghost_bench.cpp)\n",
		"tools/llama-bench/llama-bench.cpp": "", "tools/perplexity/perplexity.cpp": "", "tests/test-backend-ops.cpp": "", "main.cpp": "", "bench/micro/gdn_parity.cpp": "",
		"benchmarks/serve.py":  "import x\nif __name__ == \"__main__\":\n    run()\n",
		"benchmarks/lib.py":    "def helper(): pass\n",
		"benchmarks/run.sh":    "#!/bin/bash\n./bench\n",
		"src/perf_counter.cpp": "int x;\n",
	}
	old := refGitFn
	defer func() { refGitFn = old }()
	refGitFn = func(dir string, args ...string) (string, error) {
		if args[0] == "ls-tree" {
			var ks []string
			for k := range files {
				ks = append(ks, k)
			}
			return strings.Join(ks, "\n"), nil
		}
		return files[strings.SplitN(args[1], ":", 2)[1]], nil
	}
	b, err := evalBenchmarks("x", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, x := range b {
		got[x.Kind+":"+x.Name] = x.Command
	}
	for _, want := range []string{"cmake_test:test-perf", "cmake_target:llama-bench", "cmake_target:llama-perplexity", "script:serve.py", "script:run.sh"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s not measured: %v", want, got)
		}
	}
	// a parity check whose source is under bench/ is not a benchmark; a bench target whose
	// source is not in the tree (Strata's bench/micro) is not one either
	for _, not := range []string{"cmake_target:main", "script:lib.py", "cmake_target:gdn_parity", "cmake_target:ghost_bench"} {
		if _, ok := got[not]; ok {
			t.Errorf("%s is not a runnable benchmark", not)
		}
	}
	if got["cmake_test:test-perf"] != "test-backend-ops perf -o MUL_MAT" {
		t.Errorf("command: %q", got["cmake_test:test-perf"])
	}
	files = map[string]string{"CMakeLists.txt": "add_executable(app main.cpp)\n"}
	if b, _ := evalBenchmarks("x", "HEAD"); len(b) != 0 {
		t.Fatalf("no benchmark: %v", b)
	}
}

// Strata's CMakeLists at 6801c77f: its bench/micro targets are not in the published tree, so
// the reference has no benchmark to run (the 11/08 run counted 20).
func TestStrataPublishedTreeHasNoBenchmark(t *testing.T) {
	b, err := os.ReadFile("testdata/strata/CMakeLists.txt")
	if err != nil {
		t.Fatal(err)
	}
	tree := map[string]bool{"CMakeLists.txt": true, "src/core/device.cu": true, "src/kernels/gdn_parity.cpp": true}
	if got := cmakeBenchmarks("CMakeLists.txt", expandCMakeForeach(string(b)), tree); len(got) != 0 {
		t.Fatalf("benchmarks measured from a tree without bench/micro: %+v", got)
	}
}

// Why the reference has no benchmark is measured from its own tree and kept in the record:
// Strata's CMakeLists (the snapshot 0f2957f9 took) declares its benchmark targets under
// bench/micro/, the published tree has no bench/micro, and the tree publishes
// bench/results/2026-09-24-final/matrix.md instead — 11/09 and 11/10 the record said only
// "benches": null.
func TestWhyTheReferenceHasNoBenchmarkIsRecorded(t *testing.T) {
	cm, err := os.ReadFile("testdata/strata/CMakeLists.txt")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	ref := filepath.Join(work, "Strata")
	put := func(f string, b []byte) {
		os.MkdirAll(filepath.Dir(filepath.Join(ref, f)), 0o755)
		os.WriteFile(filepath.Join(ref, f), b, 0o644)
	}
	put("CMakeLists.txt", cm)
	put("src/core/device.cu", []byte("\n"))
	put("bench/results/2026-09-24-final/matrix.md", []byte("### Output (tok/s)\n"))
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-qm", "c"}} {
		c := exec.Command("git", args...)
		c.Dir = ref
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	sha, _ := refGitFn(filepath.Join(ref, ".git"), "rev-parse", "HEAD")
	why := evalNoBenchReason(filepath.Join(ref, ".git"), strings.TrimSpace(sha))
	if strings.Contains(why, " 0 target(s)") || !strings.Contains(why, "target(s) its CMakeLists declare whose sources are not in the tree") ||
		!strings.Contains(why, "1 result file(s)") || !strings.Contains(why, "matrix.md") {
		t.Fatalf("why: %s", why)
	}
}
