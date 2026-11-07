package kernel

import (
	"strings"
	"testing"
)

// Measured from text: a CMake bench test with its command, a bench executable without a
// test, a runnable script under benchmarks/, and nothing from a repo without them.
func TestEvalBenchmarksAreMeasured(t *testing.T) {
	files := map[string]string{
		"CMakeLists.txt":       "add_executable(llama-bench tools/llama-bench/llama-bench.cpp)\nadd_executable(llama-perplexity tools/perplexity/perplexity.cpp)\nadd_test(NAME test-perf COMMAND test-backend-ops perf -o MUL_MAT)\nadd_executable(main main.cpp)\n",
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
	for _, not := range []string{"cmake_target:main", "script:lib.py"} {
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
