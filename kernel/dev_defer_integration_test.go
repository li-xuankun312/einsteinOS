package kernel

import (
	"testing"
)

// 11/21 14:15:45 the integration unit, its four attempts used, was "left to the integration
// unit" — itself — and the commit was reported complete with every unit passing. The
// integration unit is never deferred and its fix attempts do not end it, and a plan that has it
// deferred does not count it as passed.
func TestTheIntegrationUnitIsNeverDeferred(t *testing.T) {
	integ := DevPlanNode{ID: "uintegration01529aaa82b9", Integration: true}
	p := &DevPlan{
		FixAttempts: map[string]int{integ.ID: 4},
		LastFailure: map[string]DevFailure{integ.ID: {Kind: "compile_fail", Text: "build.sh exit=1"}},
		VerifySeen:  map[string]bool{integ.ID: true},
	}
	r := &devRun{plan: p, repo: "Niko1221/Strata", branch: "einsteinOS/dev-c880662efd"}
	// it runs on: its fix attempts do not end it (its directions are budgeted, eval_direction.go)
	if r.prepareUnit(integ, "") || r.halt != nil {
		t.Fatalf("the integration unit was decided about instead of run: halt %v", r.halt)
	}
	if _, deferred := p.Deferred[integ.ID]; deferred {
		t.Fatal("the integration unit was deferred")
	}
	// a plan stored with it deferred (as on 11/21 14:15) does not count it as passed
	p.MarkDeferred(integ.ID, p.LastFailure[integ.ID])
	if p.UnitPass(integ) {
		t.Fatal("a deferred integration unit counts as passed")
	}
	port := DevPlanNode{ID: "pf7b804cf95", Sources: []string{"a.hpp"}}
	p.MarkDeferred(port.ID, DevFailure{Kind: "review_fail"})
	if !p.UnitPass(port) {
		t.Fatal("a deferred port unit is no longer left to the integration unit")
	}
}
