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
	definedIn := func(file, name string) (exists, ok bool) {
		d, seen := defined[file]
		if !seen {
			b, err := os.ReadFile(filepath.Join(ws, file))
			if err != nil {
				defined[file] = nil
				return false, false
			}
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
	portExtraFmt     = "  %[1]s %[2]s → %[3]s:%[4]s: the line names no definition this unit ports"
	portDuplicateFmt = "  %[1]s %[2]s → %[3]s:%[4]s: a second line for a definition line %[5]d already maps"
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
