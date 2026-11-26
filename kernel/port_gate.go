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
	"io/fs"
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
	// %[1]s the unit, %[2]s the file it writes, %[3]s the name there
	portRefPortedFmt = " (%[1]s's map does port it, into %[2]s %[3]s, which defines no __global__ kernel)"
	// %[1]d how many groups, %[2]s them
	// the groups themselves are given once, after the rows (gateKernelUnportedGroupsFmt): 11/26
	// 09:49:16 each of 168 refused rows carried the same list, the gate text came to 14355 bytes
	// and the conversation was shown 8000 of them
	portMissingOffFmt    = " (the planner's missing lines give this group, and it is off the model's text path, where no part is started)"
	portMissingGroupsFmt = " (no missing line of the planner's names this group; its %[1]d missing line(s) name the groups given after the rows)"
	// %[1]s the file, %[2]s the rows counting for it
	portRowCountsFmt = "; column 2 is the reference's function another unit's port map ports as a kernel — rows counting for %[1]s: %[2]s"
	// %[1]s the file, %[2]s the missing prefix
	portRowCountsNoneFmt = "; column 2 is the reference's function another unit's port map ports as a kernel, and no unit's map writes a kernel into %[1]s: a row for it counts with column 1 %[2]s<group>, where <group> is one of the %[3]d the planner's missing lines name, given after the rows"
	// %[1]s the file, %[2]d the planner's missing groups
	portRowCountsNoPathFmt = "; column 2 is the reference's function another unit's port map ports as a kernel, and no unit's map writes a kernel into %[1]s: none of the %[2]d groups the planner's missing lines name is on the model's text path, and a row for it counts with column 2 one of the reference kernels the units ported, given after the rows"
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
	ported := integrationPortedRefs(ws, unitID)
	names := integrationPortedNames(ws, unitID)
	own := map[string]bool{}
	for _, s := range syms {
		own[s.File+"\t"+s.Name] = true
	}
	refFiles := map[string]bool{}
	for _, s := range syms {
		refFiles[s.File] = true
	}
	first := map[string]int{}
	var out []string
	lines, nums := readPortLines(ws, unitID)
	for i, e := range lines {
		if plannerMissingRow(unitID, e.refFile) {
			continue // the integration unit's own kernel, checked by kernel_unported
		}
		if _, ok := ported[e.refFile+"\t"+e.refName]; ok {
			continue // a reference kernel another unit ported, the row kernel_unported asks for
		}
		if !refFiles[e.refFile] && !strings.HasPrefix(e.refFile, integrationMissingPrefix) {
			continue // port_ref_file_unknown names it, with what column 1 is
		}
		k := e.refFile + "\t" + e.refName
		switch {
		case !own[k]:
			out = append(out, fmt.Sprintf(portExtraFmt, e.refFile, e.refName, e.file, e.name)+portedElsewhere(ported, e.refName)+portedAsName(names, e))
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

// isPlannerGroup: whether a planner missing line of the unit names this group.
func isPlannerGroup(unitID, group string) bool {
	for _, g := range integrationMissingGroups(unitID) {
		if g == group {
			return true
		}
	}
	return false
}

// %[1]s the prefix, %[2]s the group
const portRefFileGroupFmt = " — it is a group of the planner's missing lines, which column 1 holds as %[1]s%[2]s"

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
	ported := integrationPortedRefs(ws, unitID)
	names := integrationPortedNames(ws, unitID)
	lines, _ := readPortLines(ws, unitID)
	for _, e := range lines {
		if plannerMissingRow(unitID, e.refFile) {
			continue // the integration unit's own kernel, checked by kernel_unported
		}
		if _, ok := ported[e.refFile+"\t"+e.refName]; ok {
			continue // a reference kernel another unit ported, the row kernel_unported asks for
		}
		if !refFiles[e.refFile] && !seen[e.refFile] {
			seen[e.refFile] = true
			line := fmt.Sprintf(portRefFileFmt, e.refFile, e.refName, e.file, e.name) + portedElsewhere(ported, e.refName) + portedAsName(names, e)
			// the group written without its prefix: 11/26 05:43:17 a command of the moe part wrote
			// model.language_model.layers.{i}.mlp.gate.weight in column 1 and the row fell here
			if isPlannerGroup(unitID, e.refFile) {
				line += fmt.Sprintf(portRefFileGroupFmt, integrationMissingPrefix, e.refFile)
			}
			out = append(out, line)
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
	// a reference function another unit's map ports into something that is no kernel
	portedNotKernel := map[string]string{}
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
			} else if _, had := portedNotKernel[e.refName]; !had {
				portedNotKernel[e.refName] = fmt.Sprintf(portRefPortedFmt, u, e.file, e.name)
			}
		}
	}
	groups := integrationMissingGroups(unitID)
	off := integrationGroupsOff(unitID)
	onPath := 0
	for _, g := range groups {
		if !off[g] {
			onPath++
		}
	}
	out := map[string]bool{}
	var refused []string
	for _, e := range pm {
		switch {
		case strings.HasPrefix(e.refFile, integrationMissingPrefix):
			g := strings.TrimPrefix(e.refFile, integrationMissingPrefix)
			switch {
			case missing[g] && off[g]:
				// a group the model's text never goes through: 11/26 13:27:10 kernel_unported
				// passed on missing:model.visual.blocks.{i}.attn.qkv.weight for
				// router_top10_wave_kernel and twelve rows like it, GDN, QSA and MoE kernels
				// written under groups of the vision encoder
				refused = append(refused, e.refFile+" → "+e.file+" "+e.name+portMissingOffFmt)
			case missing[g]:
				out[e.file] = true
			default:
				// which groups there are: 11/25 23:05:05 220 rows did not count, the conversation
				// having written missing:router_top10_kernel, missing:router_top10_wave and two
				// tensor names of the model as groups, and nothing named the planner's own
				refused = append(refused, e.refFile+" → "+e.file+" "+e.name+
					fmt.Sprintf(portMissingGroupsFmt, len(groups)))
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
			// 11/25 18:58:58 the conversation wrote the reference's own kernel names in column 2
			// (native_moe.cu moe_router_logits_kernel, qsa.cu qsa_bf16_qk_norm_kernel), names no
			// map ports as a kernel, and nothing said whether any map holds them at all
			if p, ok := portedNotKernel[e.refName]; ok {
				why += p
			}
			if c := countsFor[e.file]; len(c) > 0 {
				sort.Strings(c)
				why += fmt.Sprintf(portRowCountsFmt, e.file, shortList(dedupStrings(c), 4))
			} else {
				// which groups there are, here too: 11/26 04:27:08 this is the branch almost every
				// refused row takes, and only a row already written missing: was given them
				if onPath == 0 && len(off) > 0 {
					// 11/26 14:05:49 src/ix_moe_combine.cu was told a row counts with column 1
					// missing:<group>, every one of the planner's 21 missing groups being off the
					// text path, where such a row counts for no kernel
					why += fmt.Sprintf(portRowCountsNoPathFmt, e.file, len(groups))
				} else {
					why += fmt.Sprintf(portRowCountsNoneFmt, e.file, integrationMissingPrefix, len(groups))
				}
			}
			refused = append(refused, e.refFile+" "+e.refName+" → "+e.file+" "+e.name+why+")")
		}
	}
	sort.Strings(refused)
	return out, refused
}

// integrationPortedRefs: for the integration unit, the reference definitions another unit's port
// map ports as a kernel, keyed "<file>\t<name>" as that map writes them, each with the unit, its
// kernel and the file it is in. A row of the integration map naming one of them is the row
// kernel_unported counts for a kernel of the text path (integrationPortFiles), and its first
// column is that unit's reference file, not one of the integration unit's: 11/26 07:28:43
// src/kernels/cuda/gdn.cu gdn_conv_kernel → src/kernels/gpu/gdn_bf16_layer.cu, written as
// kernel_unported said, was refused by port_ref_file_unknown, and port_extra refused the rows
// like it written with a file of the integration unit's in column 1.
func integrationPortedRefs(ws, unitID string) map[string]string {
	if len(integrationAnswerOf(unitID).Ops) == 0 {
		return nil
	}
	out := map[string]string{}
	maps, _ := filepath.Glob(filepath.Join(ws, ".einsteinOS", "port", "*.tsv"))
	for _, m := range maps {
		u := strings.TrimSuffix(filepath.Base(m), ".tsv")
		if u == unitID {
			continue
		}
		ks := portKernels(ws, u)
		for _, e := range readPortMap(ws, u) {
			if ks[symbolBase(e.name)] {
				out[e.refFile+"\t"+e.refName] = u + " " + e.name + " in " + e.file
			}
		}
	}
	return out
}

// %[1]s the reference file, %[2]s the name, %[3]s the unit, its kernel and where
const portPortedElsewhereFmt = " — %[2]s is a reference kernel a unit ported: %[1]s %[2]s, by %[3]s"

// portedElsewhere: where another unit's map ports the name of column 2 as a kernel, when it does
// from a file other than column 1. 11/26 07:28:43 tests/k39_integration/moe_parity.cu
// test_moe_router_logits and src/kernels/cuda/router_top10.cu router_top10_kernel were refused
// alike, and nothing said that pcd027bc02f ports router_top10_kernel from that file.
func portedElsewhere(ported map[string]string, name string) string {
	var out []string
	for k, v := range ported {
		if f, n, _ := strings.Cut(k, "\t"); n == name {
			out = append(out, fmt.Sprintf(portPortedElsewhereFmt, f, n, v))
		}
	}
	sort.Strings(out)
	return strings.Join(out, "")
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

// integrationGroupsOff: the groups of the planner's lines off the model's text path.
func integrationGroupsOff(unitID string) map[string]bool {
	out := map[string]bool{}
	for _, g := range integrationAnswerOf(unitID).GroupsOff {
		out[g] = true
	}
	return out
}

// integrationMissingOff: those of the planner's missing groups off the model's text path.
func integrationMissingOff(unitID string) map[string]bool {
	off := integrationGroupsOff(unitID)
	out := map[string]bool{}
	for _, o := range integrationAnswerOf(unitID).Ops {
		if o.Unit == "" && off[o.Group] {
			out[o.Group] = true
		}
	}
	return out
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

// integrationOpKernels: the kernels the planner's op lines give the groups of the model's text
// path, each as its unit's map writes it — "<unit> <kernel> (<ref file> <ref name> → <file>):
// <groups>". 11/26 07:12 the integration conversation was given the op lines (gdn_conv_kernel
// of pcd027bc02f for linear_attn.conv1d.weight, …) and its parts' text, which named no kernel;
// the parts wrote eleven files of kernels of their own for those groups, and kernel_unported
// held them with nothing naming the kernels the op lines meant.
func integrationOpKernels(ws, unitID string) []string {
	a := integrationAnswerOf(unitID)
	off := integrationGroupsOff(unitID)
	type key struct{ unit, kernel string }
	groups := map[key][]string{}
	var order []key
	for _, o := range a.Ops {
		if o.Unit == "" || o.Kernel == "" || off[o.Group] {
			continue
		}
		k := key{o.Unit, o.Kernel}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], o.Group)
	}
	maps := map[string]map[string]portEntry{}
	var out []string
	for _, k := range order {
		m, ok := maps[k.unit]
		if !ok {
			m = readPortMap(ws, k.unit)
			maps[k.unit] = m
		}
		var rows []string
		for _, e := range m {
			if symbolBase(e.name) == k.kernel {
				rows = append(rows, e.refFile+" "+e.refName+" → "+e.file)
			}
		}
		sort.Strings(rows)
		where := "no row of its map names it"
		if len(rows) > 0 {
			where = strings.Join(rows, ", ")
		}
		out = append(out, fmt.Sprintf("%s %s (%s): %s", k.unit, k.kernel, where, shortList(groups[k], 4)))
	}
	return out
}

// integrationOtherPorted: for the integration unit, per file, the kernels in it the other units'
// port maps write.
func integrationOtherPorted(ws, unitID string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	maps, _ := filepath.Glob(filepath.Join(ws, ".einsteinOS", "port", "*.tsv"))
	for _, m := range maps {
		u := strings.TrimSuffix(filepath.Base(m), ".tsv")
		if u == unitID {
			continue
		}
		ks := portKernels(ws, u)
		for _, e := range readPortMap(ws, u) {
			if b := symbolBase(e.name); ks[b] {
				if out[e.file] == nil {
					out[e.file] = map[string]bool{}
				}
				out[e.file][b] = true
			}
		}
	}
	return out
}

// %[1]s the reference file, %[2]s the name
const portRowIncompleteCountsFmt = " — port_incomplete counts this row as the counterpart of %[1]s %[2]s"

// markPortIncompleteRows: the refused rows of kernel_unported that port_incomplete counts, said to
// be. 11/26 09:49:16 168 rows did not count for kernel_unported, each the counterpart
// port_incomplete had counted when it passed; the conversation removed them, and port_incomplete
// failed 163 of 168 from 09:56:53 on.
func markPortIncompleteRows(refused []string, syms []RefSymbol) []string {
	own := map[string]bool{}
	for _, s := range syms {
		own[s.File+" "+s.Name] = true
	}
	out := make([]string, len(refused))
	for i, r := range refused {
		out[i] = r
		head, _, ok := strings.Cut(r, " → ")
		if ok && own[head] && !strings.HasSuffix(r, portRowSameReason) { // those are given as one line
			f, n, _ := strings.Cut(head, " ")
			out[i] = r + fmt.Sprintf(portRowIncompleteCountsFmt, f, n)
		}
	}
	return out
}

// integrationPortedNames: for the integration unit, per kernel name another unit's port map
// writes, the row of that map: "<unit> <ref file> <ref name> → <file>".
func integrationPortedNames(ws, unitID string) map[string][]string {
	if len(integrationAnswerOf(unitID).Ops) == 0 {
		return nil
	}
	out := map[string][]string{}
	maps, _ := filepath.Glob(filepath.Join(ws, ".einsteinOS", "port", "*.tsv"))
	for _, m := range maps {
		u := strings.TrimSuffix(filepath.Base(m), ".tsv")
		if u == unitID {
			continue
		}
		ks := portKernels(ws, u)
		for _, e := range readPortMap(ws, u) {
			if n := symbolBase(e.name); ks[n] {
				out[n] = append(out[n], u+" "+e.refFile+" "+e.refName+" → "+e.file)
			}
		}
	}
	for n := range out {
		sort.Strings(out[n])
	}
	return out
}

// %[1]s the name, %[2]s the rows
const portPortedNameFmt = " — %[1]s is the name of a kernel a unit ported: %[2]s"

// portedAsName: the rows of other units' maps writing a kernel of the name column 4 gives, into a
// file other than column 3. 11/26 14:19:15 tests/k39_integration/qsa_layer_parity.cu
// test_qsa_bf16_kv_proj → src/ix_bf16_gemv.cu bf16_gemv_warp_kernel was refused with nothing
// saying that bf16_gemv_warp_kernel is the kernel the planner's op lines give pcd027bc02f for the
// projections, ported from a reference file of its own.
func portedAsName(names map[string][]string, e portEntry) string {
	var rows []string
	for _, r := range names[symbolBase(e.name)] {
		if !strings.HasSuffix(r, " → "+e.file) {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		return ""
	}
	return fmt.Sprintf(portPortedNameFmt, symbolBase(e.name), strings.Join(rows, "; "))
}

// %[1]s the reference file, %[2]d its definitions with no row, %[3]s them, %[4]s the port's files
const (
	portMissingFileFmt     = "  %[1]s: %[2]d definition(s) with no row: %[3]s%[4]s"
	portMissingSameFmt     = "; the port's %[1]s, of the same name, defines %[2]s"
	portMissingSameNoneFmt = "; the port's %[1]s, of the same name, defines none of them"
	portMissingNoSameFmt   = "; no source of the port has its name"
)

// portMissingByFile: port_incomplete's lines for the integration unit, those of definitions with
// no row of the map grouped by reference file, each with the port's sources of the same name
// (src/kernels/gdn_parity.cpp → …/gdn_parity.cu) and which of the definitions they define; the
// other lines as they are. 11/26 14:37:46 the 150 definitions with no row took 150 lines, 16 KB,
// of which the conversation could be shown the first dozen, and nothing said that the port has
// tests named like the reference's.
func portMissingByFile(ws, unitID string, lines []string) string {
	suffix := ": no line in " + PortMapPath(unitID)
	type fileMissing struct{ names []string }
	byFile := map[string]*fileMissing{}
	var order []string
	var other []string
	for _, l := range lines {
		body, ok := strings.CutSuffix(l, suffix)
		f := strings.Fields(body)
		if !ok || len(f) != 3 {
			other = append(other, l)
			continue
		}
		if byFile[f[0]] == nil {
			byFile[f[0]] = &fileMissing{}
			order = append(order, f[0])
		}
		byFile[f[0]].names = append(byFile[f[0]].names, f[2])
	}
	if len(order) == 0 {
		return ""
	}
	stems := portSourcesByStem(ws)
	var out []string
	for _, rf := range order {
		names := byFile[rf].names
		same := stems[fileStem(rf)]
		where := portMissingNoSameFmt
		if len(same) > 0 {
			where = ""
			for _, pf := range same {
				b, _ := os.ReadFile(filepath.Join(ws, pf))
				def := map[string]bool{}
				for _, s := range SymbolsOf(pf, string(b)) {
					def[symbolBase(s.Name)] = true
				}
				var has []string
				for _, n := range names {
					if def[symbolBase(n)] {
						has = append(has, n)
					}
				}
				if len(has) > 0 {
					where += fmt.Sprintf(portMissingSameFmt, pf, strings.Join(has, " "))
				} else {
					where += fmt.Sprintf(portMissingSameNoneFmt, pf)
				}
			}
		}
		out = append(out, fmt.Sprintf(portMissingFileFmt, rf, len(names), strings.Join(names, " "), where))
	}
	return strings.Join(append(out, other...), "\n")
}

// fileStem: a path's base name without its extension.
func fileStem(p string) string {
	b := filepath.Base(p)
	return strings.TrimSuffix(b, filepath.Ext(b))
}

// portSourcesByStem: the port's C/C++/CUDA sources by stem, the kernel's own directory left out.
func portSourcesByStem(ws string) map[string][]string {
	out := map[string][]string{}
	filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(ws, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".einsteinOS" || rel == ".git" || rel == verifyBuildDir {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(p) {
		case ".c", ".cc", ".cpp", ".cxx", ".cu", ".cuh", ".h", ".hpp":
			out[fileStem(rel)] = append(out[fileStem(rel)], rel)
		}
		return nil
	})
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}
