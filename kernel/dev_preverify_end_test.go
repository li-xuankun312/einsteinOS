package kernel

import "testing"

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
