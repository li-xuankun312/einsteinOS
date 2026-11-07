package kernel

import (
	"os"
	"os/exec"
	"regexp"
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

var corex = []string{"/usr/local/corex/bin/clang++"}

// withFakeToolchain makes toolchainDirs a directory holding an executable clang++.
func withFakeToolchain(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(dir+"/clang++", []byte("#!/bin/bash\nsleep 0.3\n"), 0o755)
	old := toolchainDirs
	t.Cleanup(func() { toolchainDirs = old })
	toolchainDirs = []string{dir}
	return dir
}

var gpuReq = UnitRequirements{Sources: []string{"src/kernels/cuda/fused_gdn.cu"}, GPUSources: []string{"src/kernels/cuda/fused_gdn.cu"}}

// 2026/11/03 unit 6: FusedGdnKernels (reference: fused_gdn.cu) written as a host header,
// both scripts exit 0. That must not pass.
func TestAGPUUnitThatWritesNoCUDADoesNotPass(t *testing.T) {
	realRunner(t)
	ws := t.TempDir()
	withFakeToolchain(t)
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
	in := GateInput{Req: gpuReq, Changed: []string{"src/baloon/fused_gdn.cu", "include/baloon/fused_gdn.cuh"}, Build: ProcTrace{Cmdlines: existing}, Compilers: corex}
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
		in.Test.Devices = []string{"/dev/iluvatar0"}
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
	in = GateInput{Req: gpuReq, Changed: []string{"include/baloon/x.cuh"}, Test: ProcTrace{Devices: []string{"/dev/iluvatar1"}}, Compilers: corex}
	if k, _ := unitGates(in); k != "gpu_not_compiled" {
		t.Fatalf("header only, nothing compiled: %q", k)
	}
	in.Build.Cmdlines = existing
	if k, _ := unitGates(in); k != "" {
		t.Fatalf("header only, compiled through a .cu: %q", k)
	}
}

// 2026/11/03: every test ran on the CPU and exited 0. A GPU unit's test must open a device.
func TestAGPUUnitsTestMustRunOnTheGPU(t *testing.T) {
	compiled := ProcTrace{Cmdlines: []string{"clang++ -x cuda --cuda-gpu-arch=ivcore10 -c src/baloon/fused_gdn.cu"}}
	in := GateInput{Req: gpuReq, Changed: []string{"src/baloon/fused_gdn.cu"}, Build: compiled, Compilers: corex,
		Test: ProcTrace{Procs: 3, Exes: []string{"/bin/bash", "/tmp/baloon_unit6_build/test_unit6"}}}
	k, txt := unitGates(in)
	if k != "gpu_unused" || !strings.Contains(txt, "3 process(es)") || !strings.Contains(txt, "test_unit6") {
		t.Fatalf("%q %q", k, txt)
	}
	in.Test.Devices = []string{"/dev/iluvatar2"}
	if k, _ := unitGates(in); k != "" {
		t.Fatalf("opened a device: %q", k)
	}
	// a CPU unit need not touch a GPU
	if k, _ := unitGates(GateInput{Req: UnitRequirements{Sources: []string{"src/a.cpp"}}, Test: ProcTrace{Procs: 2}}); k != "" {
		t.Fatalf("cpu unit: %q", k)
	}
}

// end to end with real processes: the test script's child holds a device node open
func TestGPUUseIsMeasuredFromTheTestProcesses(t *testing.T) {
	realRunner(t)
	ws := t.TempDir()
	dev := ws + "/fakedev/iluvatar0"
	write(t, ws, "fakedev/iluvatar0", "")
	old := gpuDeviceRe
	t.Cleanup(func() { gpuDeviceRe = old })
	gpuDeviceRe = func() *regexp.Regexp { return regexp.MustCompile(regexp.QuoteMeta(ws) + `/fakedev/iluvatar[0-9]+$`) }

	build, test := devVerifyScripts("u6")
	write(t, ws, "bin/clang++", "#!/bin/bash\nsleep 0.3\n") // a compiler that takes as long as one
	os.Chmod(ws+"/bin/clang++", 0o755)
	oldDirs := toolchainDirs
	t.Cleanup(func() { toolchainDirs = oldDirs })
	toolchainDirs = []string{ws + "/bin"}
	write(t, ws, build, "bin/clang++ -x cuda --cuda-gpu-arch=ivcore10 -c src/baloon/k.cu")
	write(t, ws, test, "( exec 3<"+dev+"; sleep 0.3 )")
	r := runUnitVerifyWith(ws, "u6", "0", gpuReq, []string{"src/baloon/k.cu"}, time.Minute, time.Minute)
	// the device is measured from the child process; a shell holding it open launched no
	// kernel, which the launch gates (not gpu_unused) now judge
	if len(r.Test.Devices) != 1 || r.Status()["gpu_devices_opened"] != dev || r.Gate == "gpu_unused" {
		t.Fatalf("%+v", r)
	}
	if r.Passed() || r.Gate != "gpu_launch_unmeasured" {
		t.Fatalf("a device held by a shell passed or failed the wrong gate: %q %q", r.Gate, r.GateText)
	}
	write(t, ws, test, "sleep 0.3")
	r = runUnitVerifyWith(ws, "u6", "0", gpuReq, []string{"src/baloon/k.cu"}, time.Minute, time.Minute)
	if r.Passed() || r.Gate != "gpu_unused" {
		t.Fatalf("no device opened: %q %q", r.Gate, r.GateText)
	}
}

// 2026/11/03 in the sandbox: "corex toolchain not present on this host: skipping CUDA compile",
// then BUILD OK. A GPU unit on a machine without the toolchain does not pass.
func TestAGPUUnitWithoutTheToolchainDoesNotPass(t *testing.T) {
	full := GateInput{Req: gpuReq, Changed: []string{"src/a.cu"},
		Build: ProcTrace{Cmdlines: []string{"nvcc -c src/a.cu"}}, Test: ProcTrace{Devices: []string{"/dev/iluvatar0"}}}
	if k, txt := unitGates(full); k != GateToolchainMissing || !strings.Contains(txt, "clang++/nvcc") {
		t.Fatalf("%q %q", k, txt)
	}
	full.Compilers = corex
	if k, _ := unitGates(full); k != "" {
		t.Fatalf("with a toolchain: %q", k)
	}
	if k, _ := unitGates(GateInput{Req: UnitRequirements{Sources: []string{"a.cpp"}}}); k != "" {
		t.Fatalf("a CPU unit needs no GPU toolchain: %q", k)
	}
	// measured from the directories
	dir := t.TempDir()
	old := toolchainDirs
	t.Cleanup(func() { toolchainDirs = old })
	toolchainDirs = []string{dir}
	if got := presentGPUCompilers(); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	os.WriteFile(dir+"/nvcc", []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(dir+"/clang++", []byte("not executable"), 0o644)
	if got := presentGPUCompilers(); len(got) != 1 || got[0] != dir+"/nvcc" {
		t.Fatalf("%v", got)
	}
}

// compile builds a tiny program with g++ and the given flags; skips without g++/objdump.
func compileProbe(t *testing.T, dir, name, src string, flags ...string) string {
	t.Helper()
	for _, tool := range []string{"g++", "objdump"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	os.WriteFile(dir+"/"+name+".cpp", []byte(src), 0o644)
	out := dir + "/" + name
	args := append([]string{"-O2", "-o", out, dir + "/" + name + ".cpp"}, flags...)
	if b, err := exec.Command("g++", args...).CombinedOutput(); err != nil {
		t.Skipf("g++ %v: %v %s", flags, err, b)
	}
	return out
}

const avx2Src = `#include <immintrin.h>
#include <cstdio>
__attribute__((noinline)) int add8(const int* a, const int* b) {
#if defined(__AVX2__)
  __m256i v = _mm256_add_epi32(_mm256_loadu_si256((const __m256i*)a), _mm256_loadu_si256((const __m256i*)b));
  int o[8]; _mm256_storeu_si256((__m256i*)o, v); return o[0] + o[7];
#else
  return a[0] + b[0] + a[7] + b[7];
#endif
}
int main(int c, char**) { int a[8] = {c}, b[8] = {c}; std::printf("%d\n", add8(a, b)); }
`

const vnniSrc = `#include <immintrin.h>
#include <cstdio>
__attribute__((noinline)) int dot(const unsigned char* a, const signed char* b) {
  __m256i acc = _mm256_dpbusd_epi32(_mm256_setzero_si256(), _mm256_loadu_si256((const __m256i*)a), _mm256_loadu_si256((const __m256i*)b));
  int o[8]; _mm256_storeu_si256((__m256i*)o, acc); return o[0];
}
int main(int c, char**) { unsigned char a[32] = {(unsigned char)c}; signed char b[32] = {1}; std::printf("%d\n", dot(a, b)); }
`

// 2026/11/03 unit 4: built with -std=c++17 -O2 only — the AVX2 path compiled out, the
// "VNNI kernel" an integer emulation. The reference builds these files with -mavx2…/-mavx512vnni.
func TestTheReferencesISAPathMustBeInWhatWasBuilt(t *testing.T) {
	dir := t.TempDir()
	req := UnitRequirements{Sources: []string{"src/kernels/cpu/q2_avx2.cpp"}, ISAFlags: []string{"-mavx2", "-mf16c", "-mfma"},
		FlagFiles: map[string][]string{"src/kernels/cpu/q2_avx2.cpp": {"-mavx2", "-mf16c", "-mfma"}}}
	scalar := compileProbe(t, dir, "test_unit4", avx2Src, "-std=c++17")
	in := GateInput{Req: UnitRequirements{Sources: req.Sources, ISAFlags: []string{"-mavx2"}, FlagFiles: map[string][]string{"src/kernels/cpu/q2_avx2.cpp": {"-mavx2"}}},
		Build: ProcTrace{Cmdlines: []string{"/usr/local/corex/bin/clang++ -std=c++17 -O2 -Wall -Iinclude tests/unit4/a.cpp -o " + scalar}}}
	k, txt := unitGates(in)
	if k != "isa_path_missing" || !strings.Contains(txt, "q2_avx2.cpp") || !strings.Contains(txt, "test_unit4") {
		t.Fatalf("%q %q", k, txt)
	}
	avx2 := compileProbe(t, dir, "test_unit4_avx2", avx2Src, "-std=c++17", "-mavx2")
	in.Build.Cmdlines = []string{"g++ -O2 -mavx2 x.cpp -o " + avx2}
	if k, txt := unitGates(in); k != "" {
		t.Fatalf("built with -mavx2: %q %q", k, txt)
	}
	// VNNI: an integer emulation has no vpdpbusd; the intrinsic does
	vreq := UnitRequirements{Sources: []string{"src/kernels/cpu/expert.cpp"}, ISAFlags: []string{"-mavx512vnni"},
		FlagFiles: map[string][]string{"src/kernels/cpu/expert.cpp": {"-mavx512vnni"}}}
	in = GateInput{Req: vreq, Build: ProcTrace{Cmdlines: []string{"g++ -O2 x.cpp -o " + scalar}}}
	if k, _ := unitGates(in); k != "isa_path_missing" {
		t.Fatalf("emulation passed: %q", k)
	}
	vnni := compileProbe(t, dir, "test_vnni", vnniSrc, "-std=c++17", "-mavx512vnni", "-mavx512vl", "-mavx2")
	in.Build.Cmdlines = []string{"g++ -O2 -mavx512vnni x.cpp -o " + vnni}
	if k, txt := unitGates(in); k != "" {
		t.Fatalf("vpdpbusd present: %q %q", k, txt)
	}
	// a binary the test ran counts too, a system one does not
	in.Build.Cmdlines = nil
	in.Test.Exes = []string{"/usr/bin/bash", vnni}
	if k, _ := unitGates(in); k != "" {
		t.Fatalf("executed binary: %q", k)
	}
	// nothing could be disassembled: said so, not passed
	old := objdumpFn
	t.Cleanup(func() { objdumpFn = old })
	objdumpFn = func(string) (string, error) { return "", exec.ErrNotFound }
	if k, txt := unitGates(in); k != "isa_unmeasured" || !strings.Contains(txt, "objdump") {
		t.Fatalf("%q %q", k, txt)
	}
	// flags the kernel cannot attest by an instruction are not held against the unit
	if got := attestableISA([]string{"-mavx512bw", "-mavx2", "-march=native"}); len(got) != 1 || got[0] != "-mavx2" {
		t.Fatalf("%v", got)
	}
}
