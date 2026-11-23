package kernel

import (
	"strings"
	"testing"
)

// 11/22 16:58:43 a done line's verification failed on integration_reference_impl_missing (the
// machine), the conversation ended, and the same tree was verified again from 16:59:06: the
// verification that let it end is the one used
func TestTheVerificationThatLetTheConversationEndIsUsed(t *testing.T) {
	machine := &preVerifyResult{ok: false, digest: "d1", out: UnitVerifyOutcome{Verified: true, Result: VerifyResult{Gate: GateReferenceImplMissing}}}
	if !machine.endedOn("d1") {
		t.Fatal("a machine gate's verification of the same tree is not used")
	}
	if machine.endedOn("d2") {
		t.Fatal("another tree used the verification")
	}
	unitFail := &preVerifyResult{ok: false, digest: "d1", out: UnitVerifyOutcome{Verified: true, Result: VerifyResult{Gate: "integration_e2e_unmeasured"}}}
	if unitFail.endedOn("d1") {
		t.Fatal("a gate the conversation can change did not end it, and is not used")
	}
	passed := &preVerifyResult{ok: true, digest: "d1", out: UnitVerifyOutcome{Verified: true}}
	if !passed.endedOn("d1") {
		t.Fatal("a pass of the same tree is not used")
	}
	var none *preVerifyResult
	if none.endedOn("d1") {
		t.Fatal("no verification used")
	}
}

// 11/22 23:29 the integration's dev_fix_7 was given the judge's environment failure
// (integration_reference_impl_missing) as its last failure and set out to change this machine's
// transformers: a gate outside the unit is not given to the unit's next conversation
func TestAGateOutsideTheUnitIsNotTheNextConversationsFailure(t *testing.T) {
	r := &devRun{pid: 1, repo: "o/r", devRepo: "o/d", branch: "b", task: "t", gpuAll: "0,1"}
	plan := newDevPlan("o/r", "s", []DevModule{{Name: "A.lean", Hash: "h", Bytes: 10}}, [][]string{{"A.lean"}}, "p")
	r.plan = plan
	unit := plan.Leaves()[0]
	machine := DevFailure{Kind: GateReferenceImplMissing, Text: "integration_reference_impl_missing: Qwen3_5MoeForCausalLM … requires the PyTorch library", BaseRef: "b"}
	p := r.unitPrompt(unit, 0, 1, 2, machine, "b")
	if strings.Contains(p, "requires the PyTorch library") || strings.Contains(p, GateReferenceImplMissing) {
		t.Fatalf("the machine's gate reached the conversation:\n%s", p)
	}
	own := DevFailure{Kind: "integration_e2e_unmeasured", Text: "integration_e2e_unmeasured: the runner printed no ids", BaseRef: "b"}
	if p := r.unitPrompt(unit, 0, 1, 2, own, "b"); !strings.Contains(p, "the runner printed no ids") {
		t.Fatal("the unit's own failure was not given")
	}
}
