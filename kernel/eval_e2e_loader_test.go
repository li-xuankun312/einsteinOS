package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 11/21 03:01 cbb5dc6's e2e.sh: a python opened two shards, vllm loaded the model, and each
// unit's parity binary launched its kernels with no weights open. Every unit's kernels were
// launched and the weights were opened — by different processes, none of them the port's.
func TestTheProcessThatLoadsTheModelMustBeThePortLaunchingEveryUnitsKernel(t *testing.T) {
	perUnit := map[string][]string{
		"u7991572426": {"gdn_conv_kernel", "gdn_rec_kernel"},
		"uebd9b81357": {"kv_append_q8_kernel"},
	}
	ws := "/tmp/ws"
	launched := map[int][]string{
		301: {"gdn_conv_kernel"},     // build/gdn_parity --selftest
		302: {"kv_append_q8_kernel"}, // build/kv_q8_parity
	}
	collage := []ProcSeen{
		{PID: 200, Exe: "/usr/local/bin/python3.10", Weights: []string{"/tmp/model/model-00001-of-00026.safetensors", "/tmp/model/model-00002-of-00026.safetensors"}},
		{PID: 250, Exe: "/usr/local/bin/python3.10", Weights: []string{"/tmp/model/model-00003-of-00026.safetensors"}},
		{PID: 301, Exe: ws + "/build/gdn_parity"},
		{PID: 302, Exe: ws + "/build/kv_q8_parity"},
	}
	br := EvalBenchRun{Bench: EvalBench{File: "e2e.sh"}, Loaders: e2eLoaders(ws, collage, launched)}
	k, txt := e2eLoaderGate(br, perUnit)
	if k != "integration_e2e_loader_not_port" || !strings.Contains(txt, "pid 200 /usr/local/bin/python3.10: 2 weight file(s), the workspace's program: false") {
		t.Fatalf("the collage passed or was not described: %q %s", k, txt)
	}

	// the port's program loading the model and launching every unit's kernels passes
	port := []ProcSeen{{PID: 400, Exe: ws + "/build/generate", Weights: []string{"/tmp/model/model-00001-of-00026.safetensors"}}}
	br.Loaders = e2eLoaders(ws, port, map[int][]string{400: {"gdn_rec_kernel", "kv_append_q8_kernel", "other_kernel"}})
	if k, txt := e2eLoaderGate(br, perUnit); k != "" {
		t.Fatalf("the port's run failed: %s", txt)
	}
	// a python that maps a library the workspace built counts as the port's
	py := []ProcSeen{{PID: 500, Exe: "/usr/local/bin/python3.10", Weights: []string{"/tmp/model/a.safetensors"}, WorkspaceLibs: []string{ws + "/build/libport.so"}}}
	br.Loaders = e2eLoaders(ws, py, map[int][]string{500: {"gdn_conv_kernel", "kv_append_q8_kernel"}})
	if k, txt := e2eLoaderGate(br, perUnit); k != "" {
		t.Fatalf("a python running the workspace's library failed: %s", txt)
	}
	// the port's program that leaves a unit's kernels to another process does not pass
	br.Loaders = e2eLoaders(ws, port, map[int][]string{400: {"gdn_rec_kernel"}})
	if k, txt := e2eLoaderGate(br, perUnit); k == "" || !strings.Contains(txt, "uebd9b81357") {
		t.Fatalf("a unit whose kernels the loader did not launch passed: %q %s", k, txt)
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
