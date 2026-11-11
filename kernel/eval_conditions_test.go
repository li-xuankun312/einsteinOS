package kernel

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// The model's conditions come from its directory (through the link): its name, the
// architecture fields of config.json (under text_config for a multimodal checkpoint) and the
// size of its weights; the GPUs from ixsmi's query form; the host memory from /proc/meminfo.
func TestEvalConditionsAreMeasured(t *testing.T) {
	real := filepath.Join(t.TempDir(), "Qwen3.6-35B-A3B")
	os.MkdirAll(real, 0o755)
	os.WriteFile(filepath.Join(real, "config.json"), []byte(`{"architectures":["Qwen3_5MoeForConditionalGeneration"],"model_type":"qwen3_5_moe","text_config":{"num_hidden_layers":40,"hidden_size":2048,"num_experts":256,"num_experts_per_tok":8},"torch_dtype":"bfloat16"}`), 0o644)
	os.WriteFile(filepath.Join(real, "model-00001-of-00002.safetensors"), make([]byte, 1000), 0o644)
	os.WriteFile(filepath.Join(real, "model-00002-of-00002.safetensors"), make([]byte, 500), 0o644)
	os.WriteFile(filepath.Join(real, "tokenizer.json"), make([]byte, 99), 0o644)
	link := filepath.Join(t.TempDir(), "model")
	os.Symlink(real, link)

	oldDir, oldQ := evalModelDir, evalGPUQuery
	defer func() { evalModelDir, evalGPUQuery = oldDir, oldQ }()
	evalModelDir = link
	evalGPUQuery = func() (string, error) {
		return "0, Iluvatar BI-V150, 65536\n1, Iluvatar BI-V150, 65536\n", nil
	}
	c := measureEvalConditions("Niko1221/Strata", "6801c77ff6f7")
	if c.ModelName != "Qwen3.6-35B-A3B" || c.ModelBytes != 1500 || c.ModelErr != "" {
		t.Fatalf("model: %+v", c)
	}
	if c.Model["num_hidden_layers"].(float64) != 40 || c.Model["num_experts"].(float64) != 256 || c.Model["model_type"] != "qwen3_5_moe" {
		t.Fatalf("config fields: %v", c.Model)
	}
	if len(c.GPUs) != 2 || c.GPUs[1].MemoryMiB != 65536 || c.GPUs[0].Name != "Iluvatar BI-V150" {
		t.Fatalf("gpus: %+v", c.GPUs)
	}
	if c.HostMemKiB <= 0 || c.RefSHA != "6801c77ff6f7" {
		t.Fatalf("host/ref: %+v", c)
	}
	evalModelDir = filepath.Join(t.TempDir(), "absent")
	if c := measureEvalConditions("o/r", "x"); c.ModelErr == "" {
		t.Fatal("a missing model is recorded as such")
	}
}

// 11/10 /tmp/model did not exist on the rebuilt machine; the model was at
// /root/public-storage/models/Qwen/Qwen3.6-35B-A3B. The run's model is found there once and
// kept, whatever links appear later.
func TestTheRunsModelIsFoundOnceWhereTheMachineKeepsIt(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "Qwen", "Qwen3.6-35B-A3B")
	os.MkdirAll(model, 0o755)
	os.WriteFile(filepath.Join(model, "config.json"), []byte(`{"model_type":"qwen3_5_moe"}`), 0o644)
	os.WriteFile(filepath.Join(model, "model-00001-of-00002.safetensors"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(root, "Empty", "config-only"), 0o755)
	os.WriteFile(filepath.Join(root, "Empty", "config-only", "config.json"), []byte(`{}`), 0o644)
	oldRoots, oldDir := evalModelRoots, evalModelDir
	defer func() {
		evalModelRoots, evalModelDir, evalModelOnce, evalModelResolved = oldRoots, oldDir, sync.Once{}, ""
	}()
	evalModelRoots, evalModelDir, evalModelOnce = []string{filepath.Join(root, "absent"), root}, evalModelDefault, sync.Once{}
	if hasModel(evalModelDefault) {
		t.Skip("this machine has a model at /tmp/model")
	}
	if got := evalModel(); got != model {
		t.Fatalf("model: %q", got)
	}
	os.WriteFile(filepath.Join(root, "Empty", "config-only", "w.gguf"), []byte("x"), 0o644)
	if got := evalModel(); got != model {
		t.Fatalf("kept for the run: %q", got)
	}
	evalModelDir = "/tmp/some-model"
	if evalModel() != "/tmp/some-model" {
		t.Fatal("a directory set by the caller is taken as it is")
	}
}
