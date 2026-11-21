package kernel

// eval_e2e_shards.go — the port loads the whole model.
//
// 11/21 03:01 the integration's e2e.sh (cbb5dc6) opened the first 2 of the model's 26
// safetensors shards and copied their tensors of at most 8192 elements to the GPU; the model
// gate saw weight files of the model opened and passed. The shards the model is made of are
// read from its own index (model.safetensors.index.json, every file its weight_map names), or
// are every weight file of its directory when it has none, and the port's loading processes
// must have opened or mapped each of them.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const gateE2EPartialModelFmt = "integration_e2e_partial_model: the port's processes in %[1]s opened %[2]d of the %[3]d weight files the model at %[4]s is made of (%[5]s); not opened: %[6]s"

// modelShards: the weight files the model at dir is made of, by base name.
func modelShards(dir string) []string {
	set := map[string]bool{}
	if b, err := os.ReadFile(filepath.Join(dir, "model.safetensors.index.json")); err == nil {
		var idx struct {
			WeightMap map[string]string `json:"weight_map"`
		}
		if json.Unmarshal(b, &idx) == nil {
			for _, f := range idx.WeightMap {
				set[f] = true
			}
		}
	}
	if len(set) == 0 {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if weightFileRe.MatchString(e.Name()) {
				set[e.Name()] = true
			}
		}
	}
	var out []string
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// e2eShardsGate: the port's loading processes (E2ELoader.Port) opened every shard of the model.
func e2eShardsGate(br EvalBenchRun, modelDir string) (kind, text string) {
	shards := modelShards(modelDir)
	if len(shards) == 0 {
		return "", ""
	}
	opened := map[string]bool{}
	for _, l := range br.Loaders {
		if !l.Port {
			continue
		}
		for _, w := range l.WeightFiles {
			opened[filepath.Base(w)] = true
		}
	}
	var missing []string
	for _, s := range shards {
		if !opened[s] {
			missing = append(missing, s)
		}
	}
	if len(missing) == 0 {
		return "", ""
	}
	src := "its index's weight_map"
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors.index.json")); err != nil {
		src = "the weight files of its directory"
	}
	shown := missing
	if len(shown) > 8 {
		shown = append(append([]string{}, shown[:8]...), fmt.Sprintf("… %d more", len(missing)-8))
	}
	return "integration_e2e_partial_model", fmt.Sprintf(gateE2EPartialModelFmt, br.Bench.File, len(shards)-len(missing), len(shards), modelDir, src, strings.Join(shown, " "))
}
