package kernel

// undeclared_ident.go — where a name the compiler calls undeclared is written.
//
// 11/25 22:50:31 tests/k39_integration/router_top10_parity.cu:44 failed with "use of undeclared
// identifier 'ix_router_top10_wave'", the file having put in `#include "ix_router_top10_wave.cu"`
// 67 lines before. Nothing the conversation was given said where that name is written or that a
// #if stands over it.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	undeclaredRe = regexp.MustCompile(`error: use of undeclared identifier '([A-Za-z_][A-Za-z0-9_]*)'`)
	ifOpenRe     = regexp.MustCompile(`^\s*#\s*(if|ifdef|ifndef)\b\s*(.*)$`)
	ifElseRe     = regexp.MustCompile(`^\s*#\s*(else|elif)\b\s*(.*)$`)
	ifEndRe      = regexp.MustCompile(`^\s*#\s*endif\b`)
)

const (
	undeclaredNamesMax = 6 // names given at most
	undeclaredDefsMax  = 4 // places given for one name at most
	undeclaredScanMax  = 1 << 20
	// %[1]s the places
	undeclaredNearFmt = "[WHERE THE NAMES THE COMPILER CALLS UNDECLARED ARE WRITTEN — measured from the workspace's sources]\n%[1]s"
	// %[1]s the name
	undeclaredHeadFmt = "  %[1]s:\n"
	// %[1]s the file, %[2]d the line, %[3]s the line
	undeclaredDefFmt = "    %[1]s:%[2]d: %[3]s\n"
	// %[1]s the conditions
	undeclaredUnderFmt = "      under %[1]s\n"
	// %[1]s the name
	undeclaredNoneFmt = "    no source of the workspace writes %[1]s outside a comment\n"
)

// undeclaredNames: the names the output's errors call undeclared, each once.
func undeclaredNames(out string) []string {
	var names []string
	seen := map[string]bool{}
	for _, m := range undeclaredRe.FindAllStringSubmatch(out, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	return names
}

// identDef: a place a name is written, with the #if conditions standing over it.
type identDef struct {
	file  string
	n     int
	text  string
	under []string
}

// defsOfName: the lines of the workspace's sources that write name followed by "(", outside //
// comments, each with the #if, #ifdef and #ifndef conditions open at that line.
func defsOfName(ws, name string) []identDef {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*\(`)
	var out []identDef
	_ = filepath.Walk(ws, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if fi.IsDir() {
			if fi.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if !stubHeaderExts[ext] && ext != ".cu" && ext != ".cpp" && ext != ".cc" && ext != ".c" {
			return nil
		}
		if fi.Size() > undeclaredScanMax {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		rel, _ := filepath.Rel(ws, p)
		rel = filepath.ToSlash(rel)
		var open []string
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for n := 1; sc.Scan(); n++ {
			l := sc.Text()
			if m := ifOpenRe.FindStringSubmatch(l); m != nil {
				open = append(open, strings.TrimSpace(m[1]+" "+m[2]))
				continue
			}
			if m := ifElseRe.FindStringSubmatch(l); m != nil {
				if len(open) > 0 {
					open[len(open)-1] = strings.TrimSpace(m[1] + " " + m[2])
				}
				continue
			}
			if ifEndRe.MatchString(l) {
				if len(open) > 0 {
					open = open[:len(open)-1]
				}
				continue
			}
			code := l
			if i := strings.Index(code, "//"); i >= 0 {
				code = code[:i]
			}
			if !re.MatchString(code) || strings.Contains(code, "#include") {
				continue
			}
			d := identDef{file: rel, n: n, text: strings.TrimSpace(l)}
			d.under = append(d.under, open...)
			out = append(out, d)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].under) != len(out[j].under) {
			return len(out[i].under) < len(out[j].under) // the ones no #if stands over first
		}
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		return out[i].n < out[j].n
	})
	return out
}

// undeclaredNear: for each name the compiler calls undeclared, where the workspace writes it and
// what #if stands over it.
func undeclaredNear(ws, out string) string {
	names := undeclaredNames(out)
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	for i, name := range names {
		if i == undeclaredNamesMax {
			break
		}
		b.WriteString(fmt.Sprintf(undeclaredHeadFmt, name))
		defs := defsOfName(ws, name)
		if len(defs) == 0 {
			b.WriteString(fmt.Sprintf(undeclaredNoneFmt, name))
			continue
		}
		for j, d := range defs {
			if j == undeclaredDefsMax {
				break
			}
			b.WriteString(fmt.Sprintf(undeclaredDefFmt, d.file, d.n, truncStr(d.text, 160)))
			if len(d.under) > 0 {
				b.WriteString(fmt.Sprintf(undeclaredUnderFmt, strings.Join(d.under, ", then ")))
			}
		}
	}
	return fmt.Sprintf(undeclaredNearFmt, b.String())
}
