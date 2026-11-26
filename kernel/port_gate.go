package kernel

// port_gate.go — a unit passes only when every definition of its reference files has a
// counterpart in the code: one or zero, no threshold.
//
// 11/04 14:31 and 14:37 units u89be81de0b and u0e60a9f272 wrote nothing, ran the existing
// build.sh, and passed — the existing code being header arithmetic around the reference's
// constants, with no counterpart of most of what include/strata/kernels/ngram.hpp or
// src/spec/controller.cpp define. Nothing measured that. The reference's definitions are now
// measured (ref_symbols.go, UnitRequirements.Symbols), the unit writes where each one went
// (PortMapPath), and the kernel checks every line: the definition must exist in the file it
// names. A missing line or a name not defined there is a failed gate.

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// portMapPathFmt: the map a unit writes, one line per reference definition:
// <reference file> TAB <reference name> TAB <file in this repo> TAB <name defined there>
const portMapPathFmt = ".einsteinOS/port/%[1]s.tsv"

// PortMapPath is where unit unitID's port map lives in the repo.
func PortMapPath(unitID string) string { return fmt.Sprintf(portMapPathFmt, unitID) }

const (
	gatePortFmt         = "port_incomplete: %[1]d of the %[2]d definitions of the unit's reference files have no counterpart:\n%[3]s"
	portNoLineFmt       = "  %[1]s %[2]s %[3]s: no line in %[4]s"
	portNoFileFmt       = "  %[1]s %[2]s → %[3]s: no such file"
	portNotDefinedFmt   = "  %[1]s %[2]s → %[3]s:%[4]s: not defined in that file"
	portKernelNoBodyFmt = "  %[1]s kernel %[2]s → %[3]s:%[4]s: only declared there — a kernel's counterpart is a __global__ kernel with a body"
	portNotKernelFmt    = "  %[1]s kernel %[2]s → %[3]s:%[4]s: defined there, but not as a __global__ kernel of a .cu/.cuh file — a host function about a kernel is not its port"
	portChecklistHead   = "[PORT — measured from %[1]s at %[2]s] the unit passes only when every definition below has a counterpart, listed in %[3]s as:\n<reference file>\\t<reference name>\\t<file in this repo>\\t<name defined there>\n"
	portChecklistLine   = "%[1]s\t%[2]s\t%[3]s\n"
	portChecklistFooter = "%[1]d definitions. the kernel reads every line and checks the name is defined in the file it names.\n"
)

// portEntry is one line of a port map.
type portEntry struct{ refFile, refName, file, name string }

func readPortMap(ws, unitID string) map[string]portEntry {
	m := map[string]portEntry{}
	b, err := os.ReadFile(filepath.Join(ws, PortMapPath(unitID)))
	if err != nil {
		return m
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(strings.TrimRight(l, "\r"), "\t")
		if len(f) < 4 {
			continue
		}
		e := portEntry{strings.TrimSpace(f[0]), strings.TrimSpace(f[1]), strings.TrimSpace(f[2]), strings.TrimSpace(f[3])}
		m[e.refFile+"\t"+e.refName] = e
	}
	return m
}

// portMissing returns one line per reference definition without a verified counterpart.
func portMissing(ws, unitID string, syms []RefSymbol) []string {
	pm := readPortMap(ws, unitID)
	defined := map[string]map[string]bool{} // file -> base names it defines
	kernels := map[string]map[string]bool{} // file -> base names of its __global__ kernels
	texts := map[string]string{}            // file -> its text
	definedIn := func(file, name string) (exists, ok bool) {
		d, seen := defined[file]
		if !seen {
			b, err := os.ReadFile(filepath.Join(ws, file))
			if err != nil {
				defined[file] = nil
				return false, false
			}
			texts[file] = string(b)
			d = map[string]bool{}
			k := map[string]bool{}
			for _, s := range SymbolsOf(file, string(b)) {
				d[symbolBase(s.Name)] = true
				if s.Kind == "kernel" {
					k[symbolBase(s.Name)] = true
				}
			}
			defined[file] = d
			kernels[file] = k
		}
		if d == nil {
			return false, false
		}
		return true, d[symbolBase(name)]
	}
	var out []string
	for _, s := range syms {
		e, ok := pm[s.File+"\t"+s.Name]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf(portNoLineFmt, s.File, s.Kind, s.Name, PortMapPath(unitID)))
		default:
			exists, def := definedIn(e.file, e.name)
			if !exists {
				out = append(out, fmt.Sprintf(portNoFileFmt, s.File, s.Name, e.file))
			} else if !def {
				out = append(out, fmt.Sprintf(portNotDefinedFmt, s.File, s.Name, e.file, e.name))
			} else if s.Kind == "kernel" && !kernels[e.file][symbolBase(e.name)] {
				out = append(out, fmt.Sprintf(portNotKernelFmt, s.File, s.Name, e.file, e.name))
			} else if s.Kind == "kernel" && !definesWithBody(stripCComments(texts[e.file]), symbolBase(e.name)) {
				// a declaration is not a kernel: 11/14 10:13 unit uaa158d17c5 wrote
				// src/prefill/kernels_api.cuh — 23 __global__ declarations, no body, "so the
				// port checker sees these __global__ kernels in a .cuh file" — and mapped the
				// reference's prefill kernels to them
				out = append(out, fmt.Sprintf(portKernelNoBodyFmt, s.File, s.Name, e.file, e.name))
			}
		}
	}
	return out
}

// PortChecklist is the list of definitions a unit ports, formatted from the measurement.
func (u UnitRequirements) PortChecklist(repo, sha, unitID string) string {
	if len(u.Symbols) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf(portChecklistHead, repo, shortSHA(sha), PortMapPath(unitID)))
	for _, s := range u.Symbols {
		b.WriteString(fmt.Sprintf(portChecklistLine, s.File, s.Name, s.Kind))
	}
	b.WriteString(fmt.Sprintf(portChecklistFooter, len(u.Symbols)))
	return b.String()
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// portKernels: the names, in the unit's port map, of counterparts that are __global__ kernels
// of the files the map names.
func portKernels(ws, unitID string) map[string]bool {
	out := map[string]bool{}
	kernels := map[string]map[string]bool{}
	for _, e := range readPortMap(ws, unitID) {
		k, seen := kernels[e.file]
		if !seen {
			k = map[string]bool{}
			if b, err := os.ReadFile(filepath.Join(ws, e.file)); err == nil {
				for _, s := range SymbolsOf(e.file, string(b)) {
					if s.Kind == "kernel" {
						k[symbolBase(s.Name)] = true
					}
				}
			}
			kernels[e.file] = k
		}
		if n := symbolBase(e.name); k[n] {
			out[n] = true
		}
	}
	return out
}

const (
	// %[1]d how many groups, %[2]s them
	portMissingGroupsFmt = " (no missing line of the planner's names this group; its %[1]d missing line(s) name these group(s): %[2]s)"
	portMissingGroupsMax = 8
	// %[1]s the file, %[2]s the rows counting for it
	portRowCountsFmt = "; column 2 is the reference's function another unit's port map ports as a kernel — rows counting for %[1]s: %[2]s"
	// %[1]s the file, %[2]s the missing prefix
	portRowCountsNoneFmt = "; column 2 is the reference's function another unit's port map ports as a kernel, and no unit's map writes a kernel into %[1]s: a row for it counts with column 1 %[2]s<group>"
	// %[1]s the reference file, %[2]s its function, %[3]s the unit, %[4]s its kernel
	portRowCountsLineFmt = "%[1]s %[2]s (%[3]s ports it as %[4]s)"
	portExtraFmt         = "  %[1]s %[2]s → %[3]s:%[4]s: the line names no definition this unit ports"
	portDuplicateFmt     = "  %[1]s %[2]s → %[3]s:%[4]s: a second line for a definition line %[5]d already maps"
)

// readPortLines: the port map's lines in order, with their line numbers.
func readPortLines(ws, unitID string) (lines []portEntry, nums []int) {
	b, err := os.ReadFile(filepath.Join(ws, PortMapPath(unitID)))
	if err != nil {
		return nil, nil
	}
	for i, l := range strings.Split(string(b), "\n") {
		f := strings.Split(strings.TrimRight(l, "\r"), "\t")
		if len(f) < 4 {
			continue
		}
		lines = append(lines, portEntry{strings.TrimSpace(f[0]), strings.TrimSpace(f[1]), strings.TrimSpace(f[2]), strings.TrimSpace(f[3])})
		nums = append(nums, i+1)
	}
	return lines, nums
}

// portExtra returns one line per port map line beyond the unit's definitions, each mapped
// once: the map is the measured definitions, no more. 11/13 15:37 unit u7663fc74b8 added a
// 98th line (router_top10.cu RT_MAX_THREADS → u776_check.cu u776_router_probe_kernel) for
// the definition its line 51 already mapped, so that a kernel written to pass
// kernel_unported counted as a port (the map, read last line first, took line 98), and
// raised the count its own test.sh checked from 97 to 98 to match.
func portExtra(ws, unitID string, syms []RefSymbol) []string {
	if len(syms) == 0 {
		return nil
	}
	own := map[string]bool{}
	for _, s := range syms {
		own[s.File+"\t"+s.Name] = true
	}
	first := map[string]int{}
	var out []string
	lines, nums := readPortLines(ws, unitID)
	for i, e := range lines {
		if plannerMissingRow(unitID, e.refFile) {
			continue // the integration unit's own kernel, checked by kernel_unported
		}
		k := e.refFile + "\t" + e.refName
		switch {
		case !own[k]:
			out = append(out, fmt.Sprintf(portExtraFmt, e.refFile, e.refName, e.file, e.name))
		case first[k] > 0:
			out = append(out, fmt.Sprintf(portDuplicateFmt, e.refFile, e.refName, e.file, e.name, first[k]))
		default:
			first[k] = nums[i]
		}
	}
	return out
}

const portForeignNameFmt = "  %[1]s %[2]s → %[3]s:%[4]s: %[4]s is not a name %[1]s defines"

// portForeignNames returns one line per port map line whose counterpart name is not a
// definition of its reference file: a port keeps the reference's names. 11/13 unit
// u7663fc74b8 named u776_router_probe_kernel, a kernel of its own that router_top10.cu does
// not define, as a counterpart.
func portForeignNames(ws, unitID string, fileNames map[string]map[string]bool) []string {
	if len(fileNames) == 0 {
		return nil
	}
	var out []string
	lines, _ := readPortLines(ws, unitID)
	for _, e := range lines {
		names, ok := fileNames[e.refFile]
		if ok && !names[symbolBase(e.name)] {
			out = append(out, fmt.Sprintf(portForeignNameFmt, e.refFile, e.refName, e.file, e.name))
		}
	}
	return out
}

// scriptsReadingPortMap: the files of the unit's verify directory (build.sh, test.sh and
// what sits beside them) that read a port map. The kernel checks the map against the
// definitions it measured from the reference; a count a script checks is a number the
// conversation writes and can change: 11/13 15:38 unit u7663fc74b8 ran
// sed -i 's/n != 97/n != 98/g' on its test.sh after adding a 98th line to its map.
func scriptsReadingPortMap(ws, unitID string) []string {
	build, _ := devVerifyScripts(unitID)
	dir := filepath.Dir(filepath.Join(ws, filepath.FromSlash(build)))
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if s := string(b); strings.Contains(s, ".einsteinOS/port") || strings.Contains(s, unitID+".tsv") {
			out = append(out, path.Join(path.Dir(build), e.Name()))
		}
	}
	return out
}

// unitPortScripts: scriptsReadingPortMap for a unit the kernel measures a port of; the
// integration unit's test.sh is the kernel's own.
func unitPortScripts(ws, unitID string, req UnitRequirements) []string {
	if len(req.Tests) > 0 || len(req.Symbols) == 0 {
		return nil
	}
	return scriptsReadingPortMap(ws, unitID)
}

const portRefFileFmt = "  %[1]s %[2]s → %[3]s:%[4]s: %[1]s is not one of the unit's reference files"

// portRefFiles returns one line per port map line whose reference file is not one of the
// unit's: that column is the reference, which only the kernel measures. 11/13 16:53 unit
// u739c93b0c3 wrote include/ref/kernels/{rope,gr,fused_gr,native_gr_norm}.hpp into the
// repository and gave them as its reference files (include/ref/ is the masked name of the
// reference, so the names look right), and 17:34 unit u41a85163f4 wrote three more; the
// port gate then checked the unit's code against headers the unit had written itself.
func portRefFiles(ws, unitID string, syms []RefSymbol) []string {
	if len(syms) == 0 {
		return nil
	}
	refFiles := map[string]bool{}
	for _, s := range syms {
		refFiles[s.File] = true
	}
	seen := map[string]bool{}
	var out []string
	lines, _ := readPortLines(ws, unitID)
	for _, e := range lines {
		if plannerMissingRow(unitID, e.refFile) {
			continue // the integration unit's own kernel, checked by kernel_unported
		}
		if !refFiles[e.refFile] && !seen[e.refFile] {
			seen[e.refFile] = true
			out = append(out, fmt.Sprintf(portRefFileFmt, e.refFile, e.refName, e.file, e.name))
		}
	}
	return out
}

// refPathsWritten: the files of changed that are at the path of one of the unit's reference
// files as the conversation is shown them (req is the masked requirements: include/ref/… for
// include/strata/…). 11/19 pb0569948de, p4fde6588a6 and p71c0367d2e each wrote their reference
// header at that path (include/ref/prefill/gemm.hpp, include/ref/program/logits_selection.hpp,
// include/ref/plan/plan.hpp); 2c49a7d8 kept such files out of the port map's reference column
// only, and pb0569948de's was verified and pushed.
func refPathsWritten(changed []string, req UnitRequirements) []string {
	ref := map[string]bool{}
	for _, f := range req.MaskedPaths {
		ref[f] = true
	}
	var out []string
	for _, f := range changed {
		if ref[f] {
			out = append(out, f)
		}
	}
	return sortedCopy(out)
}

// withRefPathsPresent: changed, and every file of the workspace ws at the path of one of the
// unit's reference files as shown — what the branch already held there is the unit's too
// (refPathRecheck reopens the unit that put it there; leaving it in place does not pass).
func withRefPathsPresent(ws string, changed []string, req UnitRequirements) []string {
	out := append([]string{}, changed...)
	seen := map[string]bool{}
	for _, f := range changed {
		seen[f] = true
	}
	for _, f := range req.MaskedPaths {
		if seen[f] {
			continue
		}
		if st, err := os.Stat(filepath.Join(ws, f)); err == nil && !st.IsDir() {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// portRowSameReason: why a row naming the same file and function on both sides does not count.
const portRowSameReason = " (the same file and function in both columns)"

// integrationMissingPrefix: the first column of an integration unit's port map row for a kernel
// it writes for a tensor group the planner's missing lines give it ("missing:<group>").
const integrationMissingPrefix = "missing:"

// integrationPortFiles: the files the integration unit's port map writes counterparts into, for
// kernel_unported — a row counts when its first column is missing:<group> for a group the
// planner's answer gives the integration unit to write, or when its reference definition is one
// another unit's port map ports as a kernel; any other row does not. 11/23 16:29:13 the gate
// held the kernels the planner's missing lines had given the unit, which have no reference
// counterpart, and at 16:43 the conversation wrote rows naming the reference's test helpers
// (gdn_parity.cpp check, shared_expert_parity.cpp s2_weight, sigmoid_f) as their counterparts.
func integrationPortFiles(ws, unitID string, pm map[string]portEntry) (map[string]bool, []string) {
	missing := map[string]bool{}
	for _, o := range integrationAnswerOf(unitID).Ops {
		if o.Unit == "" {
			missing[o.Group] = true
		}
	}
	refKernels := map[string]bool{}
	// file -> the rows that count for it: what column 2 holds, measured from the other units' maps
	countsFor := map[string][]string{}
	maps, _ := filepath.Glob(filepath.Join(ws, ".einsteinOS", "port", "*.tsv"))
	for _, m := range maps {
		u := strings.TrimSuffix(filepath.Base(m), ".tsv")
		if u == unitID {
			continue
		}
		ks := portKernels(ws, u)
		for _, e := range readPortMap(ws, u) {
			if ks[symbolBase(e.name)] {
				refKernels[e.refName] = true
				countsFor[e.file] = append(countsFor[e.file], fmt.Sprintf(portRowCountsLineFmt, e.refFile, e.refName, u, e.name))
			}
		}
	}
	groups := integrationMissingGroups(unitID)
	out := map[string]bool{}
	var refused []string
	for _, e := range pm {
		switch {
		case strings.HasPrefix(e.refFile, integrationMissingPrefix):
			if missing[strings.TrimPrefix(e.refFile, integrationMissingPrefix)] {
				out[e.file] = true
			} else {
				// which groups there are: 11/25 23:05:05 220 rows did not count, the conversation
				// having written missing:router_top10_kernel, missing:router_top10_wave and two
				// tensor names of the model as groups, and nothing named the planner's own
				refused = append(refused, e.refFile+" → "+e.file+" "+e.name+
					fmt.Sprintf(portMissingGroupsFmt, len(groups), shortList(groups, portMissingGroupsMax)))
			}
		case refKernels[e.refName]:
			out[e.file] = true
		case e.refFile == e.file && e.refName == e.name:
			// 11/25 06:27:29 165 of the 193 rows that did not count named a file and function of
			// this repository on both sides — src/core/device_main.cpp main → the same
			refused = append(refused, e.refFile+" "+e.refName+" → "+e.file+" "+e.name+portRowSameReason)
		default:
			// what column 2 is: 11/25 12:23 the embed part, told only that no unit ported its
			// test_X names as kernels, wrote each file's rows twice, launcher and kernel names,
			// "to cover two possible check formats"
			why := " (no unit ports " + e.refName + " as a kernel"
			if c := countsFor[e.file]; len(c) > 0 {
				sort.Strings(c)
				why += fmt.Sprintf(portRowCountsFmt, e.file, shortList(dedupStrings(c), 4))
			} else {
				why += fmt.Sprintf(portRowCountsNoneFmt, e.file, integrationMissingPrefix)
			}
			refused = append(refused, e.refFile+" "+e.refName+" → "+e.file+" "+e.name+why+")")
		}
	}
	sort.Strings(refused)
	return out, refused
}

// plannerMissingRow: a port map row of the integration unit whose first column is
// missing:<group> for a group the planner's missing lines give it — a kernel with no reference
// counterpart, which port_extra and port_ref_file_unknown do not judge. 11/23 20:32:26 and
// 20:43:15 both failed on the rows that kernel_unported asked for.
func plannerMissingRow(unitID, refFile string) bool {
	if !strings.HasPrefix(refFile, integrationMissingPrefix) {
		return false
	}
	g := strings.TrimPrefix(refFile, integrationMissingPrefix)
	for _, o := range integrationAnswerOf(unitID).Ops {
		if o.Unit == "" && o.Group == g {
			return true
		}
	}
	return false
}

// integrationMissingGroups: the tensor groups the planner's missing lines give the integration
// unit, sorted.
func integrationMissingGroups(unitID string) []string {
	var out []string
	for _, o := range integrationAnswerOf(unitID).Ops {
		if o.Unit == "" && o.Group != "" {
			out = append(out, o.Group)
		}
	}
	sort.Strings(out)
	return out
}
