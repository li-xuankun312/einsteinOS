package kernel

import (
	"encoding/json"
	"testing"
)

// 11/20 the integration unit's attempt 1 (00:34) had its tree rejected at four done lines
// (00:59, 01:08, 01:20 and the evaluation of 01:25) and nothing of it was pushed: the wip branch
// is written only when an attempt ends. The run was stopped at 02:49, and at 03:38 the plan
// still said attempts=1 with no failure, so the next conversation started again from
// einsteinOS/dev-c880662efd and wrote its 25 files anew. A tree rejected at a done line of a
// first attempt is parked on the unit's wip branch and the plan says so; a run that starts the
// unit again continues from there; the unit's end — passed or failed — clears it.
func TestARejectedTreeOfAFirstAttemptIsWhereARestartContinues(t *testing.T) {
	const dev, unit = "einsteinOS/dev-c880662efd", "uintegration01529aaa82b9"
	if !parkRejectedTree(dev, dev, true) {
		t.Fatal("a first attempt's rejected tree is not parked")
	}
	// 11/21 08:44-10:05 a fix attempt's seven rejected trees were parked nowhere: it parks
	// too, on a branch that is not its base
	if !parkRejectedTree(dev, devWipBranch(dev, unit), true) {
		t.Fatal("a fix attempt's rejected tree is not parked")
	}
	if got := parkTarget(dev, dev, unit); got != devWipBranch(dev, unit) {
		t.Fatalf("a first attempt parks on %s", got)
	}
	if got := parkTarget(dev, devWipBranch(dev, unit), unit); got != devContBranch(dev, unit) {
		t.Fatalf("a fix attempt from the wip branch parks on %s, its own base or elsewhere", got)
	}
	if got := parkTarget(dev, devContBranch(dev, unit), unit); got != devWipBranch(dev, unit) {
		t.Fatalf("an attempt from the -cont branch parks on %s", got)
	}
	if parkRejectedTree(dev, dev, false) {
		t.Fatal("a verification that did not run parks nothing")
	}

	p := &DevPlan{}
	p.NoteInProgress(unit, DevFailure{Kind: "port_extra", BaseRef: devWipBranch(dev, unit), BaseSHA: "abc123"})
	b, _ := p.Marshal()
	var q DevPlan
	if err := json.Unmarshal(b, &q); err != nil {
		t.Fatal(err)
	}
	f := q.ResumeFrom(unit)
	if f.BaseRef != devWipBranch(dev, unit) || f.BaseSHA != "abc123" {
		t.Fatalf("a restart continues from %+v", f)
	}
	// an attempt that has not ended is later than the last that did (which cleared it): a fix
	// attempt stopped after parking continues from its park, not from the failure before it
	q.LastFailure = map[string]DevFailure{unit: {Kind: "integration_correctness_unmeasured", BaseRef: "elsewhere"}}
	if f := q.ResumeFrom(unit); f.BaseRef != devWipBranch(dev, unit) || f.Kind != "integration_correctness_unmeasured" {
		t.Fatalf("a stopped fix attempt does not continue from its park: %+v", f)
	}
	delete(q.InProgress, unit)
	if f := q.ResumeFrom(unit); f.BaseRef != "elsewhere" {
		t.Fatalf("an attempt that ended is continued from its own failure, got %+v", f)
	}
	q.Fail(unit, DevFailure{Kind: "port_extra", BaseRef: devWipBranch(dev, unit)})
	if _, ok := q.InProgress[unit]; ok {
		t.Fatal("a failed attempt left its in-progress tree")
	}
	q.NoteInProgress(unit, DevFailure{BaseRef: devWipBranch(dev, unit)})
	q.MarkUnitPass(DevPlanNode{ID: unit, Integration: true})
	if _, ok := q.InProgress[unit]; ok {
		t.Fatal("a passed unit left its in-progress tree")
	}
}

// A parked tree is the attempt's base with its delta, not the wip branch's earlier tree with it:
// the delta is measured against the base, so a file an earlier park changed and the conversation
// set back is not in it, and laid over the earlier park it would keep the earlier text.
func TestAParkedTreeIsTheBaseWithTheDelta(t *testing.T) {
	bare, clone := setupRemote(t)
	const dev = "einsteinOS/dev-7"
	wip := devWipBranch(dev, "u1")
	mirror := t.TempDir()
	write(t, mirror, "src/base.cu", "changed")
	write(t, mirror, "src/new.cu", "new")
	if err := commitDelta(clone, "o/r", dev, wip, mirror, []string{"src/base.cu", "src/new.cu"}, nil, "park 1"); err != nil {
		t.Fatal(err)
	}
	write(t, mirror, "src/base.cu", "base") // set back: no longer in the delta
	if err := commitDelta(clone, "o/r", dev, wip, mirror, []string{"src/new.cu"}, nil, "park 2"); err != nil {
		t.Fatal(err)
	}
	got := git(t, clone, "--git-dir", bare, "show", wip+":src/base.cu")
	if got != "base" {
		t.Fatalf("src/base.cu on %s is %q, not the base's", wip, got)
	}
	if !remoteHas(t, bare, wip, "src/new.cu") {
		t.Fatal("the delta is not on the wip branch")
	}
}
