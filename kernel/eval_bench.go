package kernel

// eval_bench.go — what in a repository is benchmark code the kernel can run, measured from
// its text: a target its CMake build declares (add_test, or add_executable) whose name or
// sources say bench/perf/perplexity, or a script under a benchmark directory that can run on
// its own (a shebang, or Python with a __main__ block). A repository with none of these has
// numbers the kernel cannot reproduce, and is not taken for evaluation.

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// EvalBench is one runnable benchmark of a repository.
type EvalBench struct {
	File    string   // where it is declared (CMakeLists.txt, or the script itself)
	Kind    string   // cmake_test, cmake_target, script
	Name    string   // target / test / script name
	Command string   // for cmake_test: the COMMAND of its add_test
	Sources []string // for CMake targets: the files the repository builds it from
}

var (
	evalBenchNameRe = regexp.MustCompile(`(?i)bench|perf|perplexity`)
	evalBenchDirRe  = regexp.MustCompile(`(?i)(^|/)(bench\w*|benchmarks?|perf)/`)
	evalPyMainRe    = regexp.MustCompile(`(?m)^if\s+__name__\s*==\s*['"]__main__['"]`)
)

// evalBenchmarks measures the benchmarks of the repository at sha (bare: its git dir).
func evalBenchmarks(bare, sha string) ([]EvalBench, error) {
	ls, err := refGitFn(bare, "ls-tree", "-r", "--name-only", sha)
	if err != nil {
		return nil, err
	}
	tree := map[string]bool{}
	for _, f := range strings.Split(ls, "\n") {
		tree[strings.TrimSpace(f)] = true
	}
	var out []EvalBench
	for _, f := range strings.Split(ls, "\n") {
		f = strings.TrimSpace(f)
		switch {
		case path.Base(f) == "CMakeLists.txt":
			text, err := refGitFn(bare, "show", sha+":"+f)
			if err == nil {
				out = append(out, cmakeBenchmarks(f, expandCMakeForeach(text), tree)...)
			}
		case evalBenchDirRe.MatchString(f) && (strings.HasSuffix(f, ".sh") || strings.HasSuffix(f, ".py")):
			text, err := refGitFn(bare, "show", sha+":"+f)
			if err == nil && (strings.HasPrefix(text, "#!") || strings.HasSuffix(f, ".py") && evalPyMainRe.MatchString(text)) {
				out = append(out, EvalBench{File: f, Kind: "script", Name: path.Base(f)})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File+out[i].Name < out[j].File+out[j].Name })
	return out, nil
}

// cmakeBenchmarks: the add_test and add_executable entries of one CMakeLists.txt that are
// benchmarks by their target name or test command — not by where their sources sit — and
// whose sources are all in the tree. 11/08 Strata's CMakeLists declares 20 targets under
// bench/micro/ (19 of them parity checks) inside an if() on bench/micro existing, which the
// published source does not contain; matching on the source path counted all 20 as
// "benchmarks of the reference to run here".
func cmakeBenchmarks(file, text string, tree map[string]bool) []EvalBench {
	var out []EvalBench
	exeSrc := map[string][]string{}
	for _, m := range cmakeAddExeRe.FindAllStringSubmatch(text, -1) {
		exeSrc[m[1]] = cmakeSources(file, m[2])
	}
	built := func(exe string) bool {
		srcs, ok := exeSrc[exe]
		if !ok || len(srcs) == 0 {
			return false
		}
		for _, f := range srcs {
			if !tree[f] {
				return false
			}
		}
		return true
	}
	tested := map[string]bool{}
	for _, m := range cmakeAddTestRe.FindAllStringSubmatch(text, -1) {
		cmd := strings.Join(strings.Fields(m[2]), " ")
		f := strings.Fields(cmd)
		if len(f) == 0 || !built(path.Base(f[0])) {
			continue
		}
		if evalBenchNameRe.MatchString(m[1]) || evalBenchNameRe.MatchString(cmd) {
			out = append(out, EvalBench{File: file, Kind: "cmake_test", Name: m[1], Command: cmd, Sources: exeSrc[path.Base(f[0])]})
			tested[path.Base(f[0])] = true
		}
	}
	for exe, srcs := range exeSrc {
		if tested[exe] || !built(exe) || !evalBenchNameRe.MatchString(exe) {
			continue
		}
		out = append(out, EvalBench{File: file, Kind: "cmake_target", Name: exe, Sources: srcs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// evalAcceptRepo: the clone check eval_discover applies — the repository's default branch
// holds at least one benchmark the kernel can run.
func evalAcceptRepo(repo string) (bool, string) {
	b, err := evalBenchmarks(evalBareDir(repo), "HEAD")
	if err != nil {
		return false, "its tree could not be read: " + err.Error()
	}
	if len(b) == 0 {
		return false, "no benchmark the kernel can run (no bench/perf/perplexity CMake target or test, no runnable script under a benchmark directory)"
	}
	var names []string
	for _, x := range b {
		names = append(names, x.Kind+":"+x.Name)
	}
	return true, fmt.Sprintf("%d runnable benchmark(s): %s", len(b), shortList(names, 8))
}

// cmakeSources: the source files an add_executable lists, relative to the repository.
func cmakeSources(cmakeFile, list string) []string {
	var out []string
	for _, w := range strings.Fields(list) {
		if strings.Contains(w, "${") || strings.ToUpper(w) == w {
			continue
		}
		out = append(out, path.Clean(path.Join(path.Dir(cmakeFile), w)))
	}
	return out
}
