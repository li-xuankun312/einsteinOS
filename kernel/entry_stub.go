package kernel

// entry_stub.go — a test's entry a header defines inline as a constant.
//
// 11/25 05:06:01 (the tree parked as 44cc32f) include/baloon/kg_kernels.hpp:201 gained
// `inline int e2e_embed_main() { return 0; }`, while build.sh made e2e_embed_main of
// tests/k39_integration/embed_parity.cu's main by -Dmain=e2e_embed_main. e2e_all_check.cu
// includes that header: its call can be compiled into the constant 0 and embed_parity's
// gather_rows16 test never run, every compile since passing with it.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// the headers a stub is looked for in
var stubHeaderExts = map[string]bool{".h": true, ".hh": true, ".hpp": true, ".cuh": true, ".inl": true, ".inc": true}

const (
	entryStubMax = 12 // stubs given at most
	// %[1]s the header, %[2]d its line, %[3]s the definition, %[4]s the script line making the entry
	entryStubLineFmt = "%[1]s:%[2]d: %[3]s — while %[4]s makes it of a test's main"
	// %[1]d how many, %[2]s the lines
	gateEntryStubFmt = "entry_stubbed: %[1]d header definition(s) give, inline and returning a constant, an entry build.sh makes of a test's main by -Dmain=; a source including the header may be compiled to the constant and never run the test:\n%[2]s"
)

// renamedEntries: the names the build scripts' -D<macro>=<name> make of main, each with the
// first script line making it ("script:line").
func renamedEntries(ws string, scripts []string) map[string]string {
	out := map[string]string{}
	for _, d := range macroDefLines(ws, scripts, "main") {
		if v := macroValue(d.text, "main"); v != "" {
			if _, ok := out[v]; !ok {
				out[v] = fmt.Sprintf("%s:%d", d.script, d.n)
			}
		}
	}
	return out
}

// entryStubs: the headers of the workspace defining an entry the build makes of a test's main
// as an inline function whose body is a single constant return.
func entryStubs(ws, build string) []string {
	entries := renamedEntries(ws, buildScripts(ws, build))
	if len(entries) == 0 {
		return nil
	}
	var names []string
	for n := range entries {
		names = append(names, regexp.QuoteMeta(n))
	}
	sort.Strings(names)
	re := regexp.MustCompile(`\binline\s+(?:static\s+)?(?:int|bool|long)\s+(` + strings.Join(names, "|") +
		`)\s*\(\s*(?:void)?\s*\)\s*\{\s*return\s+(?:-?\d+|true|false)\s*;\s*\}`)
	var out []string
	_ = filepath.WalkDir(ws, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if e.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !stubHeaderExts[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		if st, err := e.Info(); err != nil || st.Size() > 4<<20 {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(ws, p)
		for _, m := range re.FindAllSubmatchIndex(b, -1) {
			line := 1 + strings.Count(string(b[:m[0]]), "\n")
			def := strings.Join(strings.Fields(string(b[m[0]:m[1]])), " ")
			out = append(out, fmt.Sprintf(entryStubLineFmt, filepath.ToSlash(rel), line, def, entries[string(b[m[2]:m[3]])]))
		}
		return nil
	})
	sort.Strings(out)
	if len(out) > entryStubMax {
		out = out[:entryStubMax]
	}
	return out
}
