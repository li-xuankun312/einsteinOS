package kernel

// model_shape.go — what the model on this machine is, read from its own files.
//
// 11/22 05:01-12:52 the integration unit was told the model's directory and its name and
// nothing else ("the kernel runs e2e.sh with MODEL_DIR=… and reads the prefill and decode
// tok/s it prints"). The reference's engine was written for another model of the family, and
// what this one is — its config and the name, dtype and shape of every tensor — is in the
// model's config.json and in the header each safetensors file starts with (an 8-byte
// little-endian length, then that many bytes of JSON: name -> dtype, shape, data offsets).
// The headers are read, never the weights.

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// ModelTensor is one tensor a safetensors header names.
type ModelTensor struct {
	Name  string  `json:"name"`
	Dtype string  `json:"dtype"`
	Shape []int64 `json:"shape"`
	File  string  `json:"file"`
}

// ModelShape is what the model's own files say it is.
type ModelShape struct {
	Dir     string         `json:"dir"`
	Config  map[string]any `json:"config,omitempty"`
	Tensors []ModelTensor  `json:"tensors,omitempty"`
	Files   []string       `json:"files,omitempty"`
}

// safetensorsHeaderMax: a header longer than this is not read (the format's own limit is
// 100 MB; a real model's is a few hundred kB per shard).
const safetensorsHeaderMax = 100 << 20

// readSafetensorsHeader reads the tensors a safetensors file's header names, sorted by name.
func readSafetensorsHeader(path string) ([]ModelTensor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var n uint64
	if err := binary.Read(f, binary.LittleEndian, &n); err != nil {
		return nil, fmt.Errorf("%s: header length: %v", filepath.Base(path), err)
	}
	if n == 0 || n > safetensorsHeaderMax {
		return nil, fmt.Errorf("%s: header length %d", filepath.Base(path), n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, fmt.Errorf("%s: header: %v", filepath.Base(path), err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(buf, &raw); err != nil {
		return nil, fmt.Errorf("%s: header: %v", filepath.Base(path), err)
	}
	var out []ModelTensor
	for name, v := range raw {
		if name == "__metadata__" {
			continue
		}
		var t struct {
			Dtype string  `json:"dtype"`
			Shape []int64 `json:"shape"`
		}
		if json.Unmarshal(v, &t) != nil {
			continue
		}
		out = append(out, ModelTensor{Name: name, Dtype: t.Dtype, Shape: t.Shape, File: filepath.Base(path)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// measureModelShape reads dir's config.json and the header of each of its safetensors files.
func measureModelShape(dir string) (ModelShape, error) {
	ms := ModelShape{Dir: dir}
	if b, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
		var c map[string]any
		if json.Unmarshal(b, &c) == nil {
			ms.Config = c
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.safetensors"))
	sort.Strings(files)
	for _, f := range files {
		ts, err := readSafetensorsHeader(f)
		if err != nil {
			return ms, err
		}
		ms.Files = append(ms.Files, filepath.Base(f))
		ms.Tensors = append(ms.Tensors, ts...)
	}
	sort.Slice(ms.Tensors, func(i, j int) bool { return ms.Tensors[i].Name < ms.Tensors[j].Name })
	if ms.Config == nil && len(ms.Tensors) == 0 {
		return ms, fmt.Errorf("%s: no config.json and no safetensors file", dir)
	}
	return ms, nil
}

var (
	modelShapeMu    sync.Mutex
	modelShapeCache = map[string]ModelShape{}
)

// modelShapeOf: measureModelShape of dir, read once per directory and file set.
func modelShapeOf(dir string) (ModelShape, error) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.safetensors"))
	key := dir
	for _, f := range append(files, filepath.Join(dir, "config.json")) {
		if st, err := os.Stat(f); err == nil {
			key += fmt.Sprintf("|%s:%d:%d", filepath.Base(f), st.Size(), st.ModTime().UnixNano())
		}
	}
	modelShapeMu.Lock()
	ms, ok := modelShapeCache[key]
	modelShapeMu.Unlock()
	if ok {
		return ms, nil
	}
	ms, err := measureModelShape(dir)
	if err != nil {
		return ms, err
	}
	modelShapeMu.Lock()
	modelShapeCache[key] = ms
	modelShapeMu.Unlock()
	return ms, nil
}
