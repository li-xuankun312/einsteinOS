package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 11/21 12:28-13:59 a python copied 64 elements of one tensor of each shard to the GPU and a
// test binary mmapped one shard: each had "read the weights". What a process takes in of the
// model is measured in bytes, and a loaded model is most of them.
func TestALoadedModelIsMostOfItsBytes(t *testing.T) {
	const gb = int64(1) << 30
	perUnit := map[string][]string{"u1": {"k1"}}
	br := EvalBenchRun{Bench: EvalBench{File: "e2e.sh"}, ModelBytes: 66 * gb}
	stub := E2ELoader{PID: 1, Exe: "/ws/build/e2e_all_check", Port: true, Weights: 26, PortKernels: []string{"k1"}, TookBytes: 3 << 20}
	br.Loaders = []E2ELoader{stub}
	if k, txt := e2eModelTakenGate(br, "/tmp/model"); k != "integration_e2e_model_not_read" || !strings.Contains(txt, "0.00 GiB of the 66.00 GiB") {
		t.Fatalf("26 shards opened and 3 MiB taken passed: %q %s", k, txt)
	}
	if k, txt := e2eLoaderGate(br, perUnit); k != "integration_e2e_loader_not_port" || !strings.Contains(txt, "took in 0.00 GiB of the model's 66.00 GiB") || !strings.Contains(txt, "/proc/<pid>/io") {
		t.Fatalf("a runner that took 3 MiB of 66 GiB passed, or the text does not say what is measured: %q %s", k, txt)
	}
	loaded := stub
	loaded.TookBytes = 62 * gb
	br.Loaders = []E2ELoader{loaded}
	if k, txt := e2eModelTakenGate(br, "/tmp/model"); k != "" {
		t.Fatalf("62 of 66 GiB failed: %s", txt)
	}
	if k, txt := e2eLoaderGate(br, perUnit); k != "" {
		t.Fatalf("a runner that took the model failed: %s", txt)
	}
}

// The watcher counts what a process read and what of its weight mappings was resident.
func TestProcWatchMeasuresTheBytesTakenIn(t *testing.T) {
	if _, err := os.Stat("/proc/self/io"); err != nil {
		t.Skip("no /proc/<pid>/io")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	dir := t.TempDir()
	const size = 8 << 20
	read := filepath.Join(dir, "read.bin")
	mapped := filepath.Join(dir, "model-00001-of-00001.safetensors")
	os.WriteFile(read, make([]byte, size), 0o644)
	os.WriteFile(mapped, make([]byte, size), 0o644)
	script := filepath.Join(dir, "take.py")
	os.WriteFile(script, []byte(`import mmap,sys,time
open(sys.argv[1],'rb').read()
f=open(sys.argv[2],'rb'); m=mmap.mmap(f.fileno(),0,access=mmap.ACCESS_READ)
s=0
for i in range(0,len(m),4096): s+=m[i]
time.sleep(1.2)
`), 0o644)
	pidFile := filepath.Join(dir, "pid")
	stop := watchProcTreeIn(pidFile, dir)
	if out, err := exec.Command("bash", "-c", watchedCommand(pidFile, "python3 "+script+" "+read+" "+mapped)).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	tr := stop()
	for _, p := range tr.Processes {
		if len(p.Weights) > 0 {
			if p.ReadBytes < size || p.MappedWeightBytes < size*9/10 {
				t.Fatalf("read %d (want >= %d), mapped resident %d (want ~%d)", p.ReadBytes, size, p.MappedWeightBytes, size)
			}
			return
		}
	}
	t.Fatalf("the process was not recorded: %+v", tr.Processes)
}
