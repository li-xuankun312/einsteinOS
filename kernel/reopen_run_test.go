package kernel

import (
	"strings"
	"testing"
)

// A unit a later review reopened runs again through dev_from_theorems itself: its next
// conversation is a fix conversation handed the verdict that reopened it, on the branch as it
// stands. 11/09 the test of this step (1793e718) exercised the plan's helpers and never the
// run that takes the reopen.
func TestAReopenedUnitRunsAsAFixConversationThroughTheRun(t *testing.T) {
	env := newDevEnv(t, theory2("Alpha"))
	const verdict = "gr_norm normalizes jointly over all 4 streams (D=10240) but theory/decode reference norm each stream row (N=2560) separately"
	env.model.unitWork = func(prompt, unit string, n int) {
		if n == 2 {
			if !strings.Contains(prompt, "第 2 次，上一次") || !strings.Contains(prompt, verdict) {
				t.Errorf("the reopened unit's conversation is not a fix conversation with the verdict:\n%s", prompt)
			}
			mustWrite(env.mirror, "src/impl.cu", "// per-stream norm\n")
		} else {
			mustWrite(env.mirror, "src/impl.cu", "// joint norm\n")
		}
		env.model.writeScripts(unit, 0, 0, "built")
	}
	const pid = 9031
	step := &Step{Owner: "ref", Repo: "proj"}
	if _, err := dev_from_theorems(pid, step); err != nil {
		t.Fatal(err)
	}
	branch := devBranchFor("ref/proj", "refsha1", pid)
	devPlanMu.Lock()
	var plan *DevPlan
	for _, p := range devPlanMem {
		plan = p
	}
	devPlanMu.Unlock()
	if plan == nil || len(plan.Leaves()) != 1 {
		t.Fatal("one unit planned")
	}
	id := plan.Leaves()[0].ID
	plan.MarkReopen(id, DevFailure{Kind: "review_fail", Text: verdict, BaseRef: branch})
	before := len(env.model.prompts)
	if _, err := dev_from_theorems(pid, step); err != nil {
		t.Fatal(err)
	}
	fixes := 0
	for _, p := range env.model.prompts[before:] {
		if strings.Contains(p, "[THEORY — unit ") {
			fixes++
		}
	}
	if fixes != 1 {
		t.Fatalf("want one fix conversation for the reopened unit, got %d", fixes)
	}
	if got := bareShow(env.bare, branch+":src/impl.cu"); !strings.Contains(got, "per-stream") {
		t.Fatalf("the fix is on the dev branch: %q", got)
	}
}
