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
