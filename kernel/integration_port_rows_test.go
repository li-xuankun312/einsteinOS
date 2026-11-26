package kernel

import (
	"fmt"
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
	if len(refused) != 2 || !strings.Contains(all, "check → src/kernels/gpu/gdn_layer.cu gdn_ssm_norm_kernel (no unit ports check as a kernel; ") || !strings.Contains(all, "missing:model.visual.patch_embed.proj.weight → src/kernels/gpu/vis.cu patch_kernel (no missing line") {
		t.Fatalf("%q", refused)
	}
	in := GateInput{UnitID: "uint", PortFiles: files, PortFilesRefused: refused, Kernels: map[string][]string{"src/kernels/gpu/gdn_layer.cu": {"gdn_ssm_norm_kernel"}}}
	kinds, texts := unitGatesAll(in)
	if len(kinds) == 0 || kinds[0] != "kernel_unported" || !strings.Contains(texts[0], "2 row(s) of the port map do not count") {
		t.Fatalf("%v %v", kinds, texts)
	}
}

// 11/25 06:27:29 nine files' 38 kernels were given as one list cut at 12: each file is given
// with every kernel it defines
func TestTheUnportedKernelsAreGivenWithTheirFiles(t *testing.T) {
	ks := map[string][]string{}
	for i := 0; i < 20; i++ {
		ks["src/a.cu"] = append(ks["src/a.cu"], fmt.Sprintf("a%02d_kernel", i))
	}
	ks["src/b.cu"] = []string{"z_kernel", "b_kernel"}
	in := GateInput{UnitID: "uint", PortFiles: map[string]bool{}, Kernels: ks}
	_, texts := unitGatesAll(in)
	if len(texts) == 0 || !strings.Contains(texts[0], "kernel_unported: 2 file(s) define") || !strings.Contains(texts[0], "a19_kernel)") ||
		!strings.Contains(texts[0], "; src/b.cu (b_kernel z_kernel)") || strings.Contains(texts[0], "more)") {
		t.Fatalf("%v", texts)
	}
}

// 11/25 06:27:29 the nine rows writing into the held files sorted after 184 others and were cut:
// those rows come first, all of them
func TestTheRowsIntoTheHeldFilesComeFirst(t *testing.T) {
	var refused []string
	for i := 0; i < 30; i++ {
		refused = append(refused, fmt.Sprintf("src/a%02d.cpp main → src/a%02d.cpp main (no unit ports main as a kernel)", i, i))
	}
	for i := 0; i < 9; i++ {
		refused = append(refused, fmt.Sprintf("tests/t.cu test_%d → src/k%d.cu k%d_kernel (no unit ports test_%d as a kernel)", i, i, i, i))
	}
	var held []string
	for i := 0; i < 9; i++ {
		held = append(held, fmt.Sprintf("src/k%d.cu", i))
	}
	got := refusedRowsText(refused, held)
	if !strings.HasPrefix(got, "tests/t.cu test_0 → src/k0.cu") || !strings.Contains(got, "src/k8.cu k8_kernel") || !strings.Contains(got, "(+18 more)") {
		t.Fatal(got)
	}
}

// 11/25 06:27:29 165 of the 193 rows that did not count named the same file and function on both
// sides, each given alone with "no unit ports main as a kernel": they are said to be that, in one line
func TestRowsNamingTheSameFileAndFunctionOnBothSidesAreOneLine(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/uint.tsv": "src/core/device_main.cpp\tmain\tsrc/core/device_main.cpp\tmain\n" +
			"src/kernels/x_parity.cpp\tcheck\tsrc/kernels/x_parity.cpp\tcheck\n" +
			"tests/t.cu\ttest_k\tsrc/k.cu\tk_kernel\n",
	})
	_, refused := integrationPortFiles(ws, "uint", readPortMap(ws, "uint"))
	got := refusedRowsText(refused, []string{"src/k.cu"})
	if !strings.HasPrefix(got, "tests/t.cu test_k → src/k.cu k_kernel (no unit ports test_k as a kernel; ") || !strings.Contains(got, "missing:<group>); 2 row(s) name the same file and function in both columns (") {
		t.Fatal(got)
	}
}

// 11/25 06:27:29 kernel_unported asked for missing:<group> rows and named no group: the groups the
// planner's missing lines give the unit are given
func TestKernelUnportedNamesThePlannersMissingGroups(t *testing.T) {
	integrationAnswersMu.Lock()
	integrationAnswers["uint"] = IntegrationModelPlan{Ops: []IntegrationOp{
		{Group: "model.language_model.layers.{i}.mlp.gate.weight", What: "router"},
		{Group: "model.language_model.layers.{i}.input_layernorm.weight", What: "norm"},
		{Group: "model.language_model.embed_tokens.weight", Unit: "u7991572426", What: "embed"},
	}}
	integrationAnswersMu.Unlock()
	defer func() { integrationAnswersMu.Lock(); delete(integrationAnswers, "uint"); integrationAnswersMu.Unlock() }()
	g := integrationMissingGroups("uint")
	if len(g) != 2 || g[0] != "model.language_model.layers.{i}.input_layernorm.weight" {
		t.Fatalf("%v", g)
	}
	in := GateInput{UnitID: "uint", PortFiles: map[string]bool{}, MissingGroups: g, Kernels: map[string][]string{"src/ix_router_top10_wave.cu": {"router_top10_wave_kernel"}}}
	_, texts := unitGatesAll(in)
	if len(texts) == 0 || !strings.Contains(texts[0], "; the planner's missing lines give the integration unit 2 tensor group(s): model.language_model.layers.{i}.input_layernorm.weight model.language_model.layers.{i}.mlp.gate.weight") {
		t.Fatalf("%v", texts)
	}
}

// 11/25 the reference's src/program/generate.cpp had its counterpart in src/driver/generate_cli.cpp
// and the integration unit's e2e.sh ran parity tests and never generate_cli: each reference
// program a port map row gives a counterpart is given with it and whether an e2e.sh names it
func TestTheReferencesProgramsAreGivenWithWhetherE2ERunsThem(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/ub25e8eba7f.tsv": "src/program/generate.cpp\tmain\tsrc/driver/generate_cli.cpp\tmain\n" +
			"src/kernels/gr_parity.cpp\tmain\tsrc/kernels/gr_parity.cpp\tmain\n",
		".einsteinOS/port/u1.tsv":                "src/kernels/gr_parity.cpp\tmain\ttests/gr_parity_port.cpp\tmain\n",
		".einsteinOS/verify/uintegration/e2e.sh": "\"$BUILD/gr_parity_port\" --selftest\n",
	})
	got := strings.Join(refPrograms(ws), "\n")
	want := "src/kernels/gr_parity.cpp main → tests/gr_parity_port.cpp main; .einsteinOS/verify/uintegration/e2e.sh names gr_parity_port\n" +
		"src/program/generate.cpp main → src/driver/generate_cli.cpp main; no e2e.sh of the tree names generate_cli"
	if got != want {
		t.Fatalf("%s", got)
	}
}

// 11/25 12:23 the embed part, told only that no unit ported its test_X names as kernels, wrote
// each file's rows twice, launcher and kernel names, "to cover two possible check formats": a
// refused row is given what column 2 is, with the rows that count for its file
func TestARefusedRowIsGivenTheRowsThatCountForItsFile(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/pcd027bc02f.tsv": "src/kernels/cuda/elementwise.cu\trms_norm_kernel\tsrc/kernels/gpu/ew_ops.cu\trms_norm_weighted_kernel\n",
		"src/kernels/gpu/ew_ops.cu":        "__global__ void rms_norm_weighted_kernel(float* x) {}\n",
		".einsteinOS/port/uint.tsv": "tests/k39_integration/ln_parity.cu\ttest_rms\tsrc/kernels/gpu/ew_ops.cu\trms_norm_weighted\n" +
			"tests/k39_integration/embed_parity.cu\ttest_embed_gather16\tsrc/prefill/kernels.cu\tgather_rows16_kernel\n",
	})
	_, refused := integrationPortFiles(ws, "uint", readPortMap(ws, "uint"))
	all := strings.Join(refused, "\n")
	for _, want := range []string{
		"test_rms → src/kernels/gpu/ew_ops.cu rms_norm_weighted (no unit ports test_rms as a kernel; column 2 is the reference's function another unit's port map ports as a kernel — rows counting for src/kernels/gpu/ew_ops.cu: src/kernels/cuda/elementwise.cu rms_norm_kernel (pcd027bc02f ports it as rms_norm_weighted_kernel))",
		"test_embed_gather16 → src/prefill/kernels.cu gather_rows16_kernel (no unit ports test_embed_gather16 as a kernel; column 2 is the reference's function another unit's port map ports as a kernel, and no unit's map writes a kernel into src/prefill/kernels.cu: a row for it counts with column 1 missing:<group>)",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("lacks %q:\n%s", want, all)
		}
	}
}

// 11/25 23:05:05 220 rows did not count, the conversation having written
// missing:router_top10_kernel, missing:router_top10_wave and two tensor names of the model as
// groups: a refused missing: row is given the groups the planner's own missing lines name
func TestARefusedMissingRowIsGivenThePlannersGroups(t *testing.T) {
	defer delete(integrationAnswers, "uint")
	integrationAnswers["uint"] = IntegrationModelPlan{Ops: []IntegrationOp{
		{Group: "model.language_model.layers.{i}.mlp.experts.gate_up_proj", What: "the MoE experts"},
		{Group: "model.language_model.layers.{i}.linear_attn.conv1d.weight", What: "depthwise conv1d"},
		{Group: "model.language_model.layers.{i}.mlp.router", What: "the router", Unit: "u1"},
	}}
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/uint.tsv": "missing:router_top10_kernel\t-\tsrc/ix_router_top10_wave.cu\trouter_top10_wave_kernel\n" +
			"missing:model.language_model.layers.{i}.mlp.experts.gate_up_proj\t-\tsrc/ix_moe.cu\tmoe_kernel\n",
		"src/ix_router_top10_wave.cu": "__global__ void router_top10_wave_kernel(float* x) {}\n",
		"src/ix_moe.cu":               "__global__ void moe_kernel(float* x) {}\n",
	})
	files, refused := integrationPortFiles(ws, "uint", readPortMap(ws, "uint"))
	if !files["src/ix_moe.cu"] {
		t.Fatalf("a row naming a group of the planner did not count: %v", files)
	}
	all := strings.Join(refused, "\n")
	want := "missing:router_top10_kernel → src/ix_router_top10_wave.cu router_top10_wave_kernel (no missing line of the planner's names this group; its 2 missing line(s) name these group(s): model.language_model.layers.{i}.linear_attn.conv1d.weight model.language_model.layers.{i}.mlp.experts.gate_up_proj)"
	if !strings.Contains(all, want) {
		t.Fatalf("lacks %q:\n%s", want, all)
	}
	if strings.Contains(all, "mlp.router") {
		t.Fatalf("a group a unit holds named as missing:\n%s", all)
	}
}

// 11/25 18:58:58 the conversation wrote the reference's own kernel names in column 2, names no
// map ports as a kernel: a refused row says when another unit's map does hold that reference
// function and what it ports it into
func TestARefusedRowSaysWhereTheReferenceFunctionIsPorted(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/pcd027bc02f.tsv": "src/kernels/cuda/native_moe.cu\tmoe_router_logits_kernel\tsrc/moe_host.cpp\tmoe_router_logits_host\n",
		"src/moe_host.cpp":                 "void moe_router_logits_host(float* x) {}\n",
		".einsteinOS/port/uint.tsv":        "src/kernels/cuda/native_moe.cu\tmoe_router_logits_kernel\tsrc/kernels/gpu/moe_native.cu\tmoe_router_logits_kernel\n",
		"src/kernels/gpu/moe_native.cu":    "__global__ void moe_router_logits_kernel(float* x) {}\n",
	})
	_, refused := integrationPortFiles(ws, "uint", readPortMap(ws, "uint"))
	want := "(pcd027bc02f's map does port it, into src/moe_host.cpp moe_router_logits_host, which defines no __global__ kernel)"
	if all := strings.Join(refused, "\n"); !strings.Contains(all, want) {
		t.Fatalf("lacks %q:\n%s", want, all)
	}
}
