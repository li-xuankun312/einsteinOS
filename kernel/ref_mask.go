package kernel

// ref_mask.go — what a dev or review conversation sees of the reference: never its name.
//
// The dev conversation knows the dev repository and the abstract theory, not the source
// repository (dev_run.go). That used to be three hard-coded ReplaceAll calls on the task text
// only; the port checklist and the integration list added since name reference files, tests
// and executables (include/strata/…, strata-device). The mask is now a table of formats over
// the reference's owner and name, applied to everything a conversation is given from the
// reference, with two label sets: prose labels for text, identifier-safe ones for paths and
// names (a path must stay a path, an executable name must stay one).

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// refMaskFmts: what is masked, as formats over (owner, name, lower(name), upper(name)).
var refMaskFmts = []struct{ from, label string }{
	{"github.com/%[1]s/%[2]s", "url"},
	{"%[1]s/%[2]s", "repo"},
	{"%[2]s", "name"},
	{"%[3]s", "lower"},
	{"%[4]s", "upper"},
}

// refMaskText: labels for prose (the task); refMaskIdent: labels that keep a path or an
// identifier valid.
var (
	refMaskText  = map[string]string{"url": "(reference)", "repo": "(reference project)", "name": "(ref)", "lower": "(ref)", "upper": "(REF)"}
	refMaskIdent = map[string]string{"url": "reference", "repo": "reference", "name": "Ref", "lower": "ref", "upper": "REF"}
)

// refMasker replaces whole occurrences only: a name is not replaced inside a longer word
// (a repository called "r" must not turn every r into a label).
type refMasker struct {
	re   *regexp.Regexp
	repl map[string]string
}

// Replace masks s.
func (m *refMasker) Replace(s string) string {
	if m == nil || m.re == nil {
		return s
	}
	// a match consumes its boundary characters, so neighbours separated by one character
	// need another pass
	for i := 0; i < 4; i++ {
		n := m.replaceOnce(s)
		if n == s {
			return n
		}
		s = n
	}
	return s
}

func (m *refMasker) replaceOnce(s string) string {
	return m.re.ReplaceAllStringFunc(s, func(w string) string {
		lead, core, trail := "", w, ""
		if r, n := utf8.DecodeRuneInString(core); n > 0 && !isWordRune(r) {
			lead, core = core[:n], core[n:]
		}
		if r, n := utf8.DecodeLastRuneInString(core); n > 0 && !isWordRune(r) {
			trail, core = core[len(core)-n:], core[:len(core)-n]
		}
		if r, ok := m.repl[core]; ok {
			return lead + r + trail
		}
		return w
	})
}

func isWordRune(b rune) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// newRefMask builds the masker for the given references ("owner/name") and label set.
func newRefMask(labels map[string]string, refs ...string) *refMasker {
	repl := map[string]string{}
	var froms []string
	for _, r := range refs {
		owner, name, ok := strings.Cut(r, "/")
		if !ok || name == "" {
			continue
		}
		for _, f := range refMaskFmts {
			from := fmt.Sprintf(f.from, owner, name, strings.ToLower(name), strings.ToUpper(name))
			if _, seen := repl[from]; !seen && from != "" {
				repl[from] = labels[f.label]
				froms = append(froms, from)
			}
		}
	}
	if len(froms) == 0 {
		return &refMasker{}
	}
	// longest first: "github.com/o/n" before "o/n" before "n"
	sort.SliceStable(froms, func(i, j int) bool { return len(froms[i]) > len(froms[j]) })
	var alts []string
	for _, f := range froms {
		alts = append(alts, regexp.QuoteMeta(f))
	}
	re := regexp.MustCompile(`(?:^|[^A-Za-z0-9])(?:` + strings.Join(alts, "|") + `)(?:[^A-Za-z0-9]|$)`)
	return &refMasker{re: re, repl: repl}
}

// maskRequirements returns req with every reference name in what a conversation is given
// (definitions to port, registered tests) replaced by identifier-safe labels. The kernel keeps
// measuring with the real paths (Sources); the port map and the integration script use the
// masked forms on both sides.
func maskRequirements(req UnitRequirements, m *refMasker) UnitRequirements {
	if m == nil {
		return req
	}
	out := req
	out.Symbols = make([]RefSymbol, len(req.Symbols))
	for i, s := range req.Symbols {
		out.Symbols[i] = RefSymbol{File: m.Replace(s.File), Kind: s.Kind, Name: m.Replace(s.Name)}
	}
	out.Tests = make([]RefTest, len(req.Tests))
	for i, t := range req.Tests {
		out.Tests[i] = RefTest{Name: m.Replace(t.Name), Command: m.Replace(t.Command)}
	}
	out.Repo = "(reference)"
	return out
}

// maskShownPrompt passes a whole prompt for a dev or review conversation through the mask,
// whatever section the reference's name got into: 11/05 it was in [PRIOR FEEDBACK]
// ("dev[Niko1221/Strata] unit 7/35 …", 18 times) and in the review's status
// (reference_sources = include/strata/…), sections the per-field masking did not cover.
// What is left after masking is measured and logged, never sent silently.
func maskShownPrompt(repo, prompt string) string {
	out := newRefMask(refMaskIdent, repo).Replace(prompt)
	if _, name, ok := strings.Cut(repo, "/"); ok && name != "" {
		if n := strings.Count(strings.ToLower(out), strings.ToLower(name)); n > 0 {
			log.Printf(refMaskLeakFmt, n, name)
		}
	}
	return out
}

const refMaskLeakFmt = "ref_mask: %[1]d occurrence(s) of %[2]q left in a prompt after masking (a casing the table does not cover)"
