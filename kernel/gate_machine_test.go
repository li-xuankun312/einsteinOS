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
	if k, _ := noToolchainGate(NoToolchainBuild{Err: "unshare -m: Operation not permitted"}); k != GateToolchainDependenceUnmeasured {
		t.Fatalf("%q", k)
	}
}
