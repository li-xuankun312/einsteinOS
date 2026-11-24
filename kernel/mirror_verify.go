package kernel

// mirror_verify.go — an integration unit's mirror is verified when it changes, not only at its
// conversation's done line.
//
// 11/23 20:43 to 23:05 the integration's conversation wrote nothing and gave no done line, and
// its six parts went on for 38 rounds rewriting the mirror; the one way a verification ran was
// the integration conversation's done line, so none ran, and no part learned on this machine
// what its work compiled to. A command that changes the mirror now starts its verification when
// none is running; what it finds is carried to every conversation of the unit with its next
// command's result.

import (
	"fmt"
	"log"
	"sync"
	"time"
)

type mirrorVerifier struct {
	mu      sync.Mutex
	fn      func() (bool, string) // the unit's done-line check (dev_run.go)
	running bool
	dirty   bool // the mirror changed since the running verification began
	report  string
	seq     int
	heard   map[string]int // conv -> the report it was given
}

var (
	mirrorVerifiersMu sync.Mutex
	mirrorVerifiers   = map[string]*mirrorVerifier{}
)

// registerMirrorVerify sets (nil clears) the check a change of unitID's mirror runs.
func registerMirrorVerify(unitID string, fn func() (bool, string)) {
	mirrorVerifiersMu.Lock()
	defer mirrorVerifiersMu.Unlock()
	if fn == nil {
		delete(mirrorVerifiers, unitID)
		return
	}
	mirrorVerifiers[unitID] = &mirrorVerifier{fn: fn, heard: map[string]int{}}
}

func mirrorVerifierOf(unitID string) *mirrorVerifier {
	mirrorVerifiersMu.Lock()
	defer mirrorVerifiersMu.Unlock()
	return mirrorVerifiers[unitID]
}

const (
	// %[1]s unit
	mirrorVerifyStartFmt = "mirror: integration unit %[1]s: its mirror changed — verifying it, with no done line"
	// %[1]s when, %[2]s the report
	mirrorVerifyReportFmt = "\n[THE KERNEL'S VERIFICATION OF THE UNIT'S MIRROR — run at %[1]s because the mirror changed]\n%[2]s"
	// %[1]s when
	mirrorVerifyPassedFmt = "the verification of the mirror let the attempt end at %[1]s"
	// %[1]s the gate
	mirrorEndUnchangedFmt   = "the mirror's files are those of the last rejected verification (gate=%[1]s) again; the attempt ends and the tree is judged"
	mirrorEndBranchMovedFmt = "the branch changed files of the mirror's delta since the unit started; nothing written now can be verified, and the unit runs again"
)

// mirrorChanged starts unitID's verification when none runs, or marks the running one stale so
// that another follows it.
func mirrorChanged(unitID string) {
	v := mirrorVerifierOf(unitID)
	if v == nil {
		return
	}
	v.mu.Lock()
	if v.running {
		v.dirty = true
		v.mu.Unlock()
		return
	}
	v.running = true
	v.mu.Unlock()
	go func() {
		for {
			log.Printf(mirrorVerifyStartFmt, unitID)
			end, report := v.fn()
			at := time.Now().UTC().Format("15:04:05")
			if end && report == "" {
				report = fmt.Sprintf(mirrorVerifyPassedFmt, at)
			}
			v.mu.Lock()
			if report != "" {
				v.seq++
				v.report = fmt.Sprintf(mirrorVerifyReportFmt, at, report)
			}
			again := v.dirty
			v.dirty = false
			if !again {
				v.running = false
				v.mu.Unlock()
				return
			}
			v.mu.Unlock()
		}
	}()
}

// mirrorVerifyNews: the latest verification's report, once to each conversation.
func mirrorVerifyNews(unitID, conv string) string {
	v := mirrorVerifierOf(unitID)
	if v == nil {
		return ""
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.seq == 0 || v.heard[conv] >= v.seq {
		return ""
	}
	v.heard[conv] = v.seq
	return v.report
}

// %[1]s repo, %[2]s unit, %[3]s the last gate — 11/24 09:11:01 a change of the mirror that left its
// delta as the last verification had it counted as a done line with nothing changed (1 of 3),
// the third of which ends the attempt
const preVerifyMirrorSameFmt = "dev_preverify: %[1]s unit %[2]s: the mirror changed but its files are those of the last rejected verification (gate=%[3]q) — not verified again, and not a done line"
