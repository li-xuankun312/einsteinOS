package kernel

// test_mutates_missing.go — a unit's own test must not mutate a file the kernel does not have.
//
// A test that replaces a line of a file and demands the test catch the replacement is only a
// test while the line is there. When the file is not among the unit's changed files, the
// verification tree does not hold it, the replacement finds nothing, and the test fails for a
// reason the conversation cannot see in its own workspace, where the file does exist.
// 11/15 unit p17652417b0's test.sh mutated pytools/locate.py ("if (c / \"gguf\" /
// \"__init__.py\").is_file():" → "if c.is_dir():"); locate.py was not a file the unit wrote,
// so the tree lacked it, the mutation printed "pattern not found" and the test exited 1.
// 17:47–19:53 the conversation read that as the kernel's checkout being stale, committed
// deliberately wrong code to "align" with it three times, and installed a git hook.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const gateTestMutatesMissingFmt = "test_mutates_missing: %[1]s mutates %[2]s, which is not among the %[3]d file(s) the kernel has from this unit, so the verification tree does not hold it and the mutation finds nothing (%[4]s). Mutate the files this unit wrote, or add that file to the unit."

// mutatePathRe: a path named in a mutation line of a verify script — a quoted or bare path
// with a source extension, as the scripts write them ("mutant gguf_py  locate.py 'old' 'new'",
// sed -i "s/…/…/" pytools/locate.py, and the python loops that take (file, old, new) triples.
var mutatePathRe = regexp.MustCompile(`[-\w./]+\.(py|cpp|cc|cu|cuh|hpp|h|inc|lean|sh)`)

// testMutatesMissing: the files a unit's verify scripts mutate that the unit did not write.
// shown says which of the unit's files the kernel has.
func testMutatesMissing(ws, unitID string, changed []string) (script string, missing []string) {
	have := map[string]bool{}
	for _, f := range changed {
		have[f] = true
		have[filepath.Base(f)] = true
	}
	build, test := devVerifyScripts(unitID)
	for _, s := range []string{test, build} {
		b, err := os.ReadFile(filepath.Join(ws, filepath.FromSlash(s)))
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		var miss []string
		for _, line := range strings.Split(string(b), "\n") {
			if !mutationLine(line) {
				continue
			}
			for _, p := range mutatePathRe.FindAllString(line, -1) {
				p = strings.TrimPrefix(p, "./")
				if have[p] || have[filepath.Base(p)] || seen[p] || isVerifyPath(p, unitID) {
					continue
				}
				if inWorkspace(ws, p) && !fileUnderUnit(p, changed) {
					seen[p] = true
					miss = append(miss, p)
				}
			}
		}
		if len(miss) > 0 {
			sort.Strings(miss)
			return s, miss
		}
	}
	return "", nil
}

// inWorkspace: the workspace holds this path, or a file of that name somewhere under it (the
// scripts name a file by its base, "locate.py", and run from the directory holding it).
func inWorkspace(ws, p string) bool {
	if _, err := os.Stat(filepath.Join(ws, filepath.FromSlash(p))); err == nil {
		return true
	}
	if strings.Contains(p, "/") {
		return false
	}
	found := false
	filepath.WalkDir(ws, func(q string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() && devSkipDirs[filepath.Base(q)] {
			return filepath.SkipDir
		}
		if !d.IsDir() && filepath.Base(q) == p {
			found = true
		}
		return nil
	})
	return found
}

// mutationLine: the line describes a mutation — it names a mutant, or edits a file in place.
func mutationLine(line string) bool {
	l := strings.ToLower(line)
	return strings.Contains(l, "mutant") || strings.Contains(l, "mutate") ||
		strings.Contains(l, "sed -i") || strings.Contains(l, "replace(")
}

// isVerifyPath: the unit's own build.sh or test.sh.
func isVerifyPath(p, unitID string) bool {
	build, test := devVerifyScripts(unitID)
	return p == build || p == test || strings.HasSuffix(build, p) || strings.HasSuffix(test, p)
}

// fileUnderUnit: a changed file ends in p (the script may name it by a shorter path).
func fileUnderUnit(p string, changed []string) bool {
	for _, f := range changed {
		if strings.HasSuffix(f, "/"+p) || f == p {
			return true
		}
	}
	return false
}

// testMutatesMissingText: the gate's text.
func testMutatesMissingText(script string, missing, changed []string) string {
	return fmt.Sprintf(gateTestMutatesMissingFmt, script, strings.Join(missing, ", "), len(changed), shortList(changed, 8))
}
