package kernel

// port_symbols.go — the functions the port's C/C++/CUDA sources define, measured.
//
// 11/23 13:32 and 16:11 the integration's tree did not compile: kg_kernels.hpp declared the GDN
// functions three times and gdn_layer.cu and gdn_qwen.cu each defined them, the parts having
// each written their own; three conversations then each deleted one of the two definitions,
// and by 16:11 no source of the tree defined them at all. Which function a tree defines twice is
// read from its files, not asked of a conversation.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// symbolSourceExts: the sources a definition counted twice comes from (a header's definitions
// are inline by the language's rules and are not counted).
var symbolSourceExts = map[string]bool{".c": true, ".cc": true, ".cpp": true, ".cu": true}

var (
	nsOpenRe = regexp.MustCompile(`^\s*namespace\s+([A-Za-z_]\w*)\s*\{`)
	// a function's head at namespace scope: return type and name, its parameters open
	funcHeadRe = regexp.MustCompile(`^((?:[A-Za-z_][\w:<>,\*&]*\s+|[\*&]+\s*)+)([A-Za-z_]\w*)\s*\(`)
	// what a head's leading words must not be (a statement, a type's definition, a macro)
	notFuncHead = map[string]bool{"return": true, "if": true, "for": true, "while": true, "switch": true, "else": true,
		"typedef": true, "using": true, "class": true, "struct": true, "enum": true, "union": true, "template": true,
		"case": true, "do": true, "goto": true, "sizeof": true, "new": true, "delete": true, "throw": true, "#define": true}
)

// funcSite: a function the source defines or declares at namespace scope.
type funcSite struct {
	Name   string // qualified by the namespaces it is in ("kg::gdn_bf16_conv_step")
	File   string
	Def    bool // a body follows its parameters
	Static bool // static or inline: one per translation unit
}

// scanFuncSites reads the functions defined or declared at namespace scope of one source.
func scanFuncSites(rel, src string) []funcSite {
	var out []funcSite
	type ns struct {
		name  string
		depth int
	}
	var stack []ns
	depth := 0
	ls := strings.Split(src, "\n")
	for i := 0; i < len(ls); i++ {
		line := stripLineComment(ls[i])
		trim := strings.TrimSpace(line)
		if m := nsOpenRe.FindStringSubmatch(line); m != nil {
			stack = append(stack, ns{m[1], depth + 1})
		} else if depth == len(stack) && !strings.HasPrefix(trim, "#") {
			if m := funcHeadRe.FindStringSubmatch(trim); m != nil {
				words := strings.Fields(m[1])
				if len(words) > 0 && !notFuncHead[words[0]] && !strings.Contains(m[1], "=") {
					// the parameters may run over lines: read to their close, then a body or a ';'
					head := trim
					j := i
					for strings.Count(head, "(") > strings.Count(head, ")") && j+1 < len(ls) && j-i < 12 {
						j++
						head += " " + strings.TrimSpace(stripLineComment(ls[j]))
					}
					rest := head[strings.LastIndex(head, ")")+1:]
					k := j
					for strings.TrimSpace(rest) == "" && k+1 < len(ls) && k-j < 2 {
						k++
						rest = strings.TrimSpace(stripLineComment(ls[k]))
					}
					rest = strings.TrimSpace(rest)
					isDef := strings.HasPrefix(rest, "{") || strings.HasPrefix(rest, "const {") || strings.HasPrefix(rest, "noexcept {")
					isDecl := strings.HasPrefix(rest, ";") || strings.HasPrefix(rest, "const;") || strings.HasPrefix(rest, "noexcept;")
					if isDef || isDecl {
						q := m[2]
						for n := len(stack) - 1; n >= 0; n-- {
							q = stack[n].name + "::" + q
						}
						static := false
						for _, w := range words {
							if w == "static" || w == "inline" || w == "__forceinline__" || strings.HasPrefix(w, "template") {
								static = true
							}
						}
						out = append(out, funcSite{Name: q, File: rel, Def: isDef, Static: static})
					}
				}
			}
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		for len(stack) > 0 && depth < stack[len(stack)-1].depth {
			stack = stack[:len(stack)-1]
		}
	}
	return out
}

// stripLineComment drops a // comment and the text of string literals that could hold braces.
func stripLineComment(l string) string {
	if i := strings.Index(l, "//"); i >= 0 {
		l = l[:i]
	}
	return stringLitRe.ReplaceAllString(l, `""`)
}

var stringLitRe = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)'`)

// treeFuncSites: every function site of the tree's C/C++/CUDA sources (.git, .einsteinOS and
// build directories left out).
func treeFuncSites(tree string) []funcSite {
	var all []funcSite
	filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(tree, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".einsteinOS", "build", "_build":
				return filepath.SkipDir
			}
			return nil
		}
		if !portSourceExts[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		all = append(all, scanFuncSites(rel, string(b))...)
		return nil
	})
	return all
}

// redefinedFuncs: the functions defined, not static or inline, in more than one source file —
// "name: a.cu, b.cu" — which no build of the tree links.
func redefinedFuncs(sites []funcSite) []string {
	files := map[string][]string{}
	for _, s := range sites {
		if !s.Def || s.Static || !symbolSourceExts[strings.ToLower(filepath.Ext(s.File))] {
			continue
		}
		if !hasString(files[s.Name], s.File) {
			files[s.Name] = append(files[s.Name], s.File)
		}
	}
	var out []string
	for n, fs := range files {
		if len(fs) > 1 {
			sort.Strings(fs)
			out = append(out, n+": "+strings.Join(fs, ", "))
		}
	}
	sort.Strings(out)
	return out
}
