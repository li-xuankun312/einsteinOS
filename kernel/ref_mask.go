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
	"sort"
	"strings"
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

// newRefMask builds the replacer for the given references ("owner/name") and label set.
func newRefMask(labels map[string]string, refs ...string) *strings.Replacer {
	type pair struct{ from, to string }
	var pairs []pair
	for _, r := range refs {
		owner, name, ok := strings.Cut(r, "/")
		if !ok || name == "" {
			continue
		}
		for _, f := range refMaskFmts {
			from := fmt.Sprintf(f.from, owner, name, strings.ToLower(name), strings.ToUpper(name))
			pairs = append(pairs, pair{from, labels[f.label]})
		}
	}
	// longest first: "github.com/o/n" before "o/n" before "n"
	sort.SliceStable(pairs, func(i, j int) bool { return len(pairs[i].from) > len(pairs[j].from) })
	var args []string
	seen := map[string]bool{}
	for _, p := range pairs {
		if p.from == "" || seen[p.from] {
			continue
		}
		seen[p.from] = true
		args = append(args, p.from, p.to)
	}
	return strings.NewReplacer(args...)
}

// maskRequirements returns req with every reference name in what a conversation is given
// (definitions to port, registered tests) replaced by identifier-safe labels. The kernel keeps
// measuring with the real paths (Sources); the port map and the integration script use the
// masked forms on both sides.
func maskRequirements(req UnitRequirements, m *strings.Replacer) UnitRequirements {
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
