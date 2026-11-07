package kernel

// integration.go — a reference commit is done when the reference's own tests pass on the
// real machine against the ported code, not when its units do.
//
// 11/04 the dev branch had no program outside its tests and nothing ever loaded a model or
// ran the reference's checks; every unit had passed. The reference says what "works" means:
// its CMakeLists.txt registers its tests (add_test, 40-odd for Strata's first commit, from
// kv_q8_parity to cuda_device_selftest). The kernel measures that list, and the last unit of
// every commit is the integration unit: the conversation writes the build
// (.einsteinOS/verify/<id>/build.sh, into build/), and test.sh is written BY THE KERNEL from
// the measured list — every registered test, by its name and command, must exit 0. One or
// zero. When the reference builds CUDA, the tests must also open a GPU device.

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// RefTest is one test the reference registers.
type RefTest struct {
	Name, Command string
	// Sources: the files the reference builds the test's executable from (its
	// add_executable), repo-relative — what the integration unit has to port, so a test
	// of the same name is the reference's test and not a wrapper around other checks
	Sources []string
}

var (
	cmakeAddExeRe   = regexp.MustCompile(`(?s)add_executable\s*\(\s*([A-Za-z_][\w.+-]*)\s+([^)]*)\)`)
	cmakeAddTestRe  = regexp.MustCompile(`(?s)add_test\s*\(\s*NAME\s+(\S+)\s+COMMAND\s+([^)]*)\)`)
	cmakeForeachRe  = regexp.MustCompile(`(?s)foreach\s*\(\s*(\w+)\s+IN\s+ITEMS\s+([^)]*)\)((?:[^f]|f(?:[^o]|o(?:[^r]|r(?:[^e]|e(?:[^a]|a(?:[^c]|c[^h]))))))*?)endforeach\s*\(\s*\)`)
	cmakeCudaRe     = regexp.MustCompile(`(?i)enable_language\s*\(\s*CUDA|LANGUAGES[^)]*CUDA|\.cu\b`)
	integrationIDFm = "uintegration%[1]s"
)

// IntegrationUnitID is the id of a reference commit's integration unit.
func IntegrationUnitID(sha string) string { return fmt.Sprintf(integrationIDFm, shortSHA(sha)) }

// expandCMakeForeach expands `foreach(v IN ITEMS a b) … endforeach()` blocks, innermost
// first, substituting ${v}.
func expandCMakeForeach(s string) string {
	for i := 0; i < 20; i++ {
		n := cmakeForeachRe.ReplaceAllStringFunc(s, func(blk string) string {
			m := cmakeForeachRe.FindStringSubmatch(blk)
			var out strings.Builder
			for _, item := range strings.Fields(m[2]) {
				out.WriteString(strings.ReplaceAll(m[3], "${"+m[1]+"}", item))
			}
			return out.String()
		})
		if n == s {
			return s
		}
		s = n
	}
	return s
}

// refIntegrationTests measures the tests registered by the reference's CMakeLists.txt files
// at sha, and whether it builds CUDA. A test the reference's own build only registers when
// given something it does not contain is not required (cmakeExcluded): 11/04 the 16
// native_*_parity tests sit under if(STRATA_ORACLE_SOURCE_DIR AND …) — a llama.cpp checkout
// passed in by path, empty by default — and could never pass here.
func refIntegrationTests(repo, sha string) (tests []RefTest, cuda bool) {
	tests, _, cuda = refIntegrationTestsWithExcluded(repo, sha)
	return tests, cuda
}

// refIntegrationTestsWithExcluded also returns the registered tests left out, with why.
func refIntegrationTestsWithExcluded(repo, sha string) (tests []RefTest, excluded map[string]string, cuda bool) {
	bare := refBareDir(repo)
	ls, err := refGitFn(bare, "ls-tree", "-r", "--name-only", sha)
	if err != nil {
		return nil, nil, false
	}
	tree := map[string]bool{}
	for _, f := range strings.Split(ls, "\n") {
		f = strings.TrimSpace(f)
		for p := f; p != "." && p != "/" && p != ""; p = path.Dir(p) {
			tree[p] = true
		}
	}
	excluded = map[string]string{}
	exeSources := map[string][]string{}
	seen := map[string]bool{}
	for _, f := range strings.Split(ls, "\n") {
		if !strings.HasSuffix(f, "CMakeLists.txt") || refNotSourceRe.MatchString(f) && !strings.HasPrefix(f, "CMakeLists") {
			continue
		}
		text, err := refGitFn(bare, "show", sha+":"+f)
		if err != nil {
			continue
		}
		if cmakeCudaRe.MatchString(text) {
			cuda = true
		}
		text = expandCMakeForeach(text)
		dir := path.Dir(f)
		for _, m := range cmakeAddExeRe.FindAllStringSubmatch(text, -1) {
			for _, w := range strings.Fields(m[2]) {
				if strings.Contains(w, "${") || strings.ToUpper(w) == w {
					continue // a variable, or a keyword (WIN32, EXCLUDE_FROM_ALL)
				}
				if p := path.Clean(path.Join(dir, w)); tree[p] {
					exeSources[m[1]] = append(exeSources[m[1]], p)
				}
			}
		}
		gate := cmakeExcluded(text, func(rel string) bool { return tree[path.Clean(path.Join(dir, rel))] })
		for _, m := range cmakeAddTestRe.FindAllStringSubmatchIndex(text, -1) {
			name, cmd := text[m[2]:m[3]], strings.Join(strings.Fields(text[m[4]:m[5]]), " ")
			if strings.Contains(name, "${") || seen[name] {
				continue
			}
			seen[name] = true
			if why := gate(m[0]); why != "" {
				excluded[name] = why
				continue
			}
			tests = append(tests, RefTest{Name: name, Command: cmd})
		}
	}
	for i := range tests {
		if f := strings.Fields(tests[i].Command); len(f) > 0 {
			tests[i].Sources = exeSources[path.Base(f[0])]
		}
	}
	sort.Slice(tests, func(i, j int) bool { return tests[i].Name < tests[j].Name })
	return tests, excluded, cuda
}

var (
	cmakeCondRe     = regexp.MustCompile(`(?mi)^[ \t]*(if|elseif|else|endif)[ \t]*\(([^)]*)\)`)
	cmakeExtPathRe  = regexp.MustCompile(`(?i)set\s*\(\s*(\w+)\s+""\s+CACHE\s+(PATH|FILEPATH|STRING)`)
	cmakeExistsRe   = regexp.MustCompile(`EXISTS\s+"?\$\{CMAKE_CURRENT_SOURCE_DIR\}/([^\s")]+)"?`)
	cmakeIdentRe    = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	cmakeNeedPathFm = "registered only when %[1]s (a path the build is given, empty by default) is set"
	cmakeNeedFileFm = "registered only when %[1]s exists, and the reference does not contain it"
)

// cmakeExcluded returns, for an offset in text, why a statement there is not part of the
// reference's own build ("" when it is): it sits in an if() branch that needs an external
// path (a CACHE PATH/STRING variable empty by default) or a file the reference does not
// contain. Every other condition (options) is taken as on — the port builds everything the
// reference can build.
func cmakeExcluded(text string, inTree func(rel string) bool) func(off int) string {
	ext := map[string]bool{}
	for _, m := range cmakeExtPathRe.FindAllStringSubmatch(text, -1) {
		ext[m[1]] = true
	}
	why := func(cond string) string {
		if m := cmakeExistsRe.FindStringSubmatch(cond); m != nil && !inTree(m[1]) && !strings.Contains(cond, "NOT") {
			return fmt.Sprintf(cmakeNeedFileFm, m[1])
		}
		for _, id := range cmakeIdentRe.FindAllString(cond, -1) {
			if ext[id] && !strings.Contains(cond, "NOT "+id) {
				return fmt.Sprintf(cmakeNeedPathFm, id)
			}
		}
		return ""
	}
	type frame struct{ excl, ownExcl string }
	type mark struct {
		off   int
		stack []frame
	}
	var marks []mark
	var stack []frame
	for _, m := range cmakeCondRe.FindAllStringSubmatchIndex(text, -1) {
		kw, cond := strings.ToLower(text[m[2]:m[3]]), text[m[4]:m[5]]
		switch kw {
		case "if":
			stack = append(stack, frame{ownExcl: why(cond)})
		case "elseif":
			if len(stack) > 0 {
				stack[len(stack)-1].ownExcl = why(cond)
			}
		case "else":
			if len(stack) > 0 {
				top := &stack[len(stack)-1]
				if top.ownExcl != "" {
					top.ownExcl = "" // the branch taken when the external input is absent
				} else {
					top.ownExcl = "the else-branch of a condition the port builds as on"
				}
			}
		case "endif":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
		marks = append(marks, mark{m[1], append([]frame(nil), stack...)})
	}
	return func(off int) string {
		var st []frame
		for _, mk := range marks {
			if mk.off > off {
				break
			}
			st = mk.stack
		}
		for _, f := range st {
			if f.ownExcl != "" {
				return f.ownExcl
			}
		}
		return ""
	}
}

const (
	integrationScriptHead = "#!/bin/bash\n# written by the kernel from the reference's registered tests (%[1]s at %[2]s); not editable\nB=\"${EINSTEINOS_BUILD_DIR:-$PWD/build}\"\nexport PATH=\"$B:$B/bin:$PATH\"\nfail=0\n"
	integrationScriptTest = "echo \"== %[1]s: %[2]s\"\n( cd \"$B\" && %[2]s ) || { echo \"FAIL %[1]s\"; fail=1; }\n"
	integrationScriptTail = "exit $fail\n"
	integrationListHead   = "[INTEGRATION — measured from %[1]s at %[2]s] this unit is done when every test the reference registers passes against the code on this branch, on the real machine.\nwrite %[3]s: it builds the whole project into build/ (executables named as the reference names them). %[4]s is written by the kernel and runs, from build/:\n"
	integrationListLine   = "  %[1]s: %[2]s\n"
	integrationGPULine    = "the reference builds CUDA: the tests must open a GPU device.\n"
)

// integrationScript is the test.sh the kernel writes for the integration unit.
func integrationScript(repo, sha string, tests []RefTest) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(integrationScriptHead, repo, shortSHA(sha)))
	for _, t := range tests {
		b.WriteString(fmt.Sprintf(integrationScriptTest, t.Name, t.Command))
	}
	b.WriteString(integrationScriptTail)
	return b.String()
}

// IntegrationBlock is the integration unit's requirement, formatted from the measurement.
func (u UnitRequirements) IntegrationBlock(repo, sha, unitID string) string {
	if len(u.Tests) == 0 {
		return ""
	}
	build, test := devVerifyScripts(unitID)
	var b strings.Builder
	b.WriteString(fmt.Sprintf(integrationListHead, repo, shortSHA(sha), build, test))
	for _, t := range u.Tests {
		b.WriteString(fmt.Sprintf(integrationListLine, t.Name, t.Command))
	}
	if u.RequireGPU {
		b.WriteString(integrationGPULine)
	}
	return b.String()
}

// integrationTestSymbols: the code files the required tests are built from (vendored ones
// left out) and every definition in them, measured at sha like a unit's sources.
func integrationTestSymbols(bare, sha string, tests []RefTest) (sources []string, syms []RefSymbol) {
	seen := map[string]bool{}
	for _, t := range tests {
		for _, f := range t.Sources {
			if seen[f] || refVendoredRe.MatchString(f) {
				continue
			}
			seen[f] = true
			sources = append(sources, f)
		}
	}
	sort.Strings(sources)
	for _, f := range sources {
		if text, err := refGitFn(bare, "show", sha+":"+f); err == nil {
			syms = append(syms, SymbolsOf(f, text)...)
		}
	}
	return sources, syms
}
