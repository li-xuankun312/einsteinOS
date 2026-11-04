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
	"regexp"
	"sort"
	"strings"
)

// RefTest is one test the reference registers.
type RefTest struct{ Name, Command string }

var (
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
// at sha, and whether it builds CUDA.
func refIntegrationTests(repo, sha string) (tests []RefTest, cuda bool) {
	bare := refBareDir(repo)
	ls, err := refGitFn(bare, "ls-tree", "-r", "--name-only", sha)
	if err != nil {
		return nil, false
	}
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
		for _, m := range cmakeAddTestRe.FindAllStringSubmatch(expandCMakeForeach(text), -1) {
			name, cmd := m[1], strings.Join(strings.Fields(m[2]), " ")
			if strings.Contains(name, "${") || seen[name] {
				continue
			}
			seen[name] = true
			tests = append(tests, RefTest{Name: name, Command: cmd})
		}
	}
	sort.Slice(tests, func(i, j int) bool { return tests[i].Name < tests[j].Name })
	return tests, cuda
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
