package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 11/21 03:01 cbb5dc6's e2e.sh: a python opened two shards, vllm loaded the model, and each
// unit's parity binary launched its kernels with no weights open; 11/21 09:39 the only reader
// of a shard was a python running e2e.sh's heredoc. Neither passes. The reference's own shape —
// its Python tool converts the shards into a pack, its runtime loads the pack and runs every
// unit's kernels — passes.
func TestThePortsProcessesRunTheModel(t *testing.T) {
	perUnit := map[string][]string{
		"u7991572426": {"gdn_conv_kernel", "gdn_rec_kernel"},
		"uebd9b81357": {"kv_append_q8_kernel"},
	}
	ws := t.TempDir()
	start := time.Now()
	pack := filepath.Join(t.TempDir(), "weights.bin")
	os.WriteFile(pack, []byte("pack"), 0o644)
	shards := []string{"/tmp/model/model-00001-of-00002.safetensors", "/tmp/model/model-00002-of-00002.safetensors"}
	launched := map[int][]string{
		301:   {"gdn_conv_kernel"},     // build/gdn_parity --selftest
		302:   {"kv_append_q8_kernel"}, // build/kv_q8_parity
		58416: {"elementwise_kernel"},
		400:   {"gdn_rec_kernel", "kv_append_q8_kernel"},
	}
	gate := func(procs []ProcSeen) (string, string) {
		br := EvalBenchRun{Bench: EvalBench{File: "e2e.sh"}, Loaders: e2eLoaders(ws, procs, launched, start)}
		return e2eLoaderGate(br, perUnit)
	}
	// a python running a heredoc reads a shard
	k, txt := gate([]ProcSeen{
		{PID: 58416, Exe: "/usr/local/bin/python3.10", Cmdline: "python3 -", Weights: shards[1:]},
		{PID: 301, Exe: ws + "/build/gdn_parity"}, {PID: 302, Exe: ws + "/build/kv_q8_parity"},
	})
	if k != "integration_e2e_loader_not_port" || !strings.Contains(txt, "pid 58416 /usr/local/bin/python3.10 (python3 -): the port's: false; model weight files 1") {
		t.Fatalf("a heredoc python reading the model passed or was not said: %q %s", k, txt)
	}
	// the reference's shape: the workspace's converter reads the shards, the runtime the pack
	faithful := []ProcSeen{
		{PID: 350, Exe: "/usr/local/bin/python3.10", Cmdline: "python3 pytools/ref_packer.py", WorkspaceArgs: []string{ws + "/pytools/ref_packer.py"}, Weights: shards},
		{PID: 400, Exe: ws + "/build/generate", Large: []string{pack}},
	}
	if k, txt := gate(faithful); k != "" {
		t.Fatalf("the reference's own shape failed: %s", txt)
	}
	// the runtime that read no weights at all is not running the model
	faithful[1].Large = nil
	if k, txt := gate(faithful); k != "integration_e2e_loader_not_port" || !strings.Contains(txt, "no process of the port both launched") {
		t.Fatalf("kernels with no weights passed: %q %s", k, txt)
	}
	// a pack written before the run is not this run's conversion
	old := time.Now().Add(-time.Hour)
	os.Chtimes(pack, old, old)
	faithful[1].Large = []string{pack}
	if k, _ := gate(faithful); k != "integration_e2e_loader_not_port" {
		t.Fatalf("a pack older than the run counted: %q", k)
	}
	// a unit's kernels left to its own test, beside the runner: the integration verifies its
	// sub-units in many processes (whether every unit ran is e2ePortGate's question)
	delete(launched, 400)
	launched[400] = []string{"gdn_rec_kernel"}
	if k, txt := gate([]ProcSeen{{PID: 400, Exe: ws + "/build/generate", Weights: shards}, {PID: 302, Exe: ws + "/build/kv_q8_parity"}}); k != "" {
		t.Fatalf("a runner beside a unit's test failed: %s", txt)
	}
	// a process that took the model but launched no ported kernel is not running the port
	delete(launched, 400)
	if k, _ := gate([]ProcSeen{{PID: 400, Exe: ws + "/build/generate", Weights: shards}}); k != "integration_e2e_loader_not_port" {
		t.Fatalf("a runner that launched no ported kernel passed: %q", k)
	}
}

// The watcher keeps what each process held: the process that holds a weight file and the one
// that maps the workspace's library are told apart.
func TestProcWatchRecordsWhatEachProcessHeld(t *testing.T) {
	if _, err := os.Stat("/proc/self/maps"); err != nil {
		t.Skip("no /proc")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	dir := t.TempDir()
	w := filepath.Join(dir, "model-00001-of-00002.safetensors")
	lib := filepath.Join(dir, "build", "libport.so")
	os.MkdirAll(filepath.Dir(lib), 0o755)
	os.WriteFile(w, []byte("weights"), 0o644)
	os.WriteFile(lib, []byte("0123456789abcdef"), 0o644)
	pidFile := filepath.Join(dir, "pid")
	script := "( exec 3<" + w + "; sleep 0.5 ) & python3 -c 'import mmap,time; f=open(\"" + lib + "\",\"rb\"); m=mmap.mmap(f.fileno(),0,access=mmap.ACCESS_READ); time.sleep(0.5)'; wait"
	stop := watchProcTreeIn(pidFile, dir)
	if out, err := exec.Command("bash", "-c", watchedCommand(pidFile, script)).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	tr := stop()
	var holder, mapper *ProcSeen
	for i, p := range tr.Processes {
		if len(p.Weights) > 0 {
			holder = &tr.Processes[i]
		}
		if len(p.WorkspaceLibs) > 0 {
			mapper = &tr.Processes[i]
		}
	}
	if holder == nil || mapper == nil || holder.PID == mapper.PID {
		t.Fatalf("processes not told apart: %+v", tr.Processes)
	}
	if mapper.WorkspaceLibs[0] != lib || !strings.Contains(mapper.Exe, "python") {
		t.Fatalf("mapper %+v", *mapper)
	}
}

// An interpreter running a file of the workspace is recorded with it, and so is a large file
// it opens (a pack the port converted).
func TestProcWatchRecordsWorkspaceScriptsAndLargeFiles(t *testing.T) {
	if _, err := os.Stat("/proc/self/maps"); err != nil {
		t.Skip("no /proc")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	dir := t.TempDir()
	pack := filepath.Join(t.TempDir(), "weights.bin")
	f, _ := os.Create(pack)
	f.Truncate(largeFileMin + 1)
	f.Close()
	script := filepath.Join(dir, "pytools", "ref_packer.py")
	os.MkdirAll(filepath.Dir(script), 0o755)
	os.WriteFile(script, []byte("import time\nf=open('"+pack+"','rb')\ntime.sleep(0.5)\n"), 0o644)
	pidFile := filepath.Join(dir, "pid")
	stop := watchProcTreeIn(pidFile, dir)
	if out, err := exec.Command("bash", "-c", watchedCommand(pidFile, "cd "+dir+" && python3 pytools/ref_packer.py")).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	tr := stop()
	for _, p := range tr.Processes {
		if len(p.WorkspaceArgs) > 0 {
			if p.WorkspaceArgs[0] != script || len(p.Large) != 1 || p.Large[0] != pack {
				t.Fatalf("%+v", p)
			}
			return
		}
	}
	t.Fatalf("the script was not recorded: %+v", tr.Processes)
}

// The port's conversion of the model written during the run (the reference's pytools write a
// GGUF of it) is not "another model"; a weight file outside the model older than the run is.
func TestAWeightFileWrittenDuringTheRunIsTheModelsConversion(t *testing.T) {
	model := t.TempDir()
	os.WriteFile(filepath.Join(model, "model-00001-of-00001.safetensors"), []byte("w"), 0o644)
	conv := filepath.Join(t.TempDir(), "qwen36.gguf")
	os.WriteFile(conv, []byte("converted"), 0o644)
	br := EvalBenchRun{Bench: EvalBench{File: "e2e.sh"}, Started: time.Now().Add(-time.Minute),
		Weights: []string{filepath.Join(model, "model-00001-of-00001.safetensors"), conv}}
	if k, txt := e2eModelGate(br, model); k != "" {
		t.Fatalf("the run's own conversion was another model: %s", txt)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(conv, old, old)
	if k, _ := e2eModelGate(br, model); k != "integration_e2e_other_model" {
		t.Fatalf("a weight file older than the run passed: %q", k)
	}
}
