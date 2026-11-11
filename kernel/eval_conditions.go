package kernel

// eval_conditions.go — the conditions a measurement was taken under, measured by the kernel
// and written into the evaluation record beside the numbers: the model on this machine (from
// its config.json), the GPUs and their memory (ixsmi), the host memory (/proc/meminfo) and
// the reference commit. A number is only comparable with another taken under conditions that
// correspond (eval_compare.go); nobody declares them.

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// evalModelDir: where the model the port runs end to end is on this machine (11/08: a link
// to /root/public-storage/models/Qwen/Qwen3.6-35B-A3B).
var evalModelDir = evalModelDefault

const evalModelDefault = "/tmp/model"

// evalModelRoots: where a machine keeps its models, searched when evalModelDefault holds none.
var evalModelRoots = []string{"/root/public-storage/models", "/root/shared-storage/models", "/models"}

var (
	evalModelOnce     sync.Once
	evalModelResolved string
)

// evalModel: the model directory every evaluation of this run uses, resolved once. 11/10 the
// machine had been rebuilt and /tmp/model did not exist until a conversation linked it: the
// integration's end-to-end run failed at 22:39 and 23:15 and passed at 23:07 and 23:33 on the
// same code, by whether the link existed at that moment. The kernel now takes evalModelDir
// when it holds a model, else the first directory under evalModelRoots that does (sorted),
// and keeps that for the run. A value set by a caller (tests) is taken as it is.
func evalModel() string {
	if evalModelDir != evalModelDefault {
		return evalModelDir
	}
	evalModelOnce.Do(func() {
		evalModelResolved = evalModelDefault
		if hasModel(evalModelDefault) {
			return
		}
		for _, root := range evalModelRoots {
			if d := findModel(root, 3); d != "" {
				evalModelResolved = d
				log.Printf("eval: %s holds no model; the run's model is %s", evalModelDefault, d)
				return
			}
		}
		log.Printf("eval: no model in %s nor under %v", evalModelDefault, evalModelRoots)
	})
	return evalModelResolved
}

// hasModel: dir holds a config.json and a weight file.
func hasModel(dir string) bool {
	if !fileExists(filepath.Join(dir, "config.json")) {
		return false
	}
	for _, pat := range []string{"*.safetensors", "*.gguf", "*.bin"} {
		if m, _ := filepath.Glob(filepath.Join(dir, pat)); len(m) > 0 {
			return true
		}
	}
	return false
}

// findModel: the first directory (sorted, depth-first, at most depth levels below root) that
// holds a model.
func findModel(root string, depth int) string {
	if hasModel(root) {
		return root
	}
	if depth == 0 {
		return ""
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			if d := findModel(filepath.Join(root, e.Name()), depth-1); d != "" {
				return d
			}
		}
	}
	return ""
}

// EvalGPU is one GPU as ixsmi reports it.
type EvalGPU struct {
	Index     int    `json:"index"`
	Name      string `json:"name"`
	MemoryMiB int64  `json:"memory_mib"`
}

// EvalConditions are the measured conditions of an evaluation.
type EvalConditions struct {
	ModelDir   string         `json:"model_dir"`
	ModelName  string         `json:"model_name"`  // the model directory's name (Qwen3.6-35B-A3B)
	Model      map[string]any `json:"model"`       // its config.json's architecture fields
	ModelBytes int64          `json:"model_bytes"` // size of its weight files
	ModelErr   string         `json:"model_err,omitempty"`
	GPUs       []EvalGPU      `json:"gpus"`
	GPUErr     string         `json:"gpu_err,omitempty"`
	HostMemKiB int64          `json:"host_mem_kib"`
	RefRepo    string         `json:"ref_repo"`
	RefSHA     string         `json:"ref_sha"`
}

// evalConfigKeys: the config.json fields that describe a model's architecture (top level, or
// under text_config for a multimodal checkpoint).
var evalConfigKeys = []string{"model_type", "architectures", "num_hidden_layers", "hidden_size",
	"num_attention_heads", "num_key_value_heads", "num_experts", "num_experts_per_tok",
	"moe_intermediate_size", "intermediate_size", "vocab_size", "max_position_embeddings", "torch_dtype"}

// evalGPUQuery: ixsmi takes nvidia-smi's query form.
var evalGPUQuery = func() (string, error) {
	bin := ""
	for _, d := range toolchainDirs {
		if p := filepath.Join(d, "ixsmi"); fileExists(p) {
			bin = p
		}
	}
	if bin == "" {
		if p, err := exec.LookPath("ixsmi"); err == nil {
			bin = p
		}
	}
	if bin == "" {
		return "", fmt.Errorf("ixsmi not found")
	}
	out, err := exec.Command(bin, "--query-gpu=index,name,memory.total", "--format=csv,noheader,nounits").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, tailText(string(out), 200))
	}
	return string(out), nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// measureEvalConditions measures the conditions of an evaluation of repo at sha.
func measureEvalConditions(repo, sha string) EvalConditions {
	dir := evalModel()
	c := EvalConditions{ModelDir: dir, RefRepo: repo, RefSHA: sha}
	c.ModelName, c.Model, c.ModelBytes, c.ModelErr = readModelConditions(dir)
	if out, err := evalGPUQuery(); err != nil {
		c.GPUErr = err.Error()
	} else {
		c.GPUs = parseGPUQuery(out)
	}
	c.HostMemKiB = hostMemKiB()
	return c
}

// readModelConditions reads the model directory: its name, its config.json's architecture
// fields and the total size of its weight files.
func readModelConditions(dir string) (name string, model map[string]any, bytes int64, errText string) {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", nil, 0, err.Error()
	}
	name = filepath.Base(real)
	b, err := os.ReadFile(filepath.Join(real, "config.json"))
	if err != nil {
		return name, nil, 0, err.Error()
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		return name, nil, 0, "config.json: " + err.Error()
	}
	model = map[string]any{}
	text, _ := cfg["text_config"].(map[string]any)
	for _, k := range evalConfigKeys {
		if v, ok := cfg[k]; ok {
			model[k] = v
		} else if v, ok := text[k]; ok {
			model[k] = v
		}
	}
	entries, _ := os.ReadDir(real)
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".safetensors") || strings.HasSuffix(n, ".gguf") || strings.HasSuffix(n, ".bin") {
			if info, err := e.Info(); err == nil {
				bytes += info.Size()
			}
		}
	}
	return name, model, bytes, ""
}

// parseGPUQuery: "0, Iluvatar BI-V100, 32768" per line.
func parseGPUQuery(out string) []EvalGPU {
	var gpus []EvalGPU
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, ",")
		if len(f) != 3 {
			continue
		}
		idx, e1 := strconv.Atoi(strings.TrimSpace(f[0]))
		mem, e2 := strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64)
		if e1 != nil || e2 != nil {
			continue
		}
		gpus = append(gpus, EvalGPU{Index: idx, Name: strings.TrimSpace(f[1]), MemoryMiB: mem})
	}
	return gpus
}

// hostMemKiB: MemTotal of /proc/meminfo.
func hostMemKiB() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "MemTotal:") {
			f := strings.Fields(l)
			if len(f) >= 2 {
				n, _ := strconv.ParseInt(f[1], 10, 64)
				return n
			}
		}
	}
	return 0
}
