package kernel

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestTensorsDifferingOnlyByLayerFoldIntoOneLine(t *testing.T) {
	ms := ModelShape{Tensors: []ModelTensor{
		{Name: "model.layers.0.mlp.gate.weight", Dtype: "BF16", Shape: []int64{256, 2048}},
		{Name: "model.layers.1.mlp.gate.weight", Dtype: "BF16", Shape: []int64{256, 2048}},
		{Name: "model.layers.2.mlp.gate.weight", Dtype: "BF16", Shape: []int64{256, 2048}},
		{Name: "model.layers.5.mlp.gate.weight", Dtype: "BF16", Shape: []int64{256, 2048}},
		{Name: "model.layers.3.self_attn.q_proj.weight", Dtype: "BF16", Shape: []int64{8192, 2048}},
		{Name: "model.norm.weight", Dtype: "BF16", Shape: []int64{2048}},
	}}
	got := ms.TensorLines()
	want := "  model.layers.{i}.mlp.gate.weight BF16 [256,2048] ×4 {i}=0-2,5\n" +
		"  model.layers.{i}.self_attn.q_proj.weight BF16 [8192,2048] ×1 {i}=3\n" +
		"  model.norm.weight BF16 [2048] ×1 \n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestExpertsOfLayersAreGroupedWithTheirCount(t *testing.T) {
	var ts []ModelTensor
	for l := 0; l < 2; l++ {
		for e := 0; e < 3; e++ {
			ts = append(ts, ModelTensor{Name: "m.layers." + string(rune('0'+l)) + ".experts." + string(rune('0'+e)) + ".w", Dtype: "BF16", Shape: []int64{512, 2048}})
		}
	}
	g := ModelShape{Tensors: ts}.TensorGroups()
	if len(g) != 1 || g[0].Pattern != "m.layers.{i}.experts.{i}.w" || len(g[0].Indices) != 6 {
		t.Fatalf("%+v", g)
	}
	if !strings.Contains(ModelShape{Tensors: ts}.TensorLines(), "×6 {i}=6") {
		t.Fatalf("two-index groups should be counted: %s", ModelShape{Tensors: ts}.TensorLines())
	}
}

func TestConfigIsFlattenedAndLongListsRunLength(t *testing.T) {
	var lt []any
	for i := 0; i < 40; i++ {
		if i%4 == 3 {
			lt = append(lt, "full_attention")
		} else {
			lt = append(lt, "linear_attention")
		}
	}
	ms := ModelShape{Config: map[string]any{"model_type": "x", "text_config": map[string]any{"hidden_size": 2048.0, "layer_types": lt}, "architectures": []any{"A"}}}
	got := ms.ConfigLines()
	for _, w := range []string{"  architectures=[\"A\"]\n", "  model_type=\"x\"\n", "  text_config.hidden_size=2048\n", "  text_config.layer_types=[40: linear_attention×3,full_attention×1,"} {
		if !strings.Contains(got, w) {
			t.Fatalf("missing %q in\n%s", w, got)
		}
	}
}

func TestTheIntegrationUnitIsToldTheModelAsMeasured(t *testing.T) {
	dir := writeModelDir(t)
	got := integrationModelBlock(dir)
	for _, w := range []string{"config.json and the headers of its 2 safetensors file(s), 3 tensor(s)", "  model_type=\"x_moe\"\n", "  text_config.hidden_size=2048\n",
		"  model.layers.{i}.mlp.gate.weight BF16 [256,2048] ×2 {i}=0-1\n", "  model.embed_tokens.weight BF16 [248320,2048] ×1"} {
		if !strings.Contains(got, w) {
			t.Fatalf("missing %q in\n%s", w, got)
		}
	}
	if got := integrationModelBlock(t.TempDir()); !strings.Contains(got, "could not be measured") {
		t.Fatalf("an unmeasured model should say so: %s", got)
	}
}

// 11/22 the e2e line said the kernel "reads the prefill and decode tok/s it prints", which it
// had stopped doing (bb428f62); it says what the kernel checks, with the measured model.
func TestTheE2ELineSaysWhatTheKernelChecks(t *testing.T) {
	dir := writeModelDir(t)
	old := evalModelDir
	defer func() { evalModelDir = old }()
	evalModelDir = dir
	blk := UnitRequirements{Tests: []RefTest{{Name: "t", Command: "t"}}}.IntegrationBlock("r", "abc", "uint")
	for _, w := range []string{"2 weight file(s)", "takes in at least 90% of those bytes", "launches, itself, kernels the units ported",
		"the ids that process prints", "naming the model (XForCausalLM, x_moe)"} {
		if !strings.Contains(blk, w) {
			t.Fatalf("missing %q in\n%s", w, blk)
		}
	}
	if strings.Contains(blk, "reads the prefill and decode tok/s it prints") {
		t.Fatalf("the old line is still there:\n%s", blk)
	}
}
