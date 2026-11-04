package kernel

// ref_symbols.go — what a source file defines, measured from its text.
//
// 11/04 the dev branch held 77 headers of index arithmetic (752 inline functions) and no
// counterpart of any of the reference's 60 .cpp files, and every unit had passed: nothing
// measured what the reference code contains against what was written. This file measures
// it — the top-level definitions of a C/C++/CUDA or Python file (types, functions, kernels,
// constants, macros) — so a unit's port can be checked symbol by symbol (port_gate.go).

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// RefSymbol is one top-level definition of a file.
type RefSymbol struct {
	File string // repo-relative path
	Kind string // type, func, const, macro, alias
	Name string // as written: "PleTable", "ngram_rows", "Engine::step"
}

var (
	symTypeRe   = regexp.MustCompile(`^(?:template\s*<[^{;]*>\s*)?(?:struct|class|union|enum(?:\s+class|\s+struct)?)\s+(?:\[\[[^\]]*\]\]\s*)?(?:alignas\s*\([^)]*\)\s*)?([A-Za-z_]\w*)\s*(?:final\s*)?(?:[:{]|$)`)
	symAliasRe  = regexp.MustCompile(`^(?:template\s*<[^{;]*>\s*)?using\s+([A-Za-z_]\w*)\s*=`)
	symTypedef  = regexp.MustCompile(`^typedef\b.*?([A-Za-z_]\w*)\s*(?:\[[^\]]*\])?\s*$`)
	symConstRe  = regexp.MustCompile(`\b(?:constexpr|const)\b[^=(]*?\b([A-Za-z_]\w*)\s*(?:\[[^\]]*\])?\s*(?:=|\{)`)
	symFuncRe   = regexp.MustCompile(`([A-Za-z_][\w:~]*)\s*\($`)
	symVarRe    = regexp.MustCompile(`([A-Za-z_]\w*)\s*(?:\[[^\]]*\])?\s*$`)
	symMacroRe  = regexp.MustCompile(`(?m)^[ \t]*#[ \t]*define[ \t]+([A-Za-z_]\w*)`)
	symPyRe     = regexp.MustCompile(`(?m)^(def|class|async def)\s+([A-Za-z_]\w*)`)
	symKeywords = map[string]bool{"if": true, "for": true, "while": true, "switch": true, "return": true, "sizeof": true,
		"static_assert": true, "decltype": true, "alignof": true, "catch": true, "defined": true, "operator": true}
	symCExts = map[string]bool{".h": true, ".hpp": true, ".hh": true, ".cuh": true, ".c": true, ".cc": true,
		".cpp": true, ".cxx": true, ".cu": true, ".inc": true}
)

// SymbolsOf measures the top-level definitions of one file's text.
func SymbolsOf(file, text string) []RefSymbol {
	ext := strings.ToLower(path.Ext(file))
	var out []RefSymbol
	seen := map[string]bool{}
	add := func(kind, name string) {
		if name == "" || symKeywords[name] || seen[kind+" "+name] {
			return
		}
		seen[kind+" "+name] = true
		out = append(out, RefSymbol{File: file, Kind: kind, Name: name})
	}
	switch {
	case ext == ".py":
		for _, m := range symPyRe.FindAllStringSubmatch(text, -1) {
			add(map[bool]string{true: "type", false: "func"}[m[1] == "class"], m[2])
		}
	case symCExts[ext]:
		for _, m := range symMacroRe.FindAllStringSubmatch(text, -1) {
			if !strings.HasSuffix(m[1], "_H") && !strings.HasSuffix(m[1], "_HPP") && !strings.HasSuffix(m[1], "_H_") {
				add("macro", m[1])
			}
		}
		for _, st := range topLevelStatements(stripCComments(text)) {
			switch {
			case symTypeRe.MatchString(st.head):
				add("type", symTypeRe.FindStringSubmatch(st.head)[1])
			case symAliasRe.MatchString(st.head):
				add("alias", symAliasRe.FindStringSubmatch(st.head)[1])
			case strings.HasPrefix(st.head, "typedef") && !st.body:
				if m := symTypedef.FindStringSubmatch(st.head); m != nil {
					add("alias", m[1])
				}
			case strings.Contains(st.head, "=") && (!strings.Contains(st.head, "(") || strings.Index(st.head, "=") < strings.Index(st.head, "(")):
				// a variable with an initializer: the identifier right before the "="
				if m := symVarRe.FindStringSubmatch(st.head[:strings.Index(st.head, "=")]); m != nil {
					add("const", m[1])
				}
			case strings.Contains(st.head, "("):
				// a function: the identifier right before the first "(" of the head
				if m := symFuncRe.FindStringSubmatch(st.head[:strings.Index(st.head, "(")+1]); m != nil {
					add("func", strings.TrimPrefix(m[1], "::"))
				}
			default:
				if c := symConstRe.FindStringSubmatch(st.head); c != nil {
					add("const", c[1])
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

type cStatement struct {
	head string // the statement up to its "{" or ";", whitespace collapsed
	body bool   // ended in "{" (a definition with a body)
}

// topLevelStatements splits C/C++ text into the statements outside any function, class or
// initializer body. Namespace and extern "C" blocks are transparent. Preprocessor lines are
// dropped.
func topLevelStatements(text string) []cStatement {
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		lines = append(lines, l)
	}
	src := strings.Join(lines, "\n")
	var out []cStatement
	var cur strings.Builder
	var stack []bool // true = transparent (namespace / extern "C")
	depthOpaque := 0
	for i := 0; i < len(src); i++ {
		c := src[i]
		if depthOpaque > 0 {
			switch c {
			case '{':
				depthOpaque++
			case '}':
				depthOpaque--
			}
			continue
		}
		switch c {
		case '{':
			head := strings.Join(strings.Fields(cur.String()), " ")
			cur.Reset()
			if strings.HasPrefix(head, "namespace") || strings.HasPrefix(head, `extern "C"`) {
				stack = append(stack, true)
				continue
			}
			out = append(out, cStatement{head: head, body: true})
			depthOpaque = 1
		case '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			cur.Reset()
		case ';':
			head := strings.Join(strings.Fields(cur.String()), " ")
			cur.Reset()
			if head != "" {
				out = append(out, cStatement{head: head})
			}
		default:
			cur.WriteByte(c)
		}
	}
	return out
}

var (
	cBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	cLineComment  = regexp.MustCompile(`//[^\n]*`)
	cString       = regexp.MustCompile(`"(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*'`)
)

// stripCComments removes comments and the contents of string/char literals (so a brace or
// semicolon inside them does not count), keeping extern "C" recognisable.
func stripCComments(s string) string {
	s = cBlockComment.ReplaceAllString(s, " ")
	s = cString.ReplaceAllStringFunc(s, func(lit string) string {
		if lit == `"C"` || lit == `"C++"` {
			return lit
		}
		return `""`
	})
	return cLineComment.ReplaceAllString(s, "")
}

// symbolBase: the name without its class qualifier ("Engine::step" → "step").
func symbolBase(name string) string {
	if i := strings.LastIndex(name, "::"); i >= 0 {
		return name[i+2:]
	}
	return name
}
