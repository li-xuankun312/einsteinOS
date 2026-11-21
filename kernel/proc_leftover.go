package kernel

// proc_leftover.go — what a command left running after it returned.
//
// A command run here for a conversation (ForkExec) runs in a process group of its own, so
// what it starts in the background stays findable after the command itself has returned:
// 11/21 06:44-06:47 the evaluation's search a07300a2 ran vllm benchmarks here (pids 98-102),
// and nothing said whether any of them still held the GPUs when the integration's end-to-end
// run was to measure the port on them.

import (
	"fmt"
	"sort"
	"sync"
)

var (
	leftoverMu sync.Mutex
	leftovers  = map[int]string{} // pgid → the command that left it
)

// noteReturned is called when a command's leader has returned: a group with a process still
// in it is a leftover (and stays tracked, so a Ctrl-C still kills it); an empty one is done.
func noteReturned(pgid int, command string) {
	if pgid <= 0 {
		return
	}
	if !pgidAlive(pgid) {
		UntrackProcessGroup(pgid)
		return
	}
	leftoverMu.Lock()
	leftovers[pgid] = truncStr(command, 80)
	leftoverMu.Unlock()
}

// KillLeftoverProcessGroups kills every group a returned command left running and says which.
func KillLeftoverProcessGroups() []string {
	leftoverMu.Lock()
	var pgids []int
	for p := range leftovers {
		pgids = append(pgids, p)
	}
	sort.Ints(pgids)
	var killed []string
	for _, p := range pgids {
		if pgidAlive(p) {
			killPgid(p)
			killed = append(killed, fmt.Sprintf(leftoverKilledFmt, p, leftovers[p]))
		}
		delete(leftovers, p)
		UntrackProcessGroup(p)
	}
	leftoverMu.Unlock()
	return killed
}

// %[1]d the group, %[2]s the command that left it
const leftoverKilledFmt = "group %[1]d (left by %[2]q)"
