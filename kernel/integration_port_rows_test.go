package kernel

import (
	"strings"
	"testing"
)

// 11/23 16:29:13 kernel_unported held the kernels the planner's missing lines had given the
// integration unit, and at 16:43 its port map named the reference's test helpers (check,
// s2_weight, sigmoid_f) as their counterparts: such a row does not count; a missing:<group> row
// for a group the planner's answer gives the unit does, as does a reference kernel another unit
// ports as a kernel
func TestTheIntegrationUnitsPortRowsAreChecked(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/pcd027bc02f.tsv": "src/kernels/cuda/elementwise.cu\trms_norm_kernel\tsrc/kernels/gpu/ew_ops.cu\trms_norm_weighted_kernel\n",
		"src/kernels/gpu/ew_ops.cu":        "__global__ void rms_norm_weighted_kernel(float* x) {}\n",
		".einsteinOS/port/uint.tsv": "src/kernels/gdn_parity.cpp\tcheck\tsrc/kernels/gpu/gdn_layer.cu\tgdn_ssm_norm_kernel\n" +
			"missing:model.language_model.layers.{i}.linear_attn.conv1d.weight\t-\tsrc/kernels/gpu/gdn_conv.cu\tgdn_bf16_conv_step_kernel\n" +
			"missing:model.visual.patch_embed.proj.weight\t-\tsrc/kernels/gpu/vis.cu\tpatch_kernel\n" +
			"src/kernels/cuda/elementwise.cu\trms_norm_kernel\tsrc/kernels/gpu/rms_bf16.cu\trms_norm_bf16w_inplace_kernel\n",
	})
	integrationAnswersMu.Lock()
	integrationAnswers["uint"] = IntegrationModelPlan{Ops: []IntegrationOp{{Group: "model.language_model.layers.{i}.linear_attn.conv1d.weight", What: "depthwise conv1d"}}}
	integrationAnswersMu.Unlock()
	defer func() { integrationAnswersMu.Lock(); delete(integrationAnswers, "uint"); integrationAnswersMu.Unlock() }()
	files, refused := integrationPortFiles(ws, "uint", readPortMap(ws, "uint"))
	if !files["src/kernels/gpu/gdn_conv.cu"] || !files["src/kernels/gpu/rms_bf16.cu"] || files["src/kernels/gpu/gdn_layer.cu"] || files["src/kernels/gpu/vis.cu"] {
		t.Fatalf("files %v", files)
	}
	all := strings.Join(refused, "\n")
	if len(refused) != 2 || !strings.Contains(all, "check → src/kernels/gpu/gdn_layer.cu gdn_ssm_norm_kernel (no unit ports check as a kernel)") || !strings.Contains(all, "missing:model.visual.patch_embed.proj.weight → src/kernels/gpu/vis.cu patch_kernel (no missing line") {
		t.Fatalf("%q", refused)
	}
	in := GateInput{UnitID: "uint", PortFiles: files, PortFilesRefused: refused, Kernels: map[string][]string{"src/kernels/gpu/gdn_layer.cu": {"gdn_ssm_norm_kernel"}}}
	kinds, texts := unitGatesAll(in)
	if len(kinds) == 0 || kinds[0] != "kernel_unported" || !strings.Contains(texts[0], "2 row(s) of the port map do not count") {
		t.Fatalf("%v %v", kinds, texts)
	}
}
