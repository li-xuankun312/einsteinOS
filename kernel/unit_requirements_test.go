package kernel

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUnitRequirementsAreMeasuredFromTheReference(t *testing.T) {
	bare, sha := strataLikeRef(t)
	old := refBareDirFmt
	t.Cleanup(func() { refBareDirFmt = old })
	refBareDirFmt = filepath.Join(filepath.Dir(bare), "ref.git") + "%.0s%.0s"

	theory := "## FILE: /mnt/user-data/outputs/FusedGdnKernels.lean\n/-!\n# MODULE: fused_gdn_kernels\n" +
		"## Source semantics\n`gdn_step_norm_kernel`: block = head.\n`gdn_conv_l2_kernel`: one block per 128 channels.\n-/\n" +
		"## FILE: /mnt/user-data/outputs/Q2ExpertVnniKernel.lean\n/-!\n# MODULE: q2_expert_vnni_kernel\n" +
		"## Form template identifiers\nrow_dot -> row_dot, act_quant_q8_1 -> act_quant, s2_expert_vnni_q -> expert_forward, CpuFeatures -> cpu_features.\n-/\n" +
		"## FILE: /mnt/user-data/outputs/Q2Avx2Kernel.lean\n/-!\n# MODULE: q2_avx2_kernel\nunpack64 -> unpack_block, row_multi -> row_dot_multi\n-/\n" +
		"## FILE: /mnt/user-data/outputs/F16Conversion.lean\n/-!\n# MODULE: f16_conversion\nnothing the reference names\n-/\n"
	req := measureUnitRequirements("Niko1221/Strata", sha, theory)

	if !reflect.DeepEqual(req.Sources, []string{"src/kernels/cpu/expert.cpp", "src/kernels/cpu/q2_avx2.cpp", "src/kernels/cuda/fused_gdn.cu"}) {
		t.Fatalf("sources %v", req.Sources)
	}
	if !req.GPU() || !reflect.DeepEqual(req.GPUSources, []string{"src/kernels/cuda/fused_gdn.cu"}) {
		t.Fatalf("gpu %v", req.GPUSources)
	}
	if !reflect.DeepEqual(req.ISAFlags, []string{"-mavx2", "-mavx512f", "-mavx512vnni", "-mf16c", "-mfma"}) {
		t.Fatalf("isa %v", req.ISAFlags)
	}
	if !reflect.DeepEqual(req.Unmeasured, []string{"F16Conversion"}) {
		t.Fatalf("unmeasured %v (a module with no identifier found is reported, not guessed)", req.Unmeasured)
	}
	// the evidence says why: the test file did not count, the 11-file identifier did not count
	if ev := req.Evidence["src/kernels/cuda/fused_gdn.cu"]; !reflect.DeepEqual(ev, []string{"gdn_conv_l2_kernel", "gdn_step_norm_kernel"}) {
		t.Fatalf("evidence %v", ev)
	}
	for _, s := range req.Sources {
		if strings.Contains(s, "_test") || strings.Contains(s, "noise") {
			t.Fatalf("%s should not be a source", s)
		}
	}
	// a CPU-only unit is not a GPU unit
	cpu := "## FILE: x/Q2Avx2Kernel.lean\n/-!\n# MODULE: q2_avx2_kernel\nunpack64 -> unpack_block\n-/\n"
	if r := measureUnitRequirements("Niko1221/Strata", sha, cpu); r.GPU() || !reflect.DeepEqual(r.ISAFlags, []string{"-mavx2", "-mf16c", "-mfma"}) {
		t.Fatalf("cpu unit: gpu=%v isa=%v", r.GPU(), r.ISAFlags)
	}
}

func TestModuleSourceIdentsFromARealSection(t *testing.T) {
	sec := "/-!\n# MODULE: q2_avx2_kernel\nThe AVX2-only Q2_0 row dot.\n## Source semantics\n* `maddubs` of codes with int8 activations.\n" +
		"## Form template identifiers\nunpack64 -> unpack_block, row_multi -> row_dot_multi, rows -> rows, act_quant_q8_1_avx2 -> act_quant.\n-/\n" +
		"theorem row_multi_ok : True := trivial\n"
	got := moduleSourceIdents(sec)
	for _, want := range []string{"q2_avx2_kernel", "unpack_block", "row_dot_multi", "row_multi", "act_quant_q8_1_avx2", "act_quant"} {
		found := false
		for _, g := range got {
			found = found || g == want
		}
		if !found {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	for _, g := range got {
		if g == "rows" || g == "maddubs" || g == "theorem" {
			t.Errorf("%q is not a C-like source identifier", g)
		}
	}
}
