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

// notePreverifySeen marks unitID's scripts as seen when a done line's verification ran them on
// this machine and its result went back to the conversation; it says whether that is new, so
// the plan is stored at once — a run stopped before the attempt ends knows it. 11/21 08:35 and
// 10:19 the integration unit was "would be split …, but none of its conversations has seen its
// build.sh/test.sh run on the real machine" though its conversations had been sent seven runs
// of them since 08:44: only a conversation running the scripts itself counted, and the plan
// learned it only when an attempt ended.
func notePreverifySeen(plan *DevPlan, unitID string, o UnitVerifyOutcome) bool {
	if !o.Verified || o.Err != nil || plan.VerifySeen[unitID] {
		return false
	}
	if plan.VerifySeen == nil {
		plan.VerifySeen = map[string]bool{}
	}
	plan.VerifySeen[unitID] = true
	return true
}
