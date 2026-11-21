package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 11/21 03:01 cbb5dc6's e2e.sh opened 2 of the 26 shards. The port's loading processes must
// open every shard the model's index names; a python's shards do not count for the port.
func TestThePortOpensEveryShardTheModelIsMadeOf(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "model.safetensors.index.json"), []byte(`{"weight_map":{
		"a.weight":"model-00001-of-00003.safetensors","b.weight":"model-00002-of-00003.safetensors",
		"c.weight":"model-00003-of-00003.safetensors","d.weight":"model-00003-of-00003.safetensors"}}`), 0o644)
	shard := func(n string) string { return filepath.Join(dir, "model-0000"+n+"-of-00003.safetensors") }
	br := EvalBenchRun{Bench: EvalBench{File: "e2e.sh"}, Loaders: []E2ELoader{
		{PID: 1, Exe: "/ws/build/generate", Port: true, WeightFiles: []string{shard("1"), shard("2")}},
		{PID: 2, Exe: "/usr/local/bin/python3.10", WeightFiles: []string{shard("3")}},
	}}
	k, txt := e2eShardsGate(br, dir)
	if k != "integration_e2e_partial_model" || !strings.Contains(txt, "opened 2 of the 3") || !strings.Contains(txt, "model-00003-of-00003.safetensors") {
		t.Fatalf("2 of 3 shards passed or was not said: %q %s", k, txt)
	}
	br.Loaders[0].WeightFiles = append(br.Loaders[0].WeightFiles, shard("3"))
	if k, txt := e2eShardsGate(br, dir); k != "" {
		t.Fatalf("every shard opened and it failed: %s", txt)
	}
	// with no index, the weight files of the directory are the model
	plain := t.TempDir()
	os.WriteFile(filepath.Join(plain, "m.gguf"), []byte("w"), 0o644)
	os.WriteFile(filepath.Join(plain, "n.gguf"), []byte("w"), 0o644)
	br.Loaders = []E2ELoader{{PID: 1, Port: true, WeightFiles: []string{filepath.Join(plain, "m.gguf")}}}
	if k, _ := e2eShardsGate(br, plain); k != "integration_e2e_partial_model" {
		t.Fatalf("1 of 2 gguf files passed: %q", k)
	}
}
