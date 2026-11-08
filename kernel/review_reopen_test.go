package kernel

import (
	"strings"
	"testing"
)

// A passed unit a later review faulted runs again: its pass no longer holds, its failure is
// the reviewer's own words, and taking it clears it. A second verdict against the same unit
// keeps the first.
func TestAFaultedUnitIsReopened(t *testing.T) {
	n := DevPlanNode{ID: "u41e2bf114f", Sources: []string{"src/prefill/kernels.cu"}}
	p := &DevPlan{PortPass: map[string]bool{"u41e2bf114f": true}}
	if !p.UnitPass(n) {
		t.Fatal("it passed")
	}
	f := DevFailure{Kind: "review_fail", Text: "kv_append writes f32 scales, theory says f16 (reviewing pf7b804cf95)"}
	p.MarkReopen(n.ID, f)
	p.MarkReopen(n.ID, DevFailure{Kind: "review_fail", Text: "something else"})
	if p.UnitPass(n) || !p.Reopened(n.ID) {
		t.Fatal("a reopened unit has not passed")
	}
	got, ok := p.TakeReopen(n.ID)
	if !ok || got.Text != f.Text {
		t.Fatalf("the first verdict is kept: %+v", got)
	}
	if p.Reopened(n.ID) || !p.UnitPass(n) {
		t.Fatal("taken, it passes again until it runs")
	}
}

// A verdict naming only another unit's files passes the unit under review and reopens that
// other unit with the reviewer's words; a verdict naming one of its own files stays its own.
func TestAVerdictAboutAnotherUnitsFilesDoesNotFailThisUnit(t *testing.T) {
	written := map[string][]string{"src/prefill/kernels.cu": {"u41e2bf114f"}}
	sources := map[string][]string{"pf7b804cf95": {"include/strata/prefill/kernels.hpp"}}

	other := "FAIL: kv_append (src/prefill/kernels.cu) writes f32 scales but the theory says f16"
	blames := reviewBlames("pf7b804cf95", reviewOwners(other, written, sources))
	if len(blames) != 1 || blames[0] != "u41e2bf114f" {
		t.Fatalf("blames: %v", blames)
	}
	p := &DevPlan{PortPass: map[string]bool{"u41e2bf114f": true}}
	reason := "kv_append writes f32 scales"
	p.MarkReopen(blames[0], DevFailure{Kind: "review_fail", Text: fmtReopen(reason, "pf7b804cf95")})
	f, _ := p.TakeReopen("u41e2bf114f")
	if !strings.Contains(f.Text, reason) || !strings.Contains(f.Text, "pf7b804cf95") {
		t.Fatalf("the reopened unit's failure: %q", f.Text)
	}

	own := "FAIL: include/strata/prefill/kernels.hpp declares the wrong geometry, and src/prefill/kernels.cu follows it"
	if b := reviewBlames("pf7b804cf95", reviewOwners(own, written, sources)); len(b) == 0 {
		t.Fatal("a verdict naming its own file as well still names the other unit")
	}
}

func fmtReopen(reason, unit string) string {
	return strings.ReplaceAll(strings.ReplaceAll(reopenReasonFmt, "%[1]s", reason), "%[2]s", unit)
}
