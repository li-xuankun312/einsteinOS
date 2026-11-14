package kernel

// verify_seen.go — whether a conversation has had its own build.sh/test.sh run on the real
// machine. 11/05 unit 7 was split three times after "3 fix attempts used", but none of those
// conversations had seen the real compiler or GPU; the failures came from missing feedback,
// not from size. A split is decided only once the unit's conversations have seen it.

import "sync"

var (
	verifySeenMu sync.Mutex
	verifySeen   = map[string]bool{}
)

// NoteVerifySeen records that convID's verify scripts ran on this machine.
func NoteVerifySeen(convID string) {
	if convID == "" {
		return
	}
	verifySeenMu.Lock()
	verifySeen[convID] = true
	verifySeenMu.Unlock()
}

// VerifySeen reports whether convID's verify scripts ran on this machine.
func VerifySeen(convID string) bool {
	verifySeenMu.Lock()
	defer verifySeenMu.Unlock()
	return verifySeen[convID]
}

// devSplitUnseenFmt: a split deferred because no conversation of the unit saw its scripts run.
const devSplitUnseenFmt = "dev_from_theorems: %[1]s unit %[2]s would be split (%[3]s), but none of its conversations has seen its build.sh/test.sh run on the real machine — another attempt instead"
