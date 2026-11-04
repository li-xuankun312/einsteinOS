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
	definedIn := func(file, name string) (exists, ok bool) {
		d, seen := defined[file]
		if !seen {
			b, err := os.ReadFile(filepath.Join(ws, file))
			if err != nil {
				defined[file] = nil
				return false, false
			}
			d = map[string]bool{}
			for _, s := range SymbolsOf(file, string(b)) {
				d[symbolBase(s.Name)] = true
			}
			defined[file] = d
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
