package kernel

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeSafetensors writes a safetensors file whose header names tensors (name -> dtype,
// shape) and whose data is n zero bytes.
func writeSafetensors(t *testing.T, path string, tensors map[string][2]any, n int) {
	t.Helper()
	h := map[string]any{"__metadata__": map[string]string{"format": "pt"}}
	for name, v := range tensors {
		h[name] = map[string]any{"dtype": v[0], "shape": v[1], "data_offsets": []int{0, 0}}
	}
	b, _ := json.Marshal(h)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	binary.Write(f, binary.LittleEndian, uint64(len(b)))
	f.Write(b)
	f.Write(make([]byte, n))
}

func writeModelDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"architectures":["XForCausalLM"],"model_type":"x_moe","text_config":{"hidden_size":2048,"num_experts":256}}`), 0o644)
	writeSafetensors(t, filepath.Join(dir, "model-00001-of-00002.safetensors"), map[string][2]any{
		"model.layers.0.mlp.gate.weight": {"BF16", []int{256, 2048}},
		"model.embed_tokens.weight":      {"BF16", []int{248320, 2048}},
	}, 64)
	writeSafetensors(t, filepath.Join(dir, "model-00002-of-00002.safetensors"), map[string][2]any{
		"model.layers.1.mlp.gate.weight": {"BF16", []int{256, 2048}},
	}, 64)
	return dir
}

func TestTheModelIsReadFromItsOwnConfigAndHeaders(t *testing.T) {
	dir := writeModelDir(t)
	ms, err := measureModelShape(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ms.Config["model_type"] != "x_moe" {
		t.Fatalf("config not read: %v", ms.Config)
	}
	if len(ms.Files) != 2 || len(ms.Tensors) != 3 {
		t.Fatalf("files %v tensors %v", ms.Files, ms.Tensors)
	}
	e := ms.Tensors[0]
	if e.Name != "model.embed_tokens.weight" || e.Dtype != "BF16" || len(e.Shape) != 2 || e.Shape[0] != 248320 || e.File != "model-00001-of-00002.safetensors" {
		t.Fatalf("tensor read wrong: %+v", e)
	}
}

func TestAHeaderThatIsNotSafetensorsIsAnError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.safetensors"), []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, 0o644)
	if _, err := measureModelShape(dir); err == nil {
		t.Fatal("a header length beyond the limit was read")
	}
}

func TestAModelDirWithNothingOfAModelIsAnError(t *testing.T) {
	if _, err := measureModelShape(t.TempDir()); err == nil {
		t.Fatal("an empty directory measured as a model")
	}
}

func TestTheShapeIsReadOncePerFileSet(t *testing.T) {
	dir := writeModelDir(t)
	a, err := modelShapeOf(dir)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, "model-00002-of-00002.safetensors"))
	b, err := modelShapeOf(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Tensors) != 3 || len(b.Tensors) != 2 {
		t.Fatalf("a changed file set was not read again: %d then %d", len(a.Tensors), len(b.Tensors))
	}
}
