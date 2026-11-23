package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 11/23: generate_cli.cpp ran on a synthetic backend, a 56-line shim was the one source naming
// safetensors, and no source named the attention's or the experts' tensors — measured, not read
// by hand
func TestThePortsTreeIsMeasuredTowardTheModel(t *testing.T) {
	tree := t.TempDir()
	put := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(tree, p)), 0o755)
		os.WriteFile(filepath.Join(tree, p), []byte(s), 0o644)
	}
	put("src/driver/generate_cli.cpp", "// generate_cli.cpp - runs the decode loop on the deterministic synthetic backend\nint main(int argc, char** argv) { return 0; }\n")
	put("src/baloon_e2e_shim.cu", "// shim: reads one tensor of model.safetensors\nconst char* k = \"embed_tokens\"; // language_model.embed_tokens\n")
	put("src/kernels/gdn.cu", "// gdn\n// linear_attn.in_proj_qkvz\n")
	put("pytools/e2e_infer.py", "self_attn.q_proj experts.gate_up_proj int main(")
	put(".einsteinOS/theory.md", "int main( self_attn.q_proj")
	ms := ModelShape{Tensors: []ModelTensor{
		{Name: "model.language_model.embed_tokens.weight", Dtype: "BF16", Shape: []int64{248320, 2048}},
		{Name: "model.language_model.layers.0.linear_attn.in_proj_qkvz.weight", Dtype: "BF16", Shape: []int64{12288, 2048}},
		{Name: "model.language_model.layers.3.self_attn.q_proj.weight", Dtype: "BF16", Shape: []int64{8192, 2048}},
		{Name: "model.language_model.layers.0.mlp.experts.gate_up_proj", Dtype: "BF16", Shape: []int64{256, 1024, 2048}},
	}}
	inv := measurePortInventory(tree, ms)
	if len(inv.Programs) != 1 || !strings.HasPrefix(inv.Programs[0], "src/driver/generate_cli.cpp — generate_cli.cpp - runs the decode loop on the deterministic synthetic backend") {
		t.Fatalf("programs %v", inv.Programs)
	}
	if strings.Join(inv.Readers, " ") != "src/baloon_e2e_shim.cu" {
		t.Fatalf("readers %v", inv.Readers)
	}
	if strings.Join(inv.Unnamed, " ") != "model.language_model.layers.{i}.mlp.experts.gate_up_proj model.language_model.layers.{i}.self_attn.q_proj.weight" {
		t.Fatalf("unnamed %v (python and the kernel's own files are not the port's sources)", inv.Unnamed)
	}
	blk := inv.Block(len(ms.TensorGroups()))
	for _, w := range []string{"### 1 program(s)", "### 1 source(s) naming .safetensors", "### 2 of the model's 4 tensor groups are named in some source"} {
		if !strings.Contains(blk, w) {
			t.Fatalf("missing %q in\n%s", w, blk)
		}
	}
}

// 11/23 the inventory reached the planner and not the unit that writes the code: the block the
// unit is given holds it, with or without an answer of the planner's
func TestTheUnitIsGivenWhereThePortsTreeStands(t *testing.T) {
	inv := "[THE PORT'S TREE — measured]\n  1 program(s)\n"
	p := IntegrationModelPlan{Inventory: inv}
	if p.Block() != inv {
		t.Fatalf("without an answer the inventory is not given: %q", p.Block())
	}
	p.Ops = []IntegrationOp{{Group: "a", Unit: "u1", Kernel: "k"}}
	if got := p.Block(); !strings.HasPrefix(got, inv) || !strings.Contains(got, "  a: u1 k\n") {
		t.Fatalf("%s", got)
	}
	if (IntegrationModelPlan{}).Block() != "" {
		t.Fatal("nothing measured and nothing answered should insert nothing")
	}
}

// 11/23 15:45:59 the answer read op 8, missing 59 where at 01:53 it read op 78, missing 21: the
// groups it moves between a unit's kernel and the integration unit's own code are named
func TestTheGroupsANewAnswerMovesAreNamed(t *testing.T) {
	prev := IntegrationModelPlan{Ops: []IntegrationOp{
		{Group: "model.language_model.embed_tokens.weight", Unit: "u7991572426", Kernel: "embedding_gather_kernel"},
		{Group: "lm_head.weight", Unit: "pcd027bc02f", Kernel: "bf16_gemv_warp_kernel"},
		{Group: "model.visual.patch_embed.proj.weight", What: "3-D conv"},
	}}
	now := IntegrationModelPlan{Ops: []IntegrationOp{
		{Group: "model.language_model.embed_tokens.weight", What: "embedding gather"},
		{Group: "lm_head.weight", Unit: "pcd027bc02f", Kernel: "bf16_f32_mmvf_kernel"},
		{Group: "model.visual.patch_embed.proj.weight", Unit: "pcd027bc02f", Kernel: "x"},
	}}
	got := answerMoves(prev, now)
	if got != "1 from op to missing (model.language_model.embed_tokens.weight); 1 from missing to op (model.visual.patch_embed.proj.weight)" {
		t.Fatalf("%s", got)
	}
	if answerMoves(now, now) != "" {
		t.Fatal("the same answer moves something")
	}
}
