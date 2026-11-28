package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 11/23 the integration's e2e.sh ran setup_model_ref.py on the model's directory: it wrote
// modeling_qwen3_5_moe.py there and gave config.json an auto_map. What a run changes there is
// measured and put back as it was
func TestWhatARunChangesInTheModelsDirectoryIsPutBack(t *testing.T) {
	dir := t.TempDir()
	putTree(t, dir, map[string]string{"config.json": `{"model_type":"qwen3_5_moe"}`, "model-00001-of-00026.safetensors": "weights"})
	g := guardModelDir(dir)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"model_type":"qwen3_5_moe","auto_map":{"AutoModelForCausalLM":"modeling_qwen3_5_moe.Qwen"}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "modeling_qwen3_5_moe.py"), []byte("class Qwen: pass"), 0o644)
	got := g.restore()
	if strings.Join(got, " ") != "config.json (changed) modeling_qwen3_5_moe.py (added)" {
		t.Fatalf("%q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "config.json")); string(b) != `{"model_type":"qwen3_5_moe"}` {
		t.Fatalf("config.json not put back: %s", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "modeling_qwen3_5_moe.py")); err == nil {
		t.Fatal("the added file stays")
	}
	if got := guardModelDir(dir).restore(); len(got) != 0 {
		t.Fatalf("a run that changed nothing: %q", got)
	}
}
