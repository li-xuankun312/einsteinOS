package kernel

// lean_copy.go — a dev unit's Lean is its own, not the theory's text.
//
// The theory and the dev unit are separate conversations so that the unit writes its own
// statements and proofs instead of following a text it was given. 11/13 15:22–15:23 unit
// u7663fc74b8 ran `sed -n 'X,Yp' .einsteinOS/theory.md > lean/u766/<Module>.lean` eight
// times: every one of its 123 theorems was the theory's text, proved by the theory
// conversation, and "0 sorry" measured nothing the unit did. A declaration of the unit's
// Lean whose text (comments dropped, white space collapsed) occurs in the theory is a copy.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// leanCopyMinLen: a declaration shorter than this (normalized) is too short to tell a copy
// from the same one-line definition written twice.
const leanCopyMinLen = 40

var (
	leanBlockComment = regexp.MustCompile(`(?s)/-.*?-/`)
	leanLineComment  = regexp.MustCompile(`--[^\n]*`)
	leanSpace        = regexp.MustCompile(`\s+`)
	// leanDeclStart: a line that starts a declaration (attributes and modifiers before it)
	leanDeclStart = regexp.MustCompile(`(?m)^(?:@\[[^\]]*\]\s*)?(?:(?:private|protected|noncomputable|partial|unsafe)\s+)*(theorem|lemma|def|abbrev|structure|inductive|class|instance|example)\b\s*([^\s:({\[]*)`)
)

// leanNormalize drops comments and collapses white space.
func leanNormalize(s string) string {
	s = leanBlockComment.ReplaceAllString(s, " ")
	s = leanLineComment.ReplaceAllString(s, " ")
	return strings.TrimSpace(leanSpace.ReplaceAllString(s, " "))
}

// leanDecl is one declaration of a Lean file.
type leanDecl struct{ Kind, Name, Text string }

// leanDecls splits a Lean file into its declarations, each to the next one's start.
func leanDecls(src string) []leanDecl {
	src = leanBlockComment.ReplaceAllStringFunc(src, func(c string) string { return strings.Repeat(" ", len(c)) })
	locs := leanDeclStart.FindAllStringSubmatchIndex(src, -1)
	var out []leanDecl
	for i, l := range locs {
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		out = append(out, leanDecl{Kind: src[l[2]:l[3]], Name: src[l[4]:l[5]], Text: src[l[0]:end]})
	}
	return out
}

// leanCopied returns the declarations of lean whose normalized text occurs in the theory.
func leanCopied(lean, theory string) []leanDecl {
	th := leanNormalize(theory)
	var out []leanDecl
	for _, d := range leanDecls(lean) {
		n := leanNormalize(d.Text)
		if len(n) >= leanCopyMinLen && strings.Contains(th, n) {
			out = append(out, d)
		}
	}
	return out
}

// theoryPath: where a dev branch keeps the theory (dev_store_theory).
const theoryPath = ".einsteinOS/theory.md"

// unitLeanCopied: for the unit's changed .lean files, the declarations that are the theory's
// text, as "file: kind name". No theory in the workspace measures nothing.
func unitLeanCopied(ws string, changed []string) []string {
	th, err := os.ReadFile(filepath.Join(ws, theoryPath))
	if err != nil {
		return nil
	}
	var out []string
	for _, f := range changed {
		if !strings.HasSuffix(f, ".lean") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(ws, filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		for _, d := range leanCopied(string(b), string(th)) {
			out = append(out, fmt.Sprintf("%s: %s %s", f, d.Kind, d.Name))
		}
	}
	return out
}

// linesList: one indented line each, the first n of them.
func linesList(xs []string, n int) string {
	more := ""
	if len(xs) > n {
		more = fmt.Sprintf("\n  (+%d more)", len(xs)-n)
		xs = xs[:n]
	}
	return "  " + strings.Join(xs, "\n  ") + more
}
