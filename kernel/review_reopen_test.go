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

// A reopened unit runs after everything already queued, and its reopen becomes its failure
// when it does.
func TestReopenedUnitsRunLast(t *testing.T) {
	leaves := []DevPlanNode{
		{ID: "u8", Sources: []string{"a.cu"}},
		{ID: "u9", Sources: []string{"b.cu"}},
		{ID: "u20", Sources: []string{"c.hpp"}},
		{ID: "u21", Sources: []string{"d.hpp"}},
	}
	p := &DevPlan{PortPass: map[string]bool{"u8": true, "u9": true, "u20": true}}
	if got := nextUnitIndex(p, leaves); got != 3 {
		t.Fatalf("the queued unit first: %d", got)
	}
	p.MarkReopen("u8", DevFailure{Kind: "review_fail", Text: "kv_append"})
	p.MarkReopen("u9", DevFailure{Kind: "review_fail", Text: "gr_norm"})
	if got := nextUnitIndex(p, leaves); got != 3 {
		t.Fatalf("still the queued unit: %d", got)
	}
	p.PortPass["u21"] = true
	if got := nextUnitIndex(p, leaves); got != 0 {
		t.Fatalf("then the reopened ones, in plan order: %d", got)
	}
	p.TakeReopen("u8")
	if got := nextUnitIndex(p, leaves); got != 1 {
		t.Fatalf("the next reopened one: %d", got)
	}
	p.TakeReopen("u9")
	if got := nextUnitIndex(p, leaves); got != -1 {
		t.Fatalf("nothing left: %d", got)
	}
}

// Taking a reopen makes it the unit's failure, so its next conversation is a fix conversation
// that is given the reviewer's words.
func TestAReopenBecomesTheUnitsFailure(t *testing.T) {
	p := &DevPlan{PortPass: map[string]bool{"u9": true}, LastFailure: map[string]DevFailure{}, FixAttempts: map[string]int{}}
	f := DevFailure{Kind: "review_fail", Text: "gr_norm normalizes jointly over all 4 streams", BaseRef: "einsteinOS/dev-x"}
	p.MarkReopen("u9", f)
	got, ok := p.TakeReopen("u9")
	if !ok || got.Text != f.Text || got.BaseRef != f.BaseRef {
		t.Fatalf("%+v", got)
	}
	p.LastFailure["u9"] = got
	p.FixAttempts["u9"] = 1
	if p.LastFailure["u9"].Text != f.Text || p.FixAttempts["u9"]+1 != 2 {
		t.Fatal("its next conversation is a fix conversation with that verdict")
	}
}
