package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// What the end-to-end run generated is checked, token by token, against the class the
// model's config.json names, given the same prompt and the tokens before each: one different
// token fails; an implementation that cannot be run here stops the run as a machine gate.
func TestTheGeneratedTokensAreCheckedAgainstTheModelsImplementation(t *testing.T) {
	model := filepath.Join(t.TempDir(), "Qwen3.6-35B-A3B")
	os.MkdirAll(model, 0o755)
	os.WriteFile(filepath.Join(model, "config.json"), []byte(`{"architectures":["Qwen3_5MoeForCausalLM"],"model_type":"qwen3_5_moe"}`), 0o644)
	// 11/09's run: the first tokens it generated
	br := EvalBenchRun{PromptIDs: []int{9707, 11, 1879}, GeneratedIDs: []int{220, 248046, 198, 248045}}
	old := evalTeacherForcedFn
	defer func() { evalTeacherForcedFn = old }()
	var gotArch string
	ranked := []int{220, 248046, 198, 248045}
	evalTeacherForcedFn = func(dir, arch string, p, g []int) ([]int, string, string) {
		gotArch = arch
		return ranked, arch, ""
	}
	if k, txt := evalCorrectGate(br, model); k != "" || gotArch != "Qwen3_5MoeForCausalLM" {
		t.Fatalf("the same tokens: %q %s (ran %s)", k, txt, gotArch)
	}
	ranked = []int{220, 248046, 271, 248045}
	if k, txt := evalCorrectGate(br, model); k != "integration_wrong_tokens" || !strings.Contains(txt, "at 1 of 4") || !strings.Contains(txt, "the port 198") || !strings.Contains(txt, "Qwen3_5MoeForCausalLM 271") {
		t.Fatalf("one token differs: %q %s", k, txt)
	}
	evalTeacherForcedFn = func(string, string, []int, []int) ([]int, string, string) {
		return nil, "", "unavailable: ModuleNotFoundError: No module named 'transformers'"
	}
	k, txt := evalCorrectGate(br, model)
	if k != GateReferenceImplMissing || !gateIsMachine(k) || !strings.Contains(txt, "transformers") {
		t.Fatalf("no implementation here: %q %s", k, txt)
	}
}

// The kernel's script itself, run with this machine's python: where transformers is not
// installed it says so (exit 3, "unavailable: …"), and that is what the gate reports.
func TestTheImplementationScriptSaysWhenItCannotRun(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	if exec.Command("python3", "-c", "import torch, transformers").Run() == nil {
		t.Skip("transformers is installed here")
	}
	_, _, why := evalTeacherForcedFn(t.TempDir(), "Qwen3_5MoeForCausalLM", []int{1}, []int{2})
	if !strings.Contains(why, evalCorrectUnavailableMarker) || !strings.Contains(why, "ModuleNotFoundError") {
		t.Fatalf("why: %s", why)
	}
}
