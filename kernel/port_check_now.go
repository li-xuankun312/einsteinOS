package kernel

import (
	"fmt"
	"strings"
	"sync"
)

// The port map's gates read the map and the sources only, and need no build: a command that
// changes the map is answered with them at once. 11/26 09:41 to 11:34 the integration unit's port
// map was changed some twenty times, and each change was judged only at the end of a 7-minute
// verification that had taken the mirror before it.

var (
	portChecksMu sync.Mutex
	portChecks   = map[string]func() string{}
)

// registerPortCheck sets (nil clears) what a change of unitID's port map is answered with.
func registerPortCheck(unitID string, fn func() string) {
	portChecksMu.Lock()
	defer portChecksMu.Unlock()
	if fn == nil {
		delete(portChecks, unitID)
		return
	}
	portChecks[unitID] = fn
}

// portCheckNow runs unitID's registered port check ("" when none).
func portCheckNow(unitID string) string {
	portChecksMu.Lock()
	fn := portChecks[unitID]
	portChecksMu.Unlock()
	if fn == nil {
		return ""
	}
	return fn()
}

// portGateKinds: the gates that read the port map and the sources only.
var portGateKinds = []string{"kernel_unported", "port_ref_file_unknown", "port_extra", "port_name_foreign", "port_incomplete"}

const (
	// %[1]s the map
	portCheckHeadFmt = "\n[THE PORT MAP AS THIS COMMAND LEFT IT — %[1]s, judged now on the mirror by the gates that read the map and the sources, with no build]\n"
	// %[1]s the gates
	portCheckPassFmt = "%[1]s: pass\n"
	// %[1]d rows of the map as this command left it
	portCheckRowsFmt = "the map holds %[1]d row(s)\n"
)

// portGatesNow: the port map's gates of in, as the command result gives them.
func portGatesNow(in GateInput, mapPath string) string {
	kinds, texts := unitGatesAll(in)
	is := map[string]bool{}
	for _, k := range portGateKinds {
		is[k] = true
	}
	var failed []string
	failedKind := map[string]bool{}
	for i, k := range kinds {
		if is[k] {
			failed = append(failed, texts[i])
			failedKind[k] = true
		}
	}
	var passed []string
	for _, k := range portGateKinds {
		if !failedKind[k] {
			passed = append(passed, k)
		}
	}
	s := fmt.Sprintf(portCheckHeadFmt, mapPath)
	if n := portMapRows(in); n > 0 {
		s += fmt.Sprintf(portCheckRowsFmt, n)
	}
	if len(passed) > 0 {
		s += fmt.Sprintf(portCheckPassFmt, strings.Join(passed, ", "))
	}
	if len(failed) > 0 {
		s += ShapeOutput(strings.Join(failed, "\n"), LocalResultCap()/2) + "\n"
	}
	return s
}

// portMapRows: how many rows of the port map the gates read — those that count for a kernel and
// those refused — as this command left it. 11/27 05:28:59 port_incomplete went from passing to
// 168 of 168 and port_extra to a row mapping a definition twice, uintegration01529aaa82b9's map
// having been written over while its gates were checked row by row.
func portMapRows(in GateInput) int {
	n := len(in.PortFilesRefused)
	for _, ks := range in.RowKernels {
		n += len(ks)
	}
	return n
}

// The reference definitions of the integration unit's port map, keyed "<file>\t<name>" as its
// rows write them: a part's command that leaves one of them with no row, where the map had one, is
// written back (mirror_writes.go). 11/27 the branch the unit started from has no port map, so a
// part could take out any row: at 08:09, 08:52 and 09:02 the map held none of the 168 rows
// port_incomplete counts, the gdn part's command of 09:12:27 put them back, and the qsa part's of
// 09:31:45 took them out again.
var (
	portSymsMu sync.Mutex
	portSyms   = map[string]map[string]bool{}
)

// registerPortSymbols sets (nil clears) unitID's reference definitions.
func registerPortSymbols(unitID string, syms []RefSymbol) {
	portSymsMu.Lock()
	defer portSymsMu.Unlock()
	if syms == nil {
		delete(portSyms, unitID)
		return
	}
	m := map[string]bool{}
	for _, s := range syms {
		m[s.File+"\t"+s.Name] = true
	}
	portSyms[unitID] = m
}

// portMapRefSymbols: unitID's registered reference definitions (nil when none).
func portMapRefSymbols(unitID string) map[string]bool {
	portSymsMu.Lock()
	defer portSymsMu.Unlock()
	return portSyms[unitID]
}

// portRowKey: "<column 1>\t<column 2>" of a port map line ("" for a line that is no row).
func portRowKey(line string) string {
	f := strings.Split(strings.TrimRight(line, "\r"), "\t")
	if len(f) < 4 {
		return ""
	}
	return strings.TrimSpace(f[0]) + "\t" + strings.TrimSpace(f[1])
}

// refRowTakenOut: a reference definition of refs that a row of was maps and no row of now does.
func refRowTakenOut(refs map[string]bool, was, now string) (string, bool) {
	held := map[string]bool{}
	for _, l := range strings.Split(now, "\n") {
		held[portRowKey(l)] = true
	}
	for _, l := range strings.Split(was, "\n") {
		if k := portRowKey(l); k != "" && refs[k] && !held[k] {
			return strings.Replace(k, "\t", " ", 1), true
		}
	}
	return "", false
}
