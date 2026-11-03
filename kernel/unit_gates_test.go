package kernel

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// realRunner runs the verify commands with bash for real, so the process watcher sees them.
func realRunner(t *testing.T) {
	t.Helper()
	old := devVerifyRunner
	t.Cleanup(func() { devVerifyRunner = old })
	devVerifyRunner = func(cmd, dir string, to time.Duration) (int, string, error) {
		c := exec.Command("bash", "-c", cmd)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), string(out), nil
		}
		return 0, string(out), err
	}
}

var gpuReq = UnitRequirements{Sources: []string{"src/kernels/cuda/fused_gdn.cu"}, GPUSources: []string{"src/kernels/cuda/fused_gdn.cu"}}

// 2026/11/03 unit 6: FusedGdnKernels (reference: fused_gdn.cu) written as a host header,
// both scripts exit 0. That must not pass.
func TestAGPUUnitThatWritesNoCUDADoesNotPass(t *testing.T) {
	realRunner(t)
	ws := t.TempDir()
	build, test := devVerifyScripts("u433ffdf2e4")
	write(t, ws, build, "sleep 0.2") // as long as a real build at least: the watcher sees live processes
	write(t, ws, test, "sleep 0.2")
	changed := []string{"include/baloon/fused_gdn_kernels.hpp", "tests/unit6/test_gdn.cpp"}

	r := runUnitVerifyWith(ws, "u433ffdf2e4", "", gpuReq, changed, time.Minute, time.Minute)
	if r.Compile != "pass" || r.Smoke != "pass" {
		t.Fatalf("scripts: %+v", r)
	}
	if r.Passed() || r.Gate != "gpu_code_missing" || !strings.Contains(r.GateText, "src/kernels/cuda/fused_gdn.cu") {
		t.Fatalf("gate %q %q", r.Gate, r.GateText)
	}
	if st := r.Status(); st["gate"] != "gpu_code_missing" || st["gate_error"] == "" {
		t.Fatalf("status %v", st)
	}
	// the same unit with no GPU source in the reference has nothing to answer for
	if r := runUnitVerifyWith(ws, "u433ffdf2e4", "", UnitRequirements{Sources: []string{"src/x.cpp"}}, changed, time.Minute, time.Minute); !r.Passed() {
		t.Fatalf("cpu unit: %+v", r)
	}
	// and the scripts' processes were watched
	if r.Build.Procs == 0 || r.Test.Procs == 0 {
		t.Fatalf("not watched: build %d test %d processes", r.Build.Procs, r.Test.Procs)
	}
}

func TestChangedGPUFiles(t *testing.T) {
	got := changedGPUFiles([]string{"src/a.cu", "include/b.cuh", "include/c.hpp", "src/d.cu.txt"})
	if strings.Join(got, ",") != "src/a.cu,include/b.cuh" {
		t.Fatalf("%v", got)
	}
}

// 2026/11/03: the builds compiled only the repository's existing src/*.cu for ivcore10.
// A unit that writes its own .cu must be the one compiled.
func TestTheUnitsOwnCUDAMustBeCompiled(t *testing.T) {
	existing := []string{"/usr/local/corex/bin/clang++ -x cuda --cuda-gpu-arch=ivcore10 -c src/ix_gdn.cu -o /tmp/o/ix_gdn.cu.o",
		"/usr/local/corex/bin/clang++ -x cuda --cuda-gpu-arch=ivcore10 -c tests/test_gdn.cu -o /tmp/o/test_gdn.cu.o"}
	in := GateInput{Req: gpuReq, Changed: []string{"src/baloon/fused_gdn.cu", "include/baloon/fused_gdn.cuh"}, Build: ProcTrace{Cmdlines: existing}}
	if k, txt := unitGates(in); k != "gpu_not_compiled" || !strings.Contains(txt, "src/baloon/fused_gdn.cu") || !strings.Contains(txt, "src/ix_gdn.cu") {
		t.Fatalf("%q %q", k, txt)
	}
	// compiled — by relative path, by basename after a cd, or by nvcc
	for _, c := range []string{
		"/usr/local/corex/bin/clang++ -x cuda --cuda-gpu-arch=ivcore10 -c src/baloon/fused_gdn.cu -o x.o",
		"clang++ --cuda-gpu-arch=ivcore10 -c fused_gdn.cu",
		"/usr/local/corex/bin/nvcc -c /tmp/ws/src/baloon/fused_gdn.cu",
	} {
		in.Build.Cmdlines = append(existing, c)
		if k, txt := unitGates(in); k != "" {
			t.Fatalf("%s: %q %q", c, k, txt)
		}
	}
	// the host compiler naming the file is not a CUDA compile
	in.Build.Cmdlines = []string{"g++ -std=c++17 -c src/baloon/fused_gdn.cu"}
	if k, _ := unitGates(in); k != "gpu_not_compiled" {
		t.Fatalf("g++ counted as a CUDA compile: %q", k)
	}
	// only a .cuh changed: some CUDA compile must have run
	in = GateInput{Req: gpuReq, Changed: []string{"include/baloon/x.cuh"}}
	if k, _ := unitGates(in); k != "gpu_not_compiled" {
		t.Fatalf("header only, nothing compiled: %q", k)
	}
	in.Build.Cmdlines = existing
	if k, _ := unitGates(in); k != "" {
		t.Fatalf("header only, compiled through a .cu: %q", k)
	}
}
