package kernel

import "testing"

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
