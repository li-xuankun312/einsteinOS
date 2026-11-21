package kernel

import (
	"strings"
	"testing"
)

// A README's numbers are read with the file they came from; source files are not read.
func TestEvalCluesComeFromDocumentationOnly(t *testing.T) {
	files := map[string]string{
		"README.md":      "## Performance\n| model | test | t/s |\n|---|---|---|\n| 7B Q4 | tg128 | 95.1 |\n",
		"docs/perf.md":   "decode: 120 tok/s on H100\n",
		"src/bench.cpp":  "// decode: 1 tok/s\n",
		"CMakeLists.txt": "",
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
	got := map[string]float64{}
	for _, c := range evalClues("o/r", "x", "abc") {
		got[c.File+"|"+c.Metric.Label] = c.Metric.Value
	}
	if len(got) != 2 || got["README.md|model=7b q4 tg128"] != 95.1 || got["docs/perf.md|decode"] != 120 {
		t.Fatalf("%v", got)
	}
}
