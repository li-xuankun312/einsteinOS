package kernel

import (
	"strings"
	"testing"
)

// A fix attempt that only edits the scripts is judged with the unit's kernels from its earlier
// commits: gpu_code_missing does not fire on a unit whose .cu files are on the base.
func TestGatesJudgeTheUnitsWholeCode(t *testing.T) {
	changed := []string{".einsteinOS/verify/u5/build.sh", ".einsteinOS/verify/u5/test.sh"}
	prior := []string{"src/kernels/gpu/iq_quant.cu", "src/cpu/q2_avx2_rows.cpp", ".einsteinOS/verify/u5/build.sh", "src/old.cu"}
	got := unitFiles(changed, []string{"src/old.cu"}, prior)
	if strings.Join(got, " ") != ".einsteinOS/verify/u5/build.sh .einsteinOS/verify/u5/test.sh src/kernels/gpu/iq_quant.cu src/cpu/q2_avx2_rows.cpp" {
		t.Fatalf("%v", got)
	}
	req := UnitRequirements{GPUSources: []string{"src/kernels/cuda/iq_kernels.cu"}}
	if _, txt := unitGates(GateInput{Req: req, Changed: changed}); !strings.Contains(txt, "gpu_code_missing") {
		t.Fatalf("this attempt's edit alone has no .cu: %s", txt)
	}
	if _, txt := unitGates(GateInput{Req: req, Changed: got}); strings.Contains(txt, "gpu_code_missing") {
		t.Fatalf("the unit's code has its .cu: %s", txt)
	}
}
