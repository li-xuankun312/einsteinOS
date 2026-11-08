package kernel

import (
	"os"
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
