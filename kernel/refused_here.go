package kernel

// refused_here.go — the commands of a conversation the shadow did not run on this machine
// because they touched the kernel's credentials or its own directories, per conversation.
//
// 11/22 14:30:39-14:31:20 the integration's dev_fix_5 ran eleven commands at .git-credentials,
// .ssh, .config/gh, git credential, github_token and /environ; each was refused here and logged
// on a line of its own, and nothing said at the conversation's end that it had tried.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

var (
	refusedMu   sync.Mutex
	refusedHere = map[string]map[string]int{} // conversation -> what was touched -> times
)

// NoteRefusedHere records that a command of conv was not run here because it touched what.
func NoteRefusedHere(conv, what string) {
	if conv == "" || what == "" {
		return
	}
	refusedMu.Lock()
	defer refusedMu.Unlock()
	if refusedHere[conv] == nil {
		refusedHere[conv] = map[string]int{}
	}
	refusedHere[conv][what]++
}

// %[1]d commands, %[2]s what they touched
const refusedHereFmt = "; %[1]d command(s) not run here for touching the kernel's credentials or directories: %[2]s"

// RefusedHereSummary takes what was recorded for conv ("" when nothing was), for the line its
// end is logged on.
func RefusedHereSummary(conv string) string {
	refusedMu.Lock()
	m := refusedHere[conv]
	delete(refusedHere, conv)
	refusedMu.Unlock()
	if len(m) == 0 {
		return ""
	}
	var keys []string
	n := 0
	for k, c := range m {
		keys = append(keys, fmt.Sprintf("%s ×%d", k, c))
		n += c
	}
	sort.Strings(keys)
	return fmt.Sprintf(refusedHereFmt, n, strings.Join(keys, ", "))
}
