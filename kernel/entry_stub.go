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

var stubNameRe = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// the headers a stub is looked for in
var stubHeaderExts = map[string]bool{".h": true, ".hh": true, ".hpp": true, ".cuh": true, ".inl": true, ".inc": true}

const (
	entryStubMax = 12 // stubs given at most
	// %[1]s the header, %[2]d its line, %[3]s the definition, %[4]s the script line making the entry
	entryStubLineFmt = "%[1]s:%[2]d: %[3]s — while %[4]s makes it of a test's main"
	// %[1]d how many, %[2]s the lines
	// %[1]s the objects
	entryStubObjsFmt = "; measured with nm -C, the build's object files define it: %[1]s"
	// %[1]s the places
	entryStubSourceFmt    = "; measured from the sources, no object file of the build being in the workspace, these write it too: %[1]s"
	entryStubNoSourceText = "; measured from the sources, no object file of the build being in the workspace, nothing else of the workspace writes it"
	// what nm found when no object defines it strongly
	entryStubNoObjText = "; measured with nm -C, no object file of the build defines it but by this header"
	gateEntryStubFmt   = "entry_stubbed: %[1]d header definition(s) give, inline and returning a constant, an entry build.sh makes of a test's main by -Dmain=; a source including the header may be compiled to the constant and never run the test:\n%[2]s"
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
	if len(out) > 0 {
		// what the rename made, measured: 11/25 the header's comment said COREX clang-16 "does not
		// reliably export main() renamed via -Dmain=e2e_embed_main", and the embed conversation
		// (b10706e5) wrote it by hand on that comment alone
		defs := objectFuncDefs(ws, func(b string) bool { _, ok := entries[b]; return ok })
		if defs == nil {
			// the build writes its objects outside the workspace: 11/26 03:16:24 the entry_stubbed
			// line gave kg_kernels.hpp:202 with no word of where else e2e_embed_main is written
			for i, l := range out {
				name := stubNameRe.FindStringSubmatch(l[strings.Index(l, "inline"):])[1]
				var at []string
				for _, d := range defsOfName(ws, name) {
					if !strings.Contains(d.text, "inline") {
						at = append(at, fmt.Sprintf("%s:%d", d.file, d.n))
					}
				}
				if len(at) > 0 {
					out[i] += fmt.Sprintf(entryStubSourceFmt, shortList(at, 4))
				} else {
					out[i] += entryStubNoSourceText
				}
			}
		}
		if defs != nil {
			for i, l := range out {
				name := l[strings.Index(l, "inline"):]
				name = stubNameRe.FindStringSubmatch(name)[1]
				var strong []string
				for _, d := range defs[name] {
					parts := strings.SplitN(d, "\x00", 3)
					if len(parts) == 3 && strings.ContainsAny(parts[2], "Tt") {
						strong = append(strong, parts[1]+" ("+parts[0]+")")
					}
				}
				if len(strong) > 0 {
					sort.Strings(strong)
					out[i] += fmt.Sprintf(entryStubObjsFmt, shortList(strong, 4))
				} else {
					out[i] += entryStubNoObjText
				}
			}
		}
	}
	sort.Strings(out)
	if len(out) > entryStubMax {
		out = out[:entryStubMax]
	}
	return out
}
