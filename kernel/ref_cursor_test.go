package kernel

import "testing"

// 11/03: commit 0 of 2438 completed ("2437 remaining") and nothing came next.
func TestTheCursorMovesOnlyAfterACompletePipeline(t *testing.T) {
	const pid = 77001
	if advanceAfterPipeline(pid, "Niko1221", "Strata") {
		t.Fatal("nothing bound: nothing to advance from")
	}
	recordRefBound(pid, "Niko1221/Strata", boundRef{Idx: 0, SHA: "01529aaa82b9f5c5", Total: 2438})
	before, _ := PRStepCount(pid)
	if !advanceAfterPipeline(pid, "Niko1221", "Strata") {
		t.Fatal("2437 commits remain")
	}
	prStepsMu.Lock()
	steps := append([]*Step{}, prSteps[pid][before:]...)
	prStepsMu.Unlock()
	if len(steps) != 2 || steps[0].Kind != STEP_REF_BIND || steps[0].Command != "start=1" || steps[1].Kind != STEP_DEV_FROM_THEOREMS {
		t.Fatalf("queued %+v", steps)
	}
	if got := ref_bind_cursor(pid, "Niko1221", "Strata"); got != 1 {
		t.Fatalf("cursor %d", got)
	}
	// the last commit: nothing more
	recordRefBound(pid, "Niko1221/Strata", boundRef{Idx: 2437, SHA: "ffff", Total: 2438})
	if advanceAfterPipeline(pid, "Niko1221", "Strata") {
		t.Fatal("past the last commit")
	}
}
