package kernel

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestKernelNamesIn(t *testing.T) {
	src := `
// __global__ void commented_out(int*)
/* __global__ void also_commented(int*) */
__global__ void plain(float* x) {}
template <int N> __global__ void templated(const float* __restrict__ a, int n) {}
extern "C" __global__ void __launch_bounds__(256, 2) bounded(int* y) {}
static __global__ __launch_bounds__(128) void bounded2(int* y) {}
__global__ void plain(float* x);   // declaration of the same kernel
__device__ float helper(float v) { return v; }
__host__ __device__ int both(int v) { return v; }
`
	got := kernelNamesIn(src)
	want := []string{"bounded", "bounded2", "plain", "templated"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kernelNamesIn = %v, want %v", got, want)
	}
}

// 11/04: a .cuh named after GPU kernels held only host index arithmetic
func TestHostHeaderAboutKernelsIsNotAKernel(t *testing.T) {
	hdr := `// fused_gdn_kernels.cuh - index arithmetic, memory layout and value ranges of the fused GDN CUDA kernels
#pragma once
inline int gdn_row(int h, int d) { return h * 128 + d; }
constexpr int kGdnBlock = 256;`
	if got := kernelNamesIn(hdr); len(got) != 0 {
		t.Fatalf("host header reported kernels %v", got)
	}
}

func TestUnitKernelsReadsChangedCUDAFiles(t *testing.T) {
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, "src", "k"), 0o755)
	os.WriteFile(filepath.Join(ws, "src/k/a.cu"), []byte("__global__ void ka(int* p) {}\n"), 0o644)
	os.WriteFile(filepath.Join(ws, "src/k/b.cuh"), []byte("inline int idx(int i) { return i; }\n"), 0o644)
	os.WriteFile(filepath.Join(ws, "src/k/c.cpp"), []byte("__global__ void not_cuda(int*) {}\n"), 0o644)
	got := unitKernels(ws, []string{"src/k/a.cu", "src/k/b.cuh", "src/k/c.cpp", "src/k/missing.cu"})
	if !reflect.DeepEqual(got["src/k/a.cu"], []string{"ka"}) || len(got["src/k/b.cuh"]) != 0 {
		t.Fatalf("unitKernels = %v", got)
	}
	if _, ok := got["src/k/c.cpp"]; ok {
		t.Fatalf("a .cpp file was read as CUDA: %v", got)
	}
	if _, ok := got["src/k/missing.cu"]; ok {
		t.Fatalf("an unreadable file was reported: %v", got)
	}
}

func TestGateGPUKernelMissing(t *testing.T) {
	req := UnitRequirements{Sources: []string{"src/gdn.cu"}, GPUSources: []string{"src/gdn.cu"}}
	compiled := ProcTrace{Cmdlines: []string{"/usr/local/corex/bin/clang++ -x cuda --cuda-gpu-arch=ivcore10 -c include/baloon/gdn.cuh"},
		Exes: []string{"/usr/local/corex/bin/clang++"}}
	base := GateInput{Req: req, Changed: []string{"include/baloon/gdn.cuh"}, Build: compiled,
		Test: ProcTrace{Devices: []string{"/dev/iluvatar0"}}, Compilers: corex}

	in := base
	in.Kernels, in.KernelsMeasured = map[string][]string{"include/baloon/gdn.cuh": nil}, true
	kind, text := unitGates(in)
	if kind != "gpu_kernel_missing" || !strings.Contains(text, "include/baloon/gdn.cuh") {
		t.Fatalf("host-only CUDA header: kind=%q text=%q", kind, text)
	}

	in = base
	in.Kernels, in.KernelsMeasured = map[string][]string{"include/baloon/gdn.cuh": {"gdn_step"}}, true
	if kind, text := unitGates(in); strings.Contains(text, "gpu_kernel_missing") {
		t.Fatalf("a header with a kernel failed gpu_kernel_missing: kind=%q text=%q", kind, text)
	}

	// not measured (a hand-built input): the gate does not guess
	if _, text := unitGates(base); strings.Contains(text, "gpu_kernel_missing") {
		t.Fatalf("unmeasured kernels failed the gate: %q", text)
	}
}
