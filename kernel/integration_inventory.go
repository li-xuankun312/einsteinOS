package kernel

// integration_inventory.go — where the port's tree stands toward running the model, measured.
//
// 11/23 09:1x the state of the integration — which programs the tree has, whether any reads the
// model's safetensors files, which of the model's tensor groups its code names at all — was
// read by hand from the wip branch: generate_cli.cpp ran its decode loop "on the deterministic
// synthetic backend", the one source naming safetensors was a 56-line shim, the loader read the
// reference's index.txt/weights.bin, and no C/C++/CUDA source named the attention's or the
// experts' tensors. The integration's planner had been asked once, before any of it, and never
// told. These are measured from the tree the attempt starts on and given to it.

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// PortInventory: what the tree holds toward running the model.
type PortInventory struct {
	Programs []string // a source with a main: "path — its first comment line"
	Readers  []string // sources that name the model's weight files (.safetensors)
	Named    map[string][]string
	Unnamed  []string // tensor groups no source names
}

// portSourceExts: the sources a program of the port is built from.
var portSourceExts = map[string]bool{".c": true, ".cc": true, ".cpp": true, ".cu": true, ".cuh": true, ".h": true, ".hpp": true}

var mainRe = regexp.MustCompile(`(?m)^\s*int\s+main\s*\(`)

// groupKey: what a source names a tensor group by — the group's last two components before
// .weight/.bias, the numeric segments dropped ("linear_attn.in_proj_qkvz", "experts.gate_up_proj").
func groupKey(pattern string) string {
	var parts []string
	for _, p := range strings.Split(pattern, ".") {
		if p == "{i}" || p == "weight" || p == "bias" || p == "" {
			continue
		}
		parts = append(parts, p)
	}
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "." + parts[len(parts)-1]
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return pattern
}

// measurePortInventory reads the C/C++/CUDA sources of tree (its own .einsteinOS excluded).
func measurePortInventory(tree string, ms ModelShape) PortInventory {
	inv := PortInventory{Named: map[string][]string{}}
	keys := map[string][]string{} // key -> group patterns
	for _, g := range ms.TensorGroups() {
		k := groupKey(g.Pattern)
		if !hasString(keys[k], g.Pattern) {
			keys[k] = append(keys[k], g.Pattern)
		}
	}
	filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(tree, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" || rel == ".einsteinOS" {
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
		s := string(b)
		if mainRe.MatchString(s) {
			inv.Programs = append(inv.Programs, rel+" — "+firstComment(s))
		}
		if strings.Contains(s, ".safetensors") {
			inv.Readers = append(inv.Readers, rel)
		}
		for k, pats := range keys {
			if strings.Contains(s, k) {
				for _, pat := range pats {
					if !hasString(inv.Named[pat], rel) {
						inv.Named[pat] = append(inv.Named[pat], rel)
					}
				}
			}
		}
		return nil
	})
	for _, g := range ms.TensorGroups() {
		if len(inv.Named[g.Pattern]) == 0 && !hasString(inv.Unnamed, g.Pattern) {
			inv.Unnamed = append(inv.Unnamed, g.Pattern)
		}
	}
	sort.Strings(inv.Programs)
	sort.Strings(inv.Readers)
	sort.Strings(inv.Unnamed)
	return inv
}

// firstComment: a source's first // comment line, at most 160 bytes.
func firstComment(s string) string {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(l, "//") {
			return truncStr(strings.TrimSpace(strings.TrimLeft(l, "/")), 160)
		}
	}
	return "(no comment)"
}

const (
	// %[1]d programs, %[2]s them, %[3]d readers, %[4]s them, %[5]d named, %[6]d groups, %[7]s unnamed
	portInventoryFmt = "## the port's tree as the attempt starts on it (measured: its C/C++/CUDA sources)\n\n" +
		"### %[1]d program(s) — a source with int main, and its first comment line\n%[2]s\n" +
		"### %[3]d source(s) naming .safetensors\n%[4]s\n" +
		"### %[5]d of the model's %[6]d tensor groups are named in some source; these are named in none\n%[7]s\n"
)

func lines(l []string) string {
	if len(l) == 0 {
		return "  (none)\n"
	}
	return "  " + strings.Join(l, "\n  ") + "\n"
}

// Block: the inventory as the planner's data.
func (inv PortInventory) Block(groups int) string {
	return fmt.Sprintf(portInventoryFmt, len(inv.Programs), lines(inv.Programs), len(inv.Readers), lines(inv.Readers),
		groups-len(inv.Unnamed), groups, lines(inv.Unnamed))
}
