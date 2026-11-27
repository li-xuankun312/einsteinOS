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
	if len(kinds) == 0 || kinds[0] != "kernel_unported" || !strings.Contains(texts[0], "1 row(s) of the port map do not count") || !strings.Contains(texts[0], "1 other row(s) of the map write into none of these files") {
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
	if !strings.HasPrefix(got, "tests/t.cu test_0 → src/k0.cu") || !strings.Contains(got, "src/k8.cu k8_kernel") || !strings.HasSuffix(got, "; 30 other row(s) of the map write into none of these files, and this gate does not judge them") || strings.Contains(got, "src/a00.cpp") {
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
	if !strings.HasPrefix(got, "tests/t.cu test_k → src/k.cu k_kernel (no unit ports test_k as a kernel; ") || !strings.Contains(got, "missing:<group>, where <group>") || !strings.HasSuffix(got, "; 2 other row(s) of the map write into none of these files, and this gate does not judge them") {
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
		"test_rms → src/kernels/gpu/ew_ops.cu rms_norm_weighted (no unit ports test_rms as a kernel; column 2 is the reference's function another unit's port map ports as a kernel — the reference kernels the units ported into src/kernels/gpu/ew_ops.cu are given after the rows)",
		"test_embed_gather16 → src/prefill/kernels.cu gather_rows16_kernel (no unit ports test_embed_gather16 as a kernel; column 2 is the reference's function another unit's port map ports as a kernel, and no unit's map writes a kernel into src/prefill/kernels.cu: a row for it counts with column 1 missing:<group>, where <group>",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("lacks %q:\n%s", want, all)
		}
	}
	// 11/26 17:3x each of 28 refused rows carried its file's list: the gate gives it once
	in := GateInput{UnitID: "uint", PortFiles: map[string]bool{}, PortFilesRefused: refused, PortedInto: integrationPortedInto(ws, "uint"),
		Kernels: map[string][]string{"src/kernels/gpu/ew_ops.cu": {"rms_norm_weighted"}}}
	integrationAnswers["uint"] = IntegrationModelPlan{Ops: []IntegrationOp{{Group: "g", Unit: "pcd027bc02f", Kernel: "rms_norm_weighted_kernel"}}}
	defer delete(integrationAnswers, "uint")
	in.PortedInto = integrationPortedInto(ws, "uint")
	_, texts := unitGatesAll(in)
	w := "; the reference kernels the units ported into these files: src/kernels/gpu/ew_ops.cu: src/kernels/cuda/elementwise.cu rms_norm_kernel (pcd027bc02f ports it as rms_norm_weighted_kernel)"
	if len(texts) == 0 || strings.Count(texts[0], "src/kernels/cuda/elementwise.cu rms_norm_kernel") != 1 || !strings.Contains(texts[0], w) {
		t.Fatalf("%v", texts)
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
	want := "missing:router_top10_kernel → src/ix_router_top10_wave.cu router_top10_wave_kernel (no missing line of the planner's names this group; its 2 missing line(s) name the groups given after the rows)"
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

// 11/26 04:15:56 the same rows stood under port_ref_file_unknown and under port_extra, each
// row named twice: a row whose column 1 is no reference file is left to port_ref_file_unknown
func TestARowWhoseColumn1IsNoReferenceFileIsNamedOnce(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/uint.tsv": "tests/k39_integration/elementwise_parity.cu\ttest_rms\tsrc/kernels/gpu/ew_ops.cu\trms_kernel\n" +
			"src/kernels/cuda/elementwise.cu\tnot_a_symbol\tsrc/kernels/gpu/ew_ops.cu\tx_kernel\n",
	})
	syms := []RefSymbol{{File: "src/kernels/cuda/elementwise.cu", Name: "rms_norm_kernel"}}
	extra := portExtra(ws, "uint", syms)
	all := strings.Join(extra, "\n")
	if strings.Contains(all, "elementwise_parity.cu") {
		t.Fatalf("a row port_ref_file_unknown names was named again:\n%s", all)
	}
	if !strings.Contains(all, "not_a_symbol") {
		t.Fatalf("a row of a reference file naming no definition was not named:\n%s", all)
	}
	if un := portRefFiles(ws, "uint", syms); len(un) != 1 || !strings.Contains(un[0], "elementwise_parity.cu") {
		t.Fatalf("%q", un)
	}
}

// 11/26 04:27:08 216 rows did not count and nearly all took this branch, where the row is told
// to use column 1 missing:<group>: the groups the planner's missing lines name are given here too
func TestTheBranchAskingForAMissingRowNamesTheGroups(t *testing.T) {
	defer delete(integrationAnswers, "uint")
	integrationAnswers["uint"] = IntegrationModelPlan{Ops: []IntegrationOp{
		{Group: "model.language_model.layers.{i}.linear_attn.conv1d.weight", What: "depthwise conv1d"},
	}}
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/uint.tsv":         "src/kernels/cuda/native_gdn.cu\tgdn_bf16_conv_step_kernel\tsrc/kernels/gpu/gdn_bf16_layer.cu\tgdn_bf16_conv_step_kernel\n",
		"src/kernels/gpu/gdn_bf16_layer.cu": "__global__ void gdn_bf16_conv_step_kernel(float* x) {}\n",
	})
	_, refused := integrationPortFiles(ws, "uint", readPortMap(ws, "uint"))
	want := "a row for it counts with column 1 missing:<group>, where <group> is one of the 1 the planner's missing lines name, given after the rows"
	if all := strings.Join(refused, "\n"); !strings.Contains(all, want) {
		t.Fatalf("lacks %q:\n%s", want, all)
	}
}

// 11/26 05:43:17 a command of the moe part wrote a planner group in column 1 without its
// missing: prefix, and the row was told only that it is no reference file: the prefix is named
func TestAGroupWrittenWithoutItsPrefixIsTold(t *testing.T) {
	defer delete(integrationAnswers, "uint")
	integrationAnswers["uint"] = IntegrationModelPlan{Ops: []IntegrationOp{
		{Group: "model.language_model.layers.{i}.mlp.gate.weight", What: "the router"},
	}}
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/uint.tsv": "model.language_model.layers.{i}.mlp.gate.weight\trouter_top10\tsrc/ix_router_top10_wave.cu\trouter_top10_wave_kernel\n" +
			"include/baloon/kg_kernels.hpp\tf\tsrc/a.cu\tg\n",
	})
	got := strings.Join(portRefFiles(ws, "uint", []RefSymbol{{File: "src/kernels/cuda/router_top10.cu", Name: "x"}}), "\n")
	if !strings.Contains(got, "— it is a group of the planner's missing lines, which column 1 holds as missing:model.language_model.layers.{i}.mlp.gate.weight") {
		t.Fatalf("%s", got)
	}
	if n := strings.Count(got, "it is a group of the planner's"); n != 1 {
		t.Fatalf("a file that is no group was told so too:\n%s", got)
	}
}

// 11/26 09:49:16 each of 168 refused rows carried the same list of the planner's groups and the
// text came to 14355 bytes, of which the conversation was shown 8000: the rows name no group and
// the gate gives them once
func TestTheGroupsAreGivenOnceNotWithEachRow(t *testing.T) {
	integrationAnswers["uint"] = IntegrationModelPlan{Ops: []IntegrationOp{
		{Group: "model.visual.blocks.{i}.attn.proj.bias", What: "vit"},
		{Group: "model.visual.blocks.{i}.attn.qkv.bias", What: "vit"},
	}}
	t.Cleanup(func() { delete(integrationAnswers, "uint") })
	ws := t.TempDir()
	var rows strings.Builder
	for _, f := range []string{"check", "main", "rel_l1"} {
		rows.WriteString("src/kernels/shared_expert_parity.cpp\t" + f + "\tsrc/ix_shared_expert.cu\tswiglu_kernel\n")
	}
	putTree(t, ws, map[string]string{
		".einsteinOS/port/uint.tsv": rows.String(),
		"src/ix_shared_expert.cu":   "__global__ void swiglu_kernel(float* x) {}\n",
	})
	files, refused := integrationPortFiles(ws, "uint", readPortMap(ws, "uint"))
	in := GateInput{UnitID: "uint", PortFiles: files, PortFilesRefused: refused, MissingGroups: integrationMissingGroups("uint"),
		Kernels: map[string][]string{"src/ix_shared_expert.cu": {"swiglu_kernel"}}}
	_, texts := unitGatesAll(in)
	if len(texts) == 0 {
		t.Fatal("no gate")
	}
	if n := strings.Count(texts[0], "model.visual.blocks.{i}.attn.proj.bias"); n != 1 {
		t.Fatalf("the group is given %d times:\n%s", n, texts[0])
	}
}

// 11/26 07:28 to 11:34 the planner's 21 missing groups were all model.visual.*, off the text
// path, and the text gave them as the groups a GDN or router kernel's row may name
func TestTheMissingGroupsOffTheTextPathAreSaidToBe(t *testing.T) {
	groups := []string{"model.language_model.layers.{i}.mlp.gate.weight", "model.visual.blocks.{i}.attn.qkv.weight", "model.visual.pos_embed.weight"}
	in := GateInput{UnitID: "uint", PortFiles: map[string]bool{}, MissingGroups: groups,
		MissingOff: map[string]bool{"model.visual.blocks.{i}.attn.qkv.weight": true, "model.visual.pos_embed.weight": true},
		Kernels:    map[string][]string{"src/ix_router_top10_wave.cu": {"router_top10_wave_kernel"}}}
	_, texts := unitGatesAll(in)
	want := "; the planner's missing lines give the integration unit 3 tensor group(s), 1 of them on the model's text path: model.language_model.layers.{i}.mlp.gate.weight, and 2 off it, where no part is started: model.visual.blocks.{i}.attn.qkv.weight model.visual.pos_embed.weight"
	if len(texts) == 0 || !strings.Contains(texts[0], want) {
		t.Fatalf("lacks %q:\n%v", want, texts)
	}
	in.MissingGroups, in.MissingOff = groups[1:], map[string]bool{groups[1]: true, groups[2]: true}
	_, texts = unitGatesAll(in)
	if !strings.Contains(texts[0], "0 of them on the model's text path, and 2 off it") {
		t.Fatalf("none on the path is not said: %v", texts)
	}
}

// 11/26 07:28:43 the conversation wrote the row kernel_unported asked for — column 2 the reference
// kernel a unit ported, column 1 its reference file — and port_ref_file_unknown refused it,
// column 1 being no file of the integration unit's reference
func TestARowNamingAKernelAUnitPortedPassesEveryGate(t *testing.T) {
	integrationAnswers["uint"] = IntegrationModelPlan{Ops: []IntegrationOp{{Group: "model.language_model.layers.{i}.linear_attn.conv1d.weight", Unit: "pA", Kernel: "gdn_conv_kernel"}}}
	t.Cleanup(func() { delete(integrationAnswers, "uint") })
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/pA.tsv":           "src/kernels/cuda/gdn.cu\tgdn_conv_kernel\tsrc/kernels/gpu/gdn.cu\tgdn_conv_kernel\n",
		"src/kernels/gpu/gdn.cu":            "__global__ void gdn_conv_kernel(float* x) {}\n",
		".einsteinOS/port/uint.tsv":         "src/kernels/gdn_parity.cpp\tcheck\ttests/k39/gdn_parity.cu\tcheck\nsrc/kernels/cuda/gdn.cu\tgdn_conv_kernel\tsrc/kernels/gpu/gdn_bf16_layer.cu\tgdn_bf16_conv_step_kernel\n",
		"src/kernels/gpu/gdn_bf16_layer.cu": "__global__ void gdn_bf16_conv_step_kernel(float* x) {}\n",
		"tests/k39/gdn_parity.cu":           "int check() { return 0; }\n",
	})
	syms := []RefSymbol{{File: "src/kernels/gdn_parity.cpp", Name: "check", Kind: "func"}}
	if r := portRefFiles(ws, "uint", syms); len(r) != 0 {
		t.Fatalf("port_ref_file_unknown: %v", r)
	}
	if r := portExtra(ws, "uint", syms); len(r) != 0 {
		t.Fatalf("port_extra: %v", r)
	}
	if files, _ := integrationPortFiles(ws, "uint", readPortMap(ws, "uint")); !files["src/kernels/gpu/gdn_bf16_layer.cu"] {
		t.Fatalf("kernel_unported does not count it: %v", files)
	}
	// a unit that is not the integration unit is judged as before
	delete(integrationAnswers, "uint")
	if r := portRefFiles(ws, "uint", syms); len(r) != 1 {
		t.Fatalf("outside the integration unit: %v", r)
	}
}

// 11/26 07:28:43 a row naming router_top10_kernel from a file of the port was refused with nothing
// saying from which reference file pcd027bc02f ports it
func TestARefusedRowSaysWhereAUnitPortsItsName(t *testing.T) {
	integrationAnswers["uint"] = IntegrationModelPlan{Ops: []IntegrationOp{{Group: "g", Unit: "pA", Kernel: "router_top10_kernel"}}}
	t.Cleanup(func() { delete(integrationAnswers, "uint") })
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/pA.tsv":   "src/kernels/cuda/router_top10.cu\trouter_top10_kernel\tsrc/kernels/gpu/router.cu\trouter_top10_kernel\n",
		"src/kernels/gpu/router.cu": "__global__ void router_top10_kernel(float* x) {}\n",
		".einsteinOS/port/uint.tsv": "tests/k39/moe_parity.cu\trouter_top10_kernel\tsrc/kernels/gpu/moe_native.cu\tmoe_router_logits_kernel\n",
	})
	syms := []RefSymbol{{File: "src/kernels/gdn_parity.cpp", Name: "check", Kind: "func"}}
	r := portRefFiles(ws, "uint", syms)
	want := " — router_top10_kernel is a reference kernel a unit ported: src/kernels/cuda/router_top10.cu router_top10_kernel, by pA router_top10_kernel in src/kernels/gpu/router.cu"
	if len(r) != 1 || !strings.HasSuffix(r[0], want) {
		t.Fatalf("lacks %q: %v", want, r)
	}
}

// 11/26 07:12 the parts wrote kernels of their own for groups the planner's op lines gave to
// pcd027bc02f's kernels, and kernel_unported named none of those kernels
func TestKernelUnportedNamesTheKernelsTheOpLinesGive(t *testing.T) {
	integrationAnswers["uint"] = IntegrationModelPlan{Ops: []IntegrationOp{
		{Group: "model.language_model.layers.{i}.linear_attn.conv1d.weight", Unit: "pA", Kernel: "gdn_conv_kernel"},
		{Group: "mtp.layers.{i}.mlp.gate.weight", Unit: "pA", Kernel: "router_top10_kernel"},
		{Group: "model.visual.pos_embed.weight"},
	}, GroupsOff: []string{"model.visual.pos_embed.weight", "mtp.layers.{i}.mlp.gate.weight"}}
	t.Cleanup(func() { delete(integrationAnswers, "uint") })
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/pA.tsv": "src/kernels/cuda/gdn.cu\tgdn_conv_kernel\tsrc/kernels/gpu/gdn.cu\tgdn_conv_kernel\n",
	})
	got := integrationOpKernels(ws, "uint")
	want := []string{
		"pA gdn_conv_kernel (src/kernels/cuda/gdn.cu gdn_conv_kernel → src/kernels/gpu/gdn.cu): model.language_model.layers.{i}.linear_attn.conv1d.weight",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
	got = append(got, "pA router_top10_kernel (no row of its map names it): model.language_model.layers.{i}.mlp.gate.weight")
	in := GateInput{UnitID: "uint", PortFiles: map[string]bool{}, OpKernels: got,
		Kernels: map[string][]string{"src/kernels/gpu/gdn_bf16_layer.cu": {"gdn_bf16_conv_step_kernel"}}}
	_, texts := unitGatesAll(in)
	if len(texts) == 0 || !strings.Contains(texts[0], "; the planner's op lines give the model's text path 2 kernel(s) units ported, each with the row of its unit's port map and the groups: pA gdn_conv_kernel") || strings.Contains(texts[0], "mtp.") {
		t.Fatalf("not in the gate: %v", texts)
	}
}

// 11/26 07:28 to 11:34 the integration unit changed src/kernels/gpu/ew_ops.cu, where other units
// had ported their kernels, and kernel_unported held all fourteen kernels of the file
func TestKernelUnportedHoldsOnlyTheKernelsNoUnitPorted(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/pA.tsv":   "src/kernels/cuda/elementwise.cu\trms_norm_weighted_kernel\tsrc/kernels/gpu/ew_ops.cu\trms_norm_weighted_kernel\n",
		"src/kernels/gpu/ew_ops.cu": "__global__ void rms_norm_weighted_kernel(float* x) {}\n__global__ void silu_kernel(float* x) {}\n",
	})
	other := integrationOtherPorted(ws, "uint")
	in := GateInput{UnitID: "uint", PortFiles: map[string]bool{}, OtherPorted: other,
		Kernels: map[string][]string{"src/kernels/gpu/ew_ops.cu": {"rms_norm_weighted_kernel", "silu_kernel"}}}
	kinds, texts := unitGatesAll(in)
	if len(kinds) == 0 || kinds[0] != "kernel_unported" || !strings.Contains(texts[0], "src/kernels/gpu/ew_ops.cu (silu_kernel)") {
		t.Fatalf("the integration unit's own kernel is not held alone: %v %v", kinds, texts)
	}
	in.Kernels = map[string][]string{"src/kernels/gpu/ew_ops.cu": {"rms_norm_weighted_kernel"}}
	if kinds, _ := unitGatesAll(in); len(kinds) > 0 && kinds[0] == "kernel_unported" {
		t.Fatal("a kernel another unit ported is held")
	}
}

// 11/26 09:49:16 168 rows refused by kernel_unported were each port_incomplete's counterpart of a
// reference definition; the conversation removed them and port_incomplete failed
func TestARefusedRowPortIncompleteCountsIsSaidToBe(t *testing.T) {
	refused := []string{
		"src/kernels/shared_expert_parity.cpp check → src/ix_shared_expert.cu check (no unit ports check as a kernel)",
		"missing:gdn → src/ix_gdn.cu gdn_step_kernel (no missing line of the planner's names this group)",
	}
	got := markPortIncompleteRows(refused, []RefSymbol{{File: "src/kernels/shared_expert_parity.cpp", Name: "check"}})
	if !strings.HasSuffix(got[0], " — port_incomplete counts this row as the counterpart of src/kernels/shared_expert_parity.cpp check") || got[1] != refused[1] {
		t.Fatalf("got %q", got)
	}
}

// 11/26 13:27:10 kernel_unported passed on missing:model.visual.blocks.{i}.attn.qkv.weight for
// router_top10_wave_kernel and rows like it: a missing group off the model's text path counts
// for no kernel
func TestAMissingRowOffTheTextPathDoesNotCount(t *testing.T) {
	integrationAnswers["uint"] = IntegrationModelPlan{Ops: []IntegrationOp{
		{Group: "model.visual.blocks.{i}.attn.qkv.weight", What: "vit"},
		{Group: "model.language_model.layers.{i}.linear_attn.conv1d.weight", What: "conv"},
	}, GroupsOff: []string{"model.visual.blocks.{i}.attn.qkv.weight"}}
	t.Cleanup(func() { delete(integrationAnswers, "uint") })
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/uint.tsv": "missing:model.visual.blocks.{i}.attn.qkv.weight\trouter_top10_wave_kernel\tsrc/ix_router_top10_wave.cu\trouter_top10_wave_kernel\n" +
			"missing:model.language_model.layers.{i}.linear_attn.conv1d.weight\tgdn\tsrc/ix_gdn.cu\tgdn_conv_kernel\n",
	})
	files, refused := integrationPortFiles(ws, "uint", readPortMap(ws, "uint"))
	if files["src/ix_router_top10_wave.cu"] || !files["src/ix_gdn.cu"] {
		t.Fatalf("counted: %v", files)
	}
	if len(refused) != 1 || !strings.Contains(refused[0], "off the model's text path") {
		t.Fatalf("refused: %v", refused)
	}
}

// 11/26 14:05:49 src/ix_moe_combine.cu was told a row counts with column 1 missing:<group>, while
// every one of the planner's missing groups was off the text path, where such a row counts for
// nothing
func TestNoMissingGroupIsOfferedWhenNoneIsOnThePath(t *testing.T) {
	integrationAnswers["uint"] = IntegrationModelPlan{Ops: []IntegrationOp{{Group: "model.visual.pos_embed.weight", What: "vit"}},
		GroupsOff: []string{"model.visual.pos_embed.weight"}}
	t.Cleanup(func() { delete(integrationAnswers, "uint") })
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/uint.tsv": "tests/k39_integration/moe_parity.cu\ttest_ix_moe_combine\tsrc/ix_moe_combine.cu\tmoe_combine_kernel\n",
	})
	_, refused := integrationPortFiles(ws, "uint", readPortMap(ws, "uint"))
	if len(refused) != 1 || strings.Contains(refused[0], "column 1 missing:<group>") || !strings.Contains(refused[0], "none of the 1 groups the planner's missing lines name is on the model's text path") {
		t.Fatalf("got %v", refused)
	}
}

// 11/26 14:19:15 a row writing src/ix_bf16_gemv.cu bf16_gemv_warp_kernel was refused with nothing
// saying that a unit ported a kernel of that name from a reference file of its own
func TestARefusedRowIsGivenTheUnitRowOfItsKernelsName(t *testing.T) {
	integrationAnswers["uint"] = IntegrationModelPlan{Ops: []IntegrationOp{{Group: "g", Unit: "pA", Kernel: "bf16_gemv_warp_kernel"}}}
	t.Cleanup(func() { delete(integrationAnswers, "uint") })
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/pA.tsv":        "src/kernels/cuda/bf16_gemv.cu\tbf16_gemv_warp_kernel\tsrc/kernels/gpu/bf16_native.cu\tbf16_gemv_warp_kernel\n",
		"src/kernels/gpu/bf16_native.cu": "__global__ void bf16_gemv_warp_kernel(float* x) {}\n",
		".einsteinOS/port/uint.tsv":      "tests/k39_integration/qsa_layer_parity.cu\ttest_qsa_bf16_kv_proj\tsrc/ix_bf16_gemv.cu\tbf16_gemv_warp_kernel\n",
	})
	r := portRefFiles(ws, "uint", []RefSymbol{{File: "src/kernels/qsa_parity.cpp", Name: "main"}})
	want := " — bf16_gemv_warp_kernel is the name of a kernel a unit ported: pA src/kernels/cuda/bf16_gemv.cu bf16_gemv_warp_kernel → src/kernels/gpu/bf16_native.cu"
	if len(r) != 1 || !strings.HasSuffix(r[0], want) {
		t.Fatalf("got %v", r)
	}
}

// 11/26 14:37:46 the 150 definitions with no row took 150 lines of which the conversation could be
// shown a dozen: they are grouped by reference file, each with the port's sources of its name
func TestPortIncompleteIsGroupedByReferenceFile(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		"tests/k39_integration/bf16_gemv_parity.cu": "static float rel_l1(float a) { return a; }\nint main() { return 0; }\n",
	})
	p := PortMapPath("uint")
	lines := []string{
		"  src/kernels/bf16_gemv_parity.cpp func check: no line in " + p,
		"  src/kernels/bf16_gemv_parity.cpp func main: no line in " + p,
		"  src/kernels/bf16_gemv_parity.cpp func rel_l1: no line in " + p,
		"  src/kernels/cpu/pool_test.cpp func main: no line in " + p,
		"  src/kernels/gdn_parity.cpp func check → tests/x.cu:check: not defined in that file",
	}
	got := portMissingByFile(ws, "uint", lines)
	want := []string{
		"  src/kernels/bf16_gemv_parity.cpp: 3 definition(s) with no row: check main rel_l1; the port's tests/k39_integration/bf16_gemv_parity.cu, of the same name, defines main rel_l1",
		"  src/kernels/cpu/pool_test.cpp: 1 definition(s) with no row: main; no source of the port has its name",
		"  src/kernels/gdn_parity.cpp func check → tests/x.cu:check: not defined in that file",
	}
	if got != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", got)
	}
	in := GateInput{PortMissing: lines, PortMissingText: got, Req: UnitRequirements{Symbols: make([]RefSymbol, 168)}}
	_, texts := unitGatesAll(in)
	if all := strings.Join(texts, "\n"); !strings.Contains(all, "port_incomplete: 5 of the 168") || !strings.Contains(all, want[0]) {
		t.Fatalf("gate: %s", all)
	}
}

// 11/26 15:07:34 the integration map gave port_incomplete 167 of 168 with check, reference and
// rel_l1 of src/kernels/bf16_gemv_parity.cpp each mapped to the port's main
func TestTheIntegrationUnitsCounterpartKeepsTheReferenceName(t *testing.T) {
	integrationAnswers["uint"] = IntegrationModelPlan{Ops: []IntegrationOp{{Group: "g", Unit: "pA", Kernel: "k"}}}
	t.Cleanup(func() { delete(integrationAnswers, "uint") })
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/uint.tsv": "src/kernels/bf16_gemv_parity.cpp\tcheck\ttests/k39_integration/bf16_gemv_parity.cu\tmain\n" +
			"src/kernels/bf16_gemv_parity.cpp\tmain\ttests/k39_integration/bf16_gemv_parity.cu\tmain\n",
		"tests/k39_integration/bf16_gemv_parity.cu": "int main() { return 0; }\n",
	})
	syms := []RefSymbol{{File: "src/kernels/bf16_gemv_parity.cpp", Name: "check", Kind: "func"}, {File: "src/kernels/bf16_gemv_parity.cpp", Name: "main", Kind: "func"}}
	got := portMissing(ws, "uint", syms)
	want := "  src/kernels/bf16_gemv_parity.cpp check → tests/k39_integration/bf16_gemv_parity.cu:main: the counterpart is named main, the reference's definition check"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %q", got)
	}
	delete(integrationAnswers, "uint")
	if got := portMissing(ws, "uint", syms); len(got) != 0 {
		t.Fatalf("another unit is judged by it: %q", got)
	}
}

// 11/26 15:00:17 to 15:09:12 the conversation removed the 175 rows naming a test file and function
// on both sides — port_incomplete's counterparts — given by kernel_unported among rows that "do not
// count": kernel_unported gives only the rows writing into the files it holds
func TestKernelUnportedGivesOnlyTheRowsIntoTheFilesItHolds(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/uint.tsv": "src/kernels/bf16_gemv_parity.cpp\tcheck\tsrc/kernels/bf16_gemv_parity.cpp\tcheck\n" +
			"tests/t.cu\ttest_k\tsrc/k.cu\tk_kernel\n",
	})
	files, refused := integrationPortFiles(ws, "uint", readPortMap(ws, "uint"))
	in := GateInput{UnitID: "uint", PortFiles: files, PortFilesRefused: refused, Kernels: map[string][]string{"src/k.cu": {"k_kernel"}}}
	_, texts := unitGatesAll(in)
	if len(texts) == 0 || strings.Contains(texts[0], "bf16_gemv_parity") || !strings.Contains(texts[0], "; 1 row(s) of the port map do not count: tests/t.cu test_k → src/k.cu k_kernel") || !strings.Contains(texts[0], "1 other row(s) of the map write into none of these files") {
		t.Fatalf("%v", texts)
	}
}

// 11/26 17:3x a row giving src/kernels/elementwise_parity.cpp check to ew_ops.cu to_bf16_kernel was
// said to be counted by port_incomplete, which refuses it as renamed
func TestARenamedRowIsNotSaidToBeCountedByPortIncomplete(t *testing.T) {
	refused := []string{
		"src/kernels/elementwise_parity.cpp check → src/kernels/gpu/ew_ops.cu to_bf16_kernel (no unit ports check as a kernel)",
		"src/kernels/elementwise_parity.cpp check_k → src/kernels/gpu/ew_ops.cu check_k (no unit ports check_k as a kernel)",
	}
	got := markPortIncompleteRows(refused, []RefSymbol{{File: "src/kernels/elementwise_parity.cpp", Name: "check"}, {File: "src/kernels/elementwise_parity.cpp", Name: "check_k"}})
	if got[0] != refused[0] || !strings.Contains(got[1], "port_incomplete counts this row") {
		t.Fatalf("%q", got)
	}
}

// 11/26 17:3x ew_ops.cu was held for rms_norm_bf16w_inplace_kernel alone, and a counting row for
// another kernel of the file would have let it through: the integration unit's rows count for
// the kernels they name
func TestAnIntegrationRowCountsForTheKernelItNames(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/pA.tsv":   "src/kernels/cuda/elementwise.cu\tadd_kernel\tsrc/kernels/gpu/ew.cu\tadd_kernel\n",
		"src/kernels/gpu/ew.cu":     "__global__ void add_kernel(float* x) {}\n",
		".einsteinOS/port/uint.tsv": "src/kernels/cuda/elementwise.cu\tadd_kernel\tsrc/kernels/gpu/ew_ops.cu\tadd2_kernel\n",
	})
	files, rows, refused := integrationPortRows(ws, "uint", readPortMap(ws, "uint"))
	in := GateInput{UnitID: "uint", PortFiles: files, RowKernels: rows, PortFilesRefused: refused,
		Kernels: map[string][]string{"src/kernels/gpu/ew_ops.cu": {"add2_kernel", "rms_norm_bf16w_inplace_kernel"}}}
	kinds, texts := unitGatesAll(in)
	if len(kinds) == 0 || kinds[0] != "kernel_unported" || !strings.Contains(texts[0], "src/kernels/gpu/ew_ops.cu (rms_norm_bf16w_inplace_kernel)") {
		t.Fatalf("%v %v", kinds, texts)
	}
	in.Kernels = map[string][]string{"src/kernels/gpu/ew_ops.cu": {"add2_kernel"}}
	if kinds, _ := unitGatesAll(in); len(kinds) > 0 && kinds[0] == "kernel_unported" {
		t.Fatal("the kernel its row names is held")
	}
}

// 11/27 03:54:47 thirteen refused rows each carried the same reason: it is given once after them
func TestRowsRefusedForOneReasonAreGivenTheReasonOnce(t *testing.T) {
	why := " (the planner's missing lines give this group, and it is off the model's text path, where no part is started)"
	rows := []string{
		"missing:model.visual.a → src/a.cu k1" + why,
		"x.cpp check → src/a.cu k2 (no unit ports check as a kernel)",
		"missing:model.visual.b → src/b.cu k3" + why,
	}
	got := strings.Join(groupRowsByReason(rows), "; ")
	want := "missing:model.visual.a → src/a.cu k1, missing:model.visual.b → src/b.cu k3" + why + "; x.cpp check → src/a.cu k2 (no unit ports check as a kernel)"
	if got != want {
		t.Fatalf("got\n%s", got)
	}
}

// 11/27 04:58 to 05:07 the integration conversation took 289 rows out of its map and wrote rows of
// gdn_parity.cpp main into the held files, as kernel_unported's first line read — "files the
// unit's port map writes a counterpart into"; for the integration unit it says what a row counts
// for
func TestKernelUnportedSaysWhatAnIntegrationRowCountsFor(t *testing.T) {
	in := GateInput{UnitID: "uint", PortFiles: map[string]bool{}, RowKernels: map[string]map[string]bool{},
		Kernels: map[string][]string{"src/ilu_gdn_prep.cu": {"gate_kernel"}}}
	_, texts := unitGatesAll(in)
	want := "kernel_unported: 1 file(s) outside the test files define __global__ kernels for which no row of the unit's port map .einsteinOS/port/uint.tsv counts — a row counts for the kernel its column 4 names, when its column 2 is a reference kernel another unit's port map ports as a kernel, or its column 1 is missing:<group> of a group on the model's text path: src/ilu_gdn_prep.cu (gate_kernel)"
	if len(texts) == 0 || !strings.HasPrefix(texts[0], want) {
		t.Fatalf("%v", texts)
	}
	in.RowKernels = nil
	if _, texts := unitGatesAll(in); !strings.HasPrefix(texts[0], "kernel_unported: 1 file(s) define __global__ kernels and are neither test files nor files") {
		t.Fatalf("another unit: %v", texts)
	}
}

// 11/27 08:09 to 09:31 five verifications held the same 12 files: the first line of kernel_unported
// and the line after its refused rows gave missing:<group> as a row that counts, its end gave the
// planner's 21 missing groups, 0 of them on the model's text path, and the map came to hold
// missing:model.visual.… and missing:model.language_model.… rows, none counting. With no group on
// the path neither line gives that row
func TestKernelUnportedGivesNoMissingRowWhenNoGroupIsOnThePath(t *testing.T) {
	in := GateInput{UnitID: "uint", PortFiles: map[string]bool{}, RowKernels: map[string]map[string]bool{},
		PortFilesRefused: []string{"missing:model.visual.pos_embed.weight → src/ilu_gdn_prep.cu gate_kernel" + portMissingOffFmt},
		MissingGroups:    []string{"model.visual.pos_embed.weight", "mtp.fc.weight"},
		MissingOff:       map[string]bool{"model.visual.pos_embed.weight": true, "mtp.fc.weight": true},
		Kernels:          map[string][]string{"src/ilu_gdn_prep.cu": {"gate_kernel"}}}
	_, texts := unitGatesAll(in)
	want := "kernel_unported: 1 file(s) outside the test files define __global__ kernels for which no row of the unit's port map .einsteinOS/port/uint.tsv counts — a row counts for the kernel its column 4 names, when its column 2 is a reference kernel another unit's port map ports as a kernel; none of the 2 groups the planner's missing lines name is on the model's text path, and no row with column 1 missing:<group> counts: src/ilu_gdn_prep.cu (gate_kernel)"
	if len(texts) == 0 || !strings.HasPrefix(texts[0], want) {
		t.Fatalf("%v", texts)
	}
	if strings.Contains(texts[0], "is written in a row whose first column is missing:<group>") {
		t.Fatalf("the line after the rows still gives the row: %v", texts[0])
	}
	if !strings.Contains(texts[0], "; 1 row(s) of the port map do not count: missing:model.visual.pos_embed.weight → src/ilu_gdn_prep.cu gate_kernel") {
		t.Fatalf("the refused row: %v", texts[0])
	}
	// one group on the path: both lines give the row, as before
	in.MissingOff = map[string]bool{"model.visual.pos_embed.weight": true}
	_, texts = unitGatesAll(in)
	if !strings.Contains(texts[0], "or its column 1 is missing:<group> of a group on the model's text path") || !strings.Contains(texts[0], "is written in a row whose first column is missing:<group>") {
		t.Fatalf("one group on the path: %v", texts[0])
	}
}

// 11/27 09:29:34 seventeen rows like missing:model.language_model.layers.{i}.linear_attn.A_log →
// src/ilu_gdn_prep.cu beta_sigmoid_kernel were said by port_ref_file_unknown not to be reference
// files; the reason is their group, which no missing line names and the op lines give to a unit
func TestAMissingRowOfNoPlannerGroupIsGivenItsGroup(t *testing.T) {
	g := "model.language_model.layers.{i}.linear_attn.A_log"
	integrationAnswers["uint"] = IntegrationModelPlan{Ops: []IntegrationOp{
		{Group: g, Unit: "pA", Kernel: "gdn_ab_kernel"},
		{Group: "model.visual.pos_embed.weight", What: "vit"},
	}, GroupsOff: []string{"model.visual.pos_embed.weight"}}
	t.Cleanup(func() { delete(integrationAnswers, "uint") })
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/pA.tsv":      "src/kernels/cuda/fused_gdn.cu\tgdn_ab_kernel\tsrc/kernels/gpu/gdn_fused.cu\tgdn_ab_kernel\n",
		"src/kernels/gpu/gdn_fused.cu": "__global__ void gdn_ab_kernel(float* x) {}\n",
		".einsteinOS/port/uint.tsv":    "missing:" + g + "\tgdn\tsrc/ilu_gdn_prep.cu\tbeta_sigmoid_kernel\n",
	})
	r := portRefFiles(ws, "uint", []RefSymbol{{File: "src/kernels/gdn_parity.cpp", Name: "main"}})
	want := "  missing:" + g + " gdn → src/ilu_gdn_prep.cu:beta_sigmoid_kernel: " + g + " is not a group the planner's missing lines name (its 1 missing line(s) name the groups kernel_unported gives) — the planner's op lines give this group to pA gdn_ab_kernel (src/kernels/cuda/fused_gdn.cu gdn_ab_kernel)"
	if len(r) != 1 || r[0] != want {
		t.Fatalf("got %q", r)
	}
}

// 11/27 12:19:20 the map's 242 rows were read as 185, and the rows the reading left out were not
// among those said not to count: each is named, with the line read in its place
func TestARowALaterRowReplacesIsNamed(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/pcd027bc02f.tsv": "src/kernels/cuda/gdn.cu\tgdn_step_kernel\tsrc/kernels/gpu/gdn_core.cu\tgdn_step_kernel\n",
		"src/kernels/gpu/gdn_core.cu":      "__global__ void gdn_step_kernel(float* s) {}\n",
		".einsteinOS/port/uint.tsv": "src/kernels/cuda/gdn.cu\tgdn_step_kernel\tsrc/ilu_gdn_prep.cu\trow_norm_kernel\n" +
			"src/kernels/cuda/gdn.cu\tgdn_step_kernel\tsrc/ix_gdn.cu\tgdn_step_kernel\n" +
			"src/kernels/cuda/gdn.cu\tgdn_step_kernel\tsrc/ix_gdn.cu\tgdn_step_kernel\n",
	})
	_, _, refused := integrationPortRows(ws, "uint", readPortMap(ws, "uint"))
	all := strings.Join(refused, "\n")
	if len(refused) != 1 || !strings.Contains(all, "src/kernels/cuda/gdn.cu gdn_step_kernel → src/ilu_gdn_prep.cu row_norm_kernel (line 3 of the map gives the same column 1 and 2, and the kernel reads that line for them)") {
		t.Fatalf("%q", refused)
	}
}

// 11/27 16:08:02 kernel_unported passed on rows each giving a kernel of the integration unit a
// reference kernel no other row named — qsa.cu qsa_attend_kernel → src/ilu_gdn_prep.cu
// out_norm_kernel — and nothing measured whether they were one kernel: a row counts only when its
// kernel takes the types the unit's kernel for column 2 takes
func TestARowCountsOnlyWhenItsKernelTakesTheUnitsKernelsTypes(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/u02c65ed515.tsv": "src/kernels/cuda/qsa.cu\tqsa_attend_kernel\tsrc/ilu_qsa_step.cu\tqsa_attend_kernel\n",
		"src/ilu_qsa_step.cu":              "__global__ void qsa_attend_kernel(const float* q, const uint16_t* __restrict__ kpool, int n) {}\n",
		".einsteinOS/port/pcd027bc02f.tsv": "src/kernels/cuda/gdn.cu\tgdn_step_kernel\tsrc/kernels/gpu/gdn_core.cu\tgdn_step_kernel\n",
		"src/kernels/gpu/gdn_core.cu":      "__global__ void gdn_step_kernel(float* __restrict__ s, int cols) {}\n",
		"src/ilu_gdn_prep.cu":              "__global__ void out_norm_kernel(const float* o, float eps) {}\n",
		"src/ix_gdn.cu":                    "__global__ void gdn_step_kernel(float* state, int n) {}\n",
		".einsteinOS/port/uint.tsv": "src/kernels/cuda/qsa.cu\tqsa_attend_kernel\tsrc/ilu_gdn_prep.cu\tout_norm_kernel\n" +
			"src/kernels/cuda/gdn.cu\tgdn_step_kernel\tsrc/ix_gdn.cu\tgdn_step_kernel\n",
	})
	_, rk, refused := integrationPortRows(ws, "uint", readPortMap(ws, "uint"))
	if !rk["src/ix_gdn.cu"]["gdn_step_kernel"] || rk["src/ilu_gdn_prep.cu"]["out_norm_kernel"] {
		t.Fatalf("%v", rk)
	}
	want := "src/kernels/cuda/qsa.cu qsa_attend_kernel → src/ilu_gdn_prep.cu out_norm_kernel (src/ilu_gdn_prep.cu out_norm_kernel takes (const float*, float); u02c65ed515 ports qsa_attend_kernel as src/ilu_qsa_step.cu qsa_attend_kernel, which takes (const float*, const uint16_t*, int))"
	if len(refused) != 1 || refused[0] != want {
		t.Fatalf("%q", refused)
	}
}

// 11/27 16:08:02 kernel_unported passed while src/ix_gdn.cu defined five kernels with the names
// and parameter types of pcd027bc02f's kernels in src/kernels/gpu/gdn_core.cu: such a kernel is a
// gate of its own, and the unit's own file, where it ports the kernel, is not one
func TestAnIntegrationKernelWithAUnitKernelsNameAndTypesIsAGate(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/port/pcd027bc02f.tsv": "src/kernels/cuda/gdn.cu\tgdn_step_kernel\tsrc/kernels/gpu/gdn_core.cu\tgdn_step_kernel\n" +
			"src/kernels/cuda/gdn.cu\tgdn_l2_kernel\tsrc/kernels/gpu/gdn_core.cu\tgdn_l2_kernel\n",
		"src/kernels/gpu/gdn_core.cu": "__global__ void gdn_step_kernel(float* __restrict__ s, int cols) {}\n__global__ void gdn_l2_kernel(float* x, int cols, float eps) {}\n",
		"src/ix_gdn.cu":               "__global__ void gdn_step_kernel(float* state, int n) {}\n__global__ void gdn_l2_kernel(float* x, int cols) {}\n",
		"tests/k39/gdn_test.cu":       "__global__ void gdn_step_kernel(float* state, int n) {}\n",
	})
	ks := unitKernels(ws, []string{"src/ix_gdn.cu", "src/kernels/gpu/gdn_core.cu", "tests/k39/gdn_test.cu"})
	got := integrationCopiedKernels(ws, "uint", ks)
	if len(got) != 1 || got[0] != "  src/ix_gdn.cu gdn_step_kernel — pcd027bc02f ports it into src/kernels/gpu/gdn_core.cu, both taking (float*, int)" {
		t.Fatalf("%q", got)
	}
	kinds, texts := unitGatesAll(GateInput{UnitID: "uint", Copied: got})
	if len(kinds) != 1 || kinds[0] != "kernel_copied" || !strings.HasPrefix(texts[0], "kernel_copied: 1 kernel(s) of the integration unit's files outside the tests have the name and the parameter types of a kernel another unit's port map ports into another file:\n  src/ix_gdn.cu gdn_step_kernel") {
		t.Fatalf("%v %q", kinds, texts)
	}
}
