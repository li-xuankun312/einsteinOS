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
	"strings"
	"sync"
	"time"
)

type mirrorVerifier struct {
	mu      sync.Mutex
	fn      func() (bool, string) // the unit's done-line check (dev_run.go)
	running bool
	dirty   bool     // the mirror changed since the running verification began
	during  []string // what was written since the running verification began, with whose and when
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
	// %[1]s when it began, %[2]d how many, %[3]s the writes
	mirrorVerifyDuringFmt = "\n[WRITTEN AFTER THIS VERIFICATION BEGAN AT %[1]s — not in what it ran on; another verification follows]\n%[2]d write(s): %[3]s\n"
	// %[1]s the file, %[2]s whose, %[3]s when
	mirrorVerifyDuringItemFmt = "%[1]s by %[2]s at %[3]s"
	mirrorVerifyDuringMax     = 12 // writes named at most
)

// mirrorChanged starts unitID's verification when none runs, or marks the running one stale so
// that another follows it; wrote is what the change wrote, each "file by whose".
func mirrorChanged(unitID string, wrote ...string) {
	v := mirrorVerifierOf(unitID)
	if v == nil {
		return
	}
	v.mu.Lock()
	if v.running {
		v.dirty = true
		now := time.Now().UTC().Format("15:04:05")
		for _, w := range wrote {
			f, who, _ := strings.Cut(w, " by ")
			v.during = append(v.during, fmt.Sprintf(mirrorVerifyDuringItemFmt, f, who, now))
		}
		v.mu.Unlock()
		return
	}
	v.running = true
	v.mu.Unlock()
	go func() {
		for {
			log.Printf(mirrorVerifyStartFmt, unitID)
			began := time.Now().UTC().Format("15:04:05")
			end, report := v.fn()
			at := time.Now().UTC().Format("15:04:05")
			if end && report == "" {
				report = fmt.Sprintf(mirrorVerifyPassedFmt, at)
			}
			v.mu.Lock()
			if report != "" {
				// what was written while it ran is not in it: 11/25 12:23:44 the verification
				// begun about 12:16 passed its compile, and embed (b10706e5) had written
				// embed_parity.cu's second e2e_embed_main since — 12:32:45 failed on it
				if len(v.during) > 0 {
					report += fmt.Sprintf(mirrorVerifyDuringFmt, began, len(v.during), shortList(v.during, mirrorVerifyDuringMax))
				}
				v.seq++
				v.report = fmt.Sprintf(mirrorVerifyReportFmt, at, report)
			}
			v.during = nil
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

// %[1]s when, %[2]s the report
const mirrorVerifyDoneReportFmt = "\n[THE KERNEL'S VERIFICATION OF THE UNIT'S MIRROR — run at %[1]s at the done line of the unit's own conversation]\n%[2]s"

// mirrorVerifyShare: the report of a verification run at the done line of the unit's own
// conversation, carried to the unit's other conversations with their next command's result —
// the unit's own conversation had it as its done line's answer. 11/25 04:23:56 the verification
// held test.sh on src/ix_router_top10_wave.cu and its text went to the integration conversation
// alone; a mirror change's verification went to every conversation of the unit.
func mirrorVerifyShare(unitID, report string) {
	v := mirrorVerifierOf(unitID)
	if v == nil || report == "" {
		return
	}
	var own []string
	mirrorUnitsMu.Lock()
	u := mirrorUnits[unitID]
	mirrorUnitsMu.Unlock()
	if u != nil {
		u.mu.Lock()
		for conv, part := range u.part {
			if part == "" {
				own = append(own, conv)
			}
		}
		u.mu.Unlock()
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.seq++
	v.report = fmt.Sprintf(mirrorVerifyDoneReportFmt, time.Now().UTC().Format("15:04:05"), report)
	for _, c := range own {
		v.heard[c] = v.seq
	}
}
