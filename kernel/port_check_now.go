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
	if len(passed) > 0 {
		s += fmt.Sprintf(portCheckPassFmt, strings.Join(passed, ", "))
	}
	if len(failed) > 0 {
		s += ShapeOutput(strings.Join(failed, "\n"), LocalResultCap()/2) + "\n"
	}
	return s
}
