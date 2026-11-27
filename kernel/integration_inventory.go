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
	// Redefined: functions defined, not static or inline, in more than one source (port_symbols.go)
	Redefined []string
	// Undefined: functions a header declares and a source calls that no source defines
	Undefined []string
	// Printers: the sources of any language (and the line) printing "generated:" — the token
	// ids a runner prints and the kernel measures the rate and the correctness by
	Printers []string
	// RefPrograms: each reference source whose main a port map row gives a counterpart, with that
	// counterpart and whether an e2e.sh of the tree names its executable (refPrograms)
	RefPrograms []string
}

// printerExts: the sources a "generated:" line may be printed from.
var printerExts = map[string]bool{".c": true, ".cc": true, ".cpp": true, ".cu": true, ".cuh": true, ".h": true, ".hpp": true,
	".py": true, ".sh": true, ".rs": true, ".go": true}

// printsGeneratedRe: a line that prints "generated:" (a print call, a stream or an echo with it).
var printsGeneratedRe = regexp.MustCompile(`(print|printf|puts|cout|cerr|echo|write)\b.*generated:|generated:.*(<<|\bprint)`)

// generatedPrinters: each source of the tree (its .einsteinOS left out) with a line printing
// "generated:", as "path:line". 11/23 the only one was pytools/e2e_infer.py, a forward written in
// Python, in a direction closed since 11/22 18:11, and none of the planner's eight parts was a
// program printing them.
func generatedPrinters(tree string) []string {
	var out []string
	filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(tree, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".einsteinOS", "build", "_build", "__pycache__", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !printerExts[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil || !strings.Contains(string(b), "generated:") {
			return nil
		}
		for i, l := range strings.Split(string(b), "\n") {
			if printsGeneratedRe.MatchString(l) {
				out = append(out, fmt.Sprintf("%s:%d", rel, i+1))
				break
			}
		}
		return nil
	})
	sort.Strings(out)
	return out
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
	inv := PortInventory{Named: map[string][]string{}, RefPrograms: refPrograms(tree)}
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
	sites := treeFuncSites(tree)
	inv.Redefined = redefinedFuncs(sites)
	inv.Undefined = undefinedFuncs(tree, sites)
	inv.Printers = generatedPrinters(tree)
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
		"### %[5]d of the model's %[6]d tensor groups are named in some source; these are named in none\n%[7]s\n" +
		"### %[8]d function(s) defined in more than one source (not static or inline)\n%[9]s\n" +
		"### %[10]d function(s) a header declares and a source calls that no source defines\n%[11]s\n" +
		"### %[12]d source(s), of any language, with a line printing `generated:` (the token ids the kernel measures the rate by)\n%[13]s\n"
)

func lines(l []string) string {
	if len(l) == 0 {
		return "  (none)\n"
	}
	return "  " + strings.Join(l, "\n  ") + "\n"
}

// Block: the inventory as the planner's data.
func (inv PortInventory) Block(groups int) string {
	return fmt.Sprintf(portInventoryRefProgramsFmt, len(inv.RefPrograms), lines(inv.RefPrograms)) + fmt.Sprintf(portInventoryFmt, len(inv.Programs), lines(inv.Programs), len(inv.Readers), lines(inv.Readers),
		groups-len(inv.Unnamed), groups, lines(inv.Unnamed), len(inv.Redefined), lines(inv.Redefined),
		len(inv.Undefined), lines(inv.Undefined), len(inv.Printers), lines(inv.Printers))
}

const (
	// %[1]d programs, %[2]s them
	portInventoryRefProgramsFmt = "### %[1]d program(s) of the reference with a counterpart in the port — the port map row of its main, and whether an e2e.sh of the tree names the counterpart's executable\n%[2]s\n"
	// %[1]s the reference source, %[2]s the port's, %[3]s the e2e.sh naming it
	refProgramRunFmt = "%[1]s main → %[2]s main; %[3]s names %[4]s"
	// %[1]s the reference source, %[2]s the port's, %[3]s the executable
	refProgramNotRunFmt = "%[1]s main → %[2]s main; no e2e.sh of the tree names %[3]s"
)

// refPrograms: each reference source whose main a port map of tree gives a counterpart, with it
// and whether an e2e.sh under .einsteinOS/verify names the counterpart's executable (its base
// name without extension). 11/25 the reference's src/program/generate.cpp — the program printing
// each token and the run's tok/s — had its counterpart in src/driver/generate_cli.cpp, built by
// 23 units' build.sh as generate_cli, and the integration unit's e2e.sh ran parity tests and
// pytools/e2e_infer.py and never generate_cli.
func refPrograms(tree string) []string {
	maps, _ := filepath.Glob(filepath.Join(tree, ".einsteinOS", "port", "*.tsv"))
	e2es, _ := filepath.Glob(filepath.Join(tree, ".einsteinOS", "verify", "*", "e2e.sh"))
	seen := map[string]bool{}
	var out []string
	for _, m := range maps {
		u := strings.TrimSuffix(filepath.Base(m), ".tsv")
		for _, e := range readPortMap(tree, u) {
			if e.refName != "main" || e.name != "main" || e.refFile == e.file || strings.HasPrefix(e.refFile, integrationMissingPrefix) {
				continue
			}
			k := e.refFile + "\x00" + e.file
			if seen[k] {
				continue
			}
			seen[k] = true
			exe := strings.TrimSuffix(filepath.Base(e.file), filepath.Ext(e.file))
			re := regexp.MustCompile(`(^|[^A-Za-z0-9_.-])` + regexp.QuoteMeta(exe) + `([^A-Za-z0-9_.-]|$)`)
			line := fmt.Sprintf(refProgramNotRunFmt, e.refFile, e.file, exe)
			for _, sh := range e2es {
				b, err := os.ReadFile(sh)
				if err == nil && re.Match(b) {
					rel, _ := filepath.Rel(tree, sh)
					line = fmt.Sprintf(refProgramRunFmt, e.refFile, e.file, filepath.ToSlash(rel), exe)
					break
				}
			}
			out = append(out, line+refProgramBuiltText(tree, e.file, exe)+refProgramPrintsText(tree, e.file))
		}
	}
	sort.Strings(out)
	return out
}

const (
	// %[1]s the build.sh beside an e2e.sh, %[2]s the counterpart's source
	refProgramBuiltFmt = "; %[1]s, beside an e2e.sh, names %[2]s"
	// %[1]s the counterpart's source, %[2]d the other build.sh naming it
	refProgramNotBuiltFmt = "; no build.sh beside an e2e.sh names %[1]s (%[2]d other build.sh do)"
	// %[1]s the counterpart's source, %[2]d the line
	refProgramPrintsFmt = "; %[1]s prints \"generated:\" at line %[2]d"
	// %[1]s the counterpart's source
	refProgramNoPrintFmt = "; %[1]s prints no \"generated:\" line"
)

// refProgramBuiltText: whether a build.sh under .einsteinOS/verify with an e2e.sh beside it — the
// build whose output that e2e.sh runs — names the counterpart's source (its base name) or its
// executable, and how many other build.sh do. 11/27 36 build.sh of the units named
// src/driver/generate_cli.cpp and uintegration01529aaa82b9's, beside its e2e.sh, did not.
func refProgramBuiltText(tree, src, exe string) string {
	builds, _ := filepath.Glob(filepath.Join(tree, ".einsteinOS", "verify", "*", "build.sh"))
	re := regexp.MustCompile(`(^|[^A-Za-z0-9_.-])(` + regexp.QuoteMeta(filepath.Base(src)) + `|` + regexp.QuoteMeta(exe) + `)([^A-Za-z0-9_-]|$)`)
	var beside []string
	others := 0
	for _, b := range builds {
		body, err := os.ReadFile(b)
		if err != nil || !re.Match(body) {
			continue
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(b), "e2e.sh")); err == nil {
			rel, _ := filepath.Rel(tree, b)
			beside = append(beside, filepath.ToSlash(rel))
		} else {
			others++
		}
	}
	if len(beside) == 0 {
		return fmt.Sprintf(refProgramNotBuiltFmt, src, others)
	}
	sort.Strings(beside)
	return fmt.Sprintf(refProgramBuiltFmt, strings.Join(beside, ", "), src)
}

// refProgramPrintsText: whether the counterpart's source writes "generated:", the line the kernel
// measures the rate by. 11/27 src/driver/generate_cli.cpp ran its decode loop on the synthetic
// backend (synthetic_drive) and printed tokens:, rows: and positions:, and no generated: line.
func refProgramPrintsText(tree, src string) string {
	b, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(src)))
	if err != nil {
		return ""
	}
	for i, l := range strings.Split(string(b), "\n") {
		if printsGeneratedRe.MatchString(l) && !strings.HasPrefix(strings.TrimSpace(l), "//") {
			return fmt.Sprintf(refProgramPrintsFmt, src, i+1)
		}
	}
	return fmt.Sprintf(refProgramNoPrintFmt, src)
}
