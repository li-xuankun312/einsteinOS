package kernel

// model_path.go — which of the model's tensor groups the text it generates is computed through,
// measured from the model's own files.
//
// 11/23 the planner's parts for the integration included visual (the vision encoder: patch_embed,
// 27 blocks, the merger) and mtp (the multi-token-prediction drafter); both were started, at
// 16:58:59 and 17:12:02, beside the parts the generated tokens need, taking the proxy's requests
// and the account's places. The model says which layers its text goes through: its config's
// num_hidden_layers is the count of one layered prefix of its tensors (model.language_model's
// 40 layers), and a tensor under another layered root (model.visual's 27 blocks, mtp's 1 layer)
// is off that path.

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// configInts: every integer value of key anywhere in the config (its nested configs too).
func configInts(c any, key string) []int {
	var out []int
	switch v := c.(type) {
	case map[string]any:
		for k, x := range v {
			if k == key {
				if f, ok := x.(float64); ok {
					out = append(out, int(f))
				}
			}
			out = append(out, configInts(x, key)...)
		}
	case []any:
		for _, x := range v {
			out = append(out, configInts(x, key)...)
		}
	}
	return out
}

// layeredRoots: each tensor prefix that is indexed ("model.language_model.layers.{i}") by its root
// ("model.language_model") and the count of its distinct first indices.
func layeredRoots(ms ModelShape) map[string]int {
	seen := map[string]map[string]bool{}
	for _, g := range ms.TensorGroups() {
		segs := strings.Split(g.Pattern, ".")
		for i, s := range segs {
			if s != "{i}" || i < 1 {
				continue
			}
			root := strings.Join(segs[:i-1], ".")
			if seen[root] == nil {
				seen[root] = map[string]bool{}
			}
			for _, ix := range g.Indices {
				first, _, _ := strings.Cut(ix, ",")
				seen[root][first] = true
			}
			break
		}
	}
	out := map[string]int{}
	for r, ix := range seen {
		out[r] = len(ix)
	}
	return out
}

// ModelPath: the text path's root and the roots off it.
type ModelPath struct {
	TextRoot string   // the layered root whose layers num_hidden_layers counts
	OffRoots []string // the other layered roots, not under the text root nor above it
}

// measureModelPath reads the path from the config and the tensors; empty when no layered root
// matches a num_hidden_layers of the config.
func measureModelPath(ms ModelShape) ModelPath {
	roots := layeredRoots(ms)
	counts := configInts(ms.Config, "num_hidden_layers")
	var p ModelPath
	best := -1
	for r, n := range roots {
		for _, c := range counts {
			if n == c && n > best {
				p.TextRoot, best = r, n
			}
		}
	}
	if p.TextRoot == "" {
		return ModelPath{}
	}
	for r := range roots {
		if r == p.TextRoot || strings.HasPrefix(r, p.TextRoot+".") || strings.HasPrefix(p.TextRoot, r+".") || r == "" {
			continue
		}
		p.OffRoots = append(p.OffRoots, r)
	}
	sort.Strings(p.OffRoots)
	return p
}

// Off: whether a tensor group is off the text path.
func (p ModelPath) Off(pattern string) bool {
	if p.TextRoot != "" && strings.HasPrefix(pattern, p.TextRoot+".") {
		return false
	}
	for _, r := range p.OffRoots {
		if strings.HasPrefix(pattern, r+".") {
			return true
		}
	}
	return false
}

// groupTokens: the names a group is written with (its segments but the index, weight and bias).
func groupTokens(pattern string) []string {
	var out []string
	for _, s := range strings.Split(pattern, ".") {
		switch s {
		case "{i}", "weight", "bias", "model", "":
			continue
		}
		if _, err := strconv.Atoi(s); err == nil {
			continue
		}
		out = append(out, s)
	}
	return out
}

// offPathParts: the parts whose text names tensors off the text path and none on it — a name
// written only in groups off the path, and no name written only in groups on it — each with the
// off-path names it uses.
func offPathParts(ms ModelShape, parts []string) (keep []string, off []string) {
	p := measureModelPath(ms)
	if len(p.OffRoots) == 0 {
		return parts, nil
	}
	on, offs := map[string]bool{}, map[string]bool{}
	for _, g := range ms.TensorGroups() {
		for _, t := range groupTokens(g.Pattern) {
			if p.Off(g.Pattern) {
				offs[t] = true
			} else {
				on[t] = true
			}
		}
	}
	names := func(text string, set, not map[string]bool) []string {
		var out []string
		for t := range set {
			if not[t] {
				continue
			}
			if regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(t) + `([^A-Za-z0-9_]|$)`).MatchString(text) {
				out = append(out, t)
			}
		}
		sort.Strings(out)
		return out
	}
	for _, part := range parts {
		offN, onN := names(part, offs, on), names(part, on, offs)
		if len(offN) > 0 && len(onN) == 0 {
			name, _, _ := strings.Cut(part, ":")
			off = append(off, fmt.Sprintf("%s (names %s, only in tensors under %s)", strings.TrimSpace(name), strings.Join(offN, " "), strings.Join(p.OffRoots, ", ")))
			continue
		}
		keep = append(keep, part)
	}
	return keep, off
}
