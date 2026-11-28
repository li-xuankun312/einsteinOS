package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTheModelsNamesAreItsArchitecturesAndModelTypes(t *testing.T) {
	cfg := map[string]any{"architectures": []any{"Qwen3_5MoeForConditionalGeneration"}, "model_type": "qwen3_5_moe",
		"text_config": map[string]any{"model_type": "qwen3_5_moe_text", "hidden_size": 2048.0}, "vision_config": map[string]any{"model_type": "vit"}}
	got := strings.Join(modelNames(cfg), " ")
	if got != "Qwen3_5MoeForConditionalGeneration qwen3_5_moe qwen3_5_moe_text" {
		t.Fatalf("%q", got)
	}
}

// 11/22: pytools/e2e_sub.py imported transformers' qwen3_5_moe model code; a process started on
// a file of the workspace that read another implementation of the model is not the port's,
// and one that read nothing of the weights yet is still listed.
func TestAProcessThatReadAnotherImplementationOfTheModelIsNotThePort(t *testing.T) {
	ws := t.TempDir()
	site := t.TempDir()
	impl := filepath.Join(site, "transformers", "models", "x_moe", "modeling_x_moe.py")
	os.MkdirAll(filepath.Dir(impl), 0o755)
	os.WriteFile(impl, []byte("class XMoeForCausalLM(PreTrainedModel):\n    pass\n"), 0o644)
	unrelated := filepath.Join(site, "json", "__init__.py")
	os.MkdirAll(filepath.Dir(unrelated), 0o755)
	os.WriteFile(unrelated, []byte("def dumps(): pass\n"), 0o644)
	own := filepath.Join(ws, "pytools", "runner.py")
	os.MkdirAll(filepath.Dir(own), 0o755)
	os.WriteFile(own, []byte("# runs XMoeForCausalLM with the port's kernels\n"), 0o644)

	now := time.Now()
	procs := []ProcSeen{
		{PID: 7, Exe: "/usr/bin/python3", Cmdline: "python3 pytools/e2e_sub.py", WorkspaceArgs: []string{"pytools/e2e_sub.py"}, Weights: []string{"m.safetensors"}, First: now, Last: now.Add(3 * time.Second)},
		{PID: 8, Exe: "/usr/bin/python3", Cmdline: "python3 pytools/e2e_infer.py", WorkspaceArgs: []string{"pytools/e2e_infer.py"}, First: now, Last: now.Add(time.Second)},
		{PID: 9, Exe: "/usr/bin/python3", Cmdline: "python3 pytools/runner.py", WorkspaceArgs: []string{"pytools/runner.py"}, Weights: []string{"m.safetensors"}, First: now, Last: now.Add(9 * time.Second)},
	}
	loaders := e2eLoaders(ws, procs, map[int][]string{9: {"k"}}, now)
	code := map[int][]string{7: {impl, unrelated}, 8: {impl}, 9: {own, unrelated}}
	loaders = e2eWithOtherCode(ws, loaders, procs, code, []string{"XMoeForCausalLM", "x_moe"})
	by := map[int]E2ELoader{}
	for _, l := range loaders {
		by[l.PID] = l
	}
	if l := by[7]; l.Port || len(l.OtherCode) != 1 || !strings.Contains(l.OtherCode[0], "modeling_x_moe.py (XMoeForCausalLM, x_moe)") {
		t.Fatalf("the transformers process: %+v", l)
	}
	if l, ok := by[8]; !ok || l.Port || l.Weights != 0 || l.Scripts[0] != "pytools/e2e_infer.py" {
		t.Fatalf("an import that loaded nothing yet is not listed: %+v", loaders)
	}
	if l := by[9]; !l.Port || len(l.OtherCode) != 0 {
		t.Fatalf("the workspace's own code naming the model is the port's: %+v", l)
	}
	if k, _ := e2eLoaderGate(EvalBenchRun{Loaders: loaders}, nil); k != "integration_e2e_loader_not_port" {
		t.Fatalf("a weight reader running another implementation passed: %q", k)
	}
}

func TestTheOtherImplementationGateNamesTheBranchFilesThatRanIt(t *testing.T) {
	br := EvalBenchRun{Bench: EvalBench{File: ".einsteinOS/verify/uint/e2e.sh"}, Loaders: []E2ELoader{
		{PID: 9, Exe: "/ws/build/runner", Port: true, Weights: 3},
		{PID: 8, Exe: "/usr/bin/python3", Cmdline: "python3 pytools/e2e_infer.py", Scripts: []string{"pytools/e2e_infer.py"}, OtherCode: []string{"/site/vllm/x.py (x_moe)"}},
	}}
	k, txt := e2eOtherImplGate(br, []string{"XMoeForCausalLM", "x_moe"})
	if k != "integration_e2e_other_implementation" {
		t.Fatalf("passed: %q", k)
	}
	for _, w := range []string{"1 process(es)", "(XMoeForCausalLM, x_moe, as its config.json does)", "pid 8 /usr/bin/python3 (python3 pytools/e2e_infer.py): started on the workspace's pytools/e2e_infer.py; read /site/vllm/x.py (x_moe)"} {
		if !strings.Contains(txt, w) {
			t.Fatalf("missing %q in\n%s", w, txt)
		}
	}
	br.Loaders = br.Loaders[:1]
	if k, _ := e2eOtherImplGate(br, nil); k != "" {
		t.Fatalf("a run of the port alone failed: %q", k)
	}
}

func TestTheRunsProcessesAreLoggedLongestFirst(t *testing.T) {
	now := time.Now()
	got := e2eProcTimes([]ProcSeen{
		{PID: 1, Exe: "/bin/bash", Cmdline: "bash e2e.sh", First: now, Last: now.Add(240 * time.Second)},
		{PID: 2, Exe: "/usr/bin/python3", Cmdline: "python3 pytools/e2e_infer.py", First: now, Last: now.Add(200 * time.Second)},
		{PID: 3, Exe: "/usr/bin/cat", First: now, Last: now.Add(10 * time.Millisecond)},
	}, time.Second)
	if got != "pid 1 /bin/bash (bash e2e.sh) 4m0s; pid 2 /usr/bin/python3 (python3 pytools/e2e_infer.py) 3m20s" {
		t.Fatalf("%q", got)
	}
}

// once the planner answered which units the model uses, a process that launched one used
// unit's kernel beside a forward pass of its own is not the runner
func TestWithThePlannersAnswerTheRunnerLaunchesEveryUsedUnit(t *testing.T) {
	perUnit := map[string][]string{"u_gdn": {"gdn_k"}, "u_moe": {"moe_k"}, "u_attn": {"attn_k"}}
	br := EvalBenchRun{ModelBytes: 100, Loaders: []E2ELoader{
		{PID: 9, Port: true, Weights: 26, TookBytes: 100, PortKernels: []string{"gdn_k"}, Seconds: 8},
	}}
	if _, ok := e2eRunner(br, perUnit); !ok {
		t.Fatal("without the planner's answer one unit's kernel makes the runner")
	}
	br.RunnerAllUnits = true
	if _, ok := e2eRunner(br, perUnit); ok {
		t.Fatal("one of three used units' kernels made the runner")
	}
	if k, txt := e2eLoaderGate(br, perUnit); k != "integration_e2e_loader_not_port" || !strings.Contains(txt, "u_attn: attn_k") || !strings.Contains(txt, "u_moe: moe_k") {
		t.Fatalf("the gate should name the used units the process did not launch: %q %s", k, txt)
	}
	br.Loaders[0].PortKernels = []string{"attn_k", "gdn_k", "moe_k"}
	if _, ok := e2eRunner(br, perUnit); !ok {
		t.Fatal("every used unit launched, and no runner")
	}
}

// the op lines name, per tensor group, the kernels that compute it — alternatives (a bf16 and a
// quantized matvec for lm_head, three gathers for embed_tokens): a runner launches one of each
// group's, and a group none of whose kernels it launched keeps it from being the runner
func TestTheRunnerLaunchesAKernelOfEveryTensorGroup(t *testing.T) {
	perUnit := map[string][]string{"u_gdn": {"gdn_step", "gdn_conv"}, "u_dec": {"bf16_gemv_warp_kernel", "mmvq_kernel"}}
	a := IntegrationModelPlan{Ops: []IntegrationOp{
		{Group: "lm_head.weight", Unit: "u_dec", Kernel: "bf16_gemv_warp_kernel"}, {Group: "lm_head.weight", Unit: "u_dec", Kernel: "mmvq_kernel"},
		{Group: "linear_attn.conv1d.weight", Unit: "u_gdn", Kernel: "gdn_conv"}, {Group: "linear_attn.A_log", Unit: "u_gdn", Kernel: "gdn_step"},
		{Group: "x", Unit: "u_gone", Kernel: "x"}, {Group: "q_norm", What: "per-head norm"}}}
	gs := a.opGroups(perUnit)
	if len(gs) != 3 || strings.Join(gs["lm_head.weight"], " ") != "bf16_gemv_warp_kernel mmvq_kernel" {
		t.Fatalf("%v", gs)
	}
	br := EvalBenchRun{ModelBytes: 100, RunnerAllUnits: true, RunnerGroups: gs, Loaders: []E2ELoader{
		{PID: 9, Port: true, Weights: 26, TookBytes: 100, PortKernels: []string{"bf16_gemv_warp_kernel", "gdn_conv"}, Seconds: 8},
	}}
	if _, ok := e2eRunner(br, perUnit); ok {
		t.Fatal("a process that launched no kernel of linear_attn.A_log is the runner")
	}
	k, txt := e2eLoaderGate(br, perUnit)
	if k != "integration_e2e_loader_not_port" || !strings.Contains(txt, "of the 3 tensor groups the planner's op lines name, 1 had none of their kernels launched by the port's process that took in the most of the model: linear_attn.A_log (one of: gdn_step)") {
		t.Fatalf("%q %s", k, txt)
	}
	// one of lm_head's two kernels is enough
	br.Loaders[0].PortKernels = append(br.Loaders[0].PortKernels, "gdn_step")
	if _, ok := e2eRunner(br, perUnit); !ok {
		t.Fatal("a kernel of every group launched, and no runner")
	}
}

func TestABuildsProcessesAreSummedByExecutableAndCapped(t *testing.T) {
	now := time.Now()
	var ps []ProcSeen
	for i := 0; i < 20; i++ {
		ps = append(ps, ProcSeen{PID: 100 + i, Exe: "/usr/local/corex/bin/clang++", Cmdline: "clang++ -c x.cu", First: now, Last: now.Add(time.Duration(2+i) * time.Second)})
	}
	ps = append(ps, ProcSeen{PID: 1, Exe: "/usr/bin/bash", First: now, Last: now.Add(400 * time.Second)})
	if got := procTimesByExe(ps); got != "/usr/local/corex/bin/clang++ ×20 3m50s; /usr/bin/bash ×1 6m40s" && got != "/usr/bin/bash ×1 6m40s; /usr/local/corex/bin/clang++ ×20 3m50s" {
		t.Fatalf("%q", got)
	}
	if got := procTimesByExe(ps); !strings.HasPrefix(got, "/usr/bin/bash ×1 6m40s") {
		t.Fatalf("most time first: %q", got)
	}
	if got := e2eProcTimes(ps, time.Second); !strings.HasSuffix(got, "; and 9 more") {
		t.Fatalf("not capped: %q", got)
	}
}

// 11/28 04:10:54 the gate said no process of the port both launched a ported kernel and took in
// the weights while build/e2e_all_check had done both: such a process is named, with what it took
// in, ahead of what it did not launch
func TestAPortProcessThatTookTheModelIsNamedWhenItIsNotTheRunner(t *testing.T) {
	perUnit := map[string][]string{"u_gdn": {"gdn_step", "gdn_conv"}}
	gs := map[string][]string{"linear_attn.A_log": {"gdn_step"}, "linear_attn.conv1d.weight": {"gdn_conv"}}
	br := EvalBenchRun{Bench: EvalBench{File: "e2e.sh"}, ModelBytes: 100 << 30, RunnerAllUnits: true, RunnerGroups: gs, Loaders: []E2ELoader{
		{PID: 9, Cmdline: "build/e2e_all_check /m", Port: true, Weights: 26, TookBytes: 100 << 30, PortKernels: []string{"gdn_conv"}},
		{PID: 7, Cmdline: "python3.10 e2e_weights.py", Port: true, Weights: 5, TookBytes: 1 << 20, PortKernels: []string{"gdn_step"}},
	}}
	k, txt := e2eLoaderGate(br, perUnit)
	if k != "integration_e2e_loader_not_port" || !strings.HasPrefix(txt, "integration_e2e_loader_not_port: in e2e.sh pid 9 (build/e2e_all_check /m), the port's, took in 100.00 GiB of the model's 100.00 GiB and launched ported kernels itself") ||
		strings.Contains(txt, "no process of the port both launched") || !strings.Contains(txt, "linear_attn.A_log (one of: gdn_step)") {
		t.Fatalf("%s", txt)
	}
	br.Loaders[0].TookBytes = 1 << 30
	if _, txt := e2eLoaderGate(br, perUnit); !strings.Contains(txt, "no process of the port both launched") {
		t.Fatalf("%s", txt)
	}
}

// 11/28 the loader lines gave each process's weights, intake and kernels and nothing of the token
// ids it printed: each line ends with the ids of its "generated:" lines
func TestEachLoaderLineGivesTheGeneratedIDsItPrinted(t *testing.T) {
	br := EvalBenchRun{ModelBytes: 100, TokensBy: map[int][]string{7: {"prompt: 1 2 3", "generated: 220 248046 198"}}, Loaders: []E2ELoader{
		{PID: 7, Cmdline: "python3.10 e2e_infer.py", Port: true, Weights: 26, TookBytes: 100},
		{PID: 9, Cmdline: "build/e2e_all_check /m", Port: true, Built: true, Weights: 26, TookBytes: 100, PortKernels: []string{"k"}},
	}}
	got := strings.Split(e2eLoaderLines(br), "\n")
	if len(got) != 2 || !strings.HasSuffix(got[0], "; token ids it printed on \"generated:\" lines: 3") || !strings.HasSuffix(got[1], "lines: 0") {
		t.Fatalf("%q", got)
	}
}
