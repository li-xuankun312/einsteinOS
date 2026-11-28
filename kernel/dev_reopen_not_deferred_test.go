package kernel

import "testing"

// 11/21 14:21:36 pf7b804cf95 was reopened with three review failures stored; judged by them it
// was deferred, still reopened, looked at again, deferred again — 320 times until 21:13. A
// reopened unit runs: its attempts start again from the reopen.
func TestAReopenedUnitIsNotDeferredAgainAndAgain(t *testing.T) {
	unit := DevPlanNode{ID: "pf7b804cf95", Sources: []string{"include/strata/prefill/gemm.hpp"}}
	p := &DevPlan{
		FixAttempts: map[string]int{unit.ID: 3},
		FailHistory: map[string][]string{unit.ID: {"review_fail", "review_fail", "review_fail"}},
		LastFailure: map[string]DevFailure{unit.ID: {Kind: "review_fail", Text: "gr_norm normalizes jointly over all 4 streams"}},
		VerifySeen:  map[string]bool{unit.ID: true},
		Reopen:      map[string]DevFailure{unit.ID: {Kind: "port_map_read", Text: "its scripts read its port map"}},
	}
	r := &devRun{plan: p, repo: "Niko1221/Strata", branch: "einsteinOS/dev-3b59928b8f"}
	if r.prepareUnit(unit, "") {
		t.Fatalf("a reopened unit was decided about instead of run (deferred: %v)", p.Deferred)
	}
	if _, deferred := p.Deferred[unit.ID]; deferred {
		t.Fatal("a reopened unit was deferred")
	}
	// not reopened, the same unit is deferred as before
	delete(p.Reopen, unit.ID)
	if !r.prepareUnit(unit, "") || r.halt != nil {
		t.Fatal("an exhausted unit that was not reopened is no longer deferred")
	}
	if _, deferred := p.Deferred[unit.ID]; !deferred {
		t.Fatal("not deferred")
	}
}
