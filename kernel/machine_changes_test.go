package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 11/21 03:01 the integration's e2e.sh ran sed -i on corex's vllm registry.py and removed
// transformers from /root/.local. A run that changes a file of the machine's Python packages
// does not pass; an import writing __pycache__ is not a change.
func TestAnEndToEndRunThatChangesTheMachinesPackagesDoesNotPass(t *testing.T) {
	bootKernel()
	pkgs := t.TempDir()
	reg := filepath.Join(pkgs, "vllm", "model_executor", "models", "registry.py")
	os.MkdirAll(filepath.Dir(reg), 0o755)
	os.WriteFile(reg, []byte(`"Qwen3MoeForCausalLM": ("qwen3_moe", "Qwen3MoeForCausalLM"),`+"\n"), 0o644)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(reg, old, old)
	os.Chtimes(filepath.Dir(reg), old, old)
	oldDirs := machinePackageDirs
	machinePackageDirs = func() []string { return []string{pkgs} }
	defer func() { machinePackageDirs = oldDirs }()

	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, ".einsteinOS/verify/u1"), 0o755)
	put := func(body string) {
		os.WriteFile(filepath.Join(ws, ".einsteinOS/verify/u1/e2e.sh"), []byte(body), 0o755)
	}
	put("mkdir -p " + filepath.Join(filepath.Dir(reg), "__pycache__") + "; touch " + filepath.Join(filepath.Dir(reg), "__pycache__", "registry.cpython-310.pyc") + "\n")
	if k, txt := e2eMachineGate(evalRunE2E(ws, "u1")); k != "" {
		t.Fatalf("a bytecode cache was a change: %s", txt)
	}
	put(`sed -i 's/"Qwen3MoeForCausalLM": ("qwen3_moe", "Qwen3MoeForCausalLM"),/&\n    "Qwen3_5MoeForCausalLM": ("qwen3_moe", "Qwen3MoeForCausalLM"),/' ` + reg + "\n")
	k, txt := e2eMachineGate(evalRunE2E(ws, "u1"))
	if k != "integration_e2e_modifies_machine" || !strings.Contains(txt, reg) {
		t.Fatalf("sed -i on the vllm registry passed: %q %s", k, txt)
	}
}
