package kernel

import "testing"

// A failure that measured the machine ends the conversation (no conversation can change it)
// and is the kind of failure the run stops on; a unit's own failure is sent back.
func TestAMachineGateEndsTheConversationAndStopsTheRun(t *testing.T) {
	for _, k := range []string{GateToolchainMissing, GateToolchainDependenceUnmeasured} {
		if !gateIsMachine(k) || !preverifyMayEnd(false, k) {
			t.Errorf("%s is the machine's", k)
		}
	}
	for _, k := range []string{"gpu_code_missing", "build_passes_without_toolchain", "gpu_kernel_not_launched", ""} {
		if gateIsMachine(k) {
			t.Errorf("%s is the unit's", k)
		}
	}
	if preverifyMayEnd(false, "gpu_kernel_not_launched") || !preverifyMayEnd(true, "") {
		t.Fatal("the unit's own result decides otherwise")
	}
	// a machine failure is recorded but is not one of the unit's fix attempts
	p := &DevPlan{}
	if n := p.Fail("u5", DevFailure{Kind: GateToolchainMissing}); n != 0 {
		t.Fatalf("machine failure counted: %d", n)
	}
	if n := p.Fail("u5", DevFailure{Kind: "gpu_kernel_not_launched"}); n != 1 || len(p.FailHistory["u5"]) != 2 {
		t.Fatalf("unit failure: %d, history %v", n, p.FailHistory["u5"])
	}
}
