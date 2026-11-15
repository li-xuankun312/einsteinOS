package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A passed unit whose port map does not cover what the reference requires as measured now is
// reopened when the run starts; one that does, and one that has not passed, are left alone.
// 11/15 08:59 the run restarted on a kernel that measures __launch_bounds__ kernels by their
// names: ued2beb8a35 owned 162 definitions instead of 158, and was not looked at again.
func TestPassedUnitsAreHeldToTheReferenceAgain(t *testing.T) {
	tree := t.TempDir()
	put := func(f, body string) {
		os.MkdirAll(filepath.Dir(filepath.Join(tree, f)), 0o755)
		os.WriteFile(filepath.Join(tree, f), []byte(body), 0o644)
	}
	put(PortMapPath("ued2beb8a35"), "src/kernels/cuda/fused_gdn.cu\tgdn_step_kernel\tsrc/kernels/gpu/gdn.cu\tgdn_step_kernel\n")
	put("src/kernels/gpu/gdn.cu", "__global__ void gdn_step_kernel(float* s) { s[0] = 1; }\n")
	put(PortMapPath("u739c93b0c3"), "src/kernels/cuda/rope.cu\trope_kernel\tsrc/kernels/gpu/rope.cu\trope_kernel\n")
	put("src/kernels/gpu/rope.cu", "__global__ void rope_kernel(float* x) { x[0] = 2; }\n")
	plan := &DevPlan{PortPass: map[string]bool{"ued2beb8a35": true, "u739c93b0c3": true},
		PortUnits: []DevPlanNode{
			{ID: "ued2beb8a35", Sources: []string{"src/kernels/cuda/fused_gdn.cu"}},
			{ID: "u739c93b0c3", Sources: []string{"src/kernels/cuda/rope.cu"}},
			{ID: "p4da61fbf21", Sources: []string{"src/kernels/cuda/native_rope.cu"}},
		}}
	req := map[string][]RefSymbol{
		// measured now: a second kernel the unit's file declares with __launch_bounds__
		"ued2beb8a35": {{File: "src/kernels/cuda/fused_gdn.cu", Kind: "kernel", Name: "gdn_step_kernel"},
			{File: "src/kernels/cuda/fused_gdn.cu", Kind: "kernel", Name: "gdn_chunk_kernel"}},
		"u739c93b0c3": {{File: "src/kernels/cuda/rope.cu", Kind: "kernel", Name: "rope_kernel"}},
		"p4da61fbf21": {{File: "src/kernels/cuda/native_rope.cu", Kind: "kernel", Name: "native_rope_kernel"}},
	}
	got := portRecheck(plan, tree, func(n DevPlanNode) UnitRequirements { return UnitRequirements{Symbols: req[n.ID]} })
	if strings.Join(got, " ") != "ued2beb8a35" {
		t.Fatalf("reopened: %v", got)
	}
	f := plan.Reopen["ued2beb8a35"]
	if f.Kind != "port_incomplete" || !strings.Contains(f.Text, "gdn_chunk_kernel") || plan.UnitPass(plan.PortUnits[0]) {
		t.Fatalf("reopen: %+v", f)
	}
}
