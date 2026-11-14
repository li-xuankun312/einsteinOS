package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A reference __global__ kernel is measured as kind "kernel", and its counterpart must be a
// __global__ kernel of a .cu/.cuh file: a host header "about" the kernel (11/03 FusedGdnKernels:
// "index arithmetic … of the … CUDA kernels") does not port it.
func TestPortKernelMustBeKernel(t *testing.T) {
	ref := "#include <cuda_runtime.h>\n__global__ void gdn_step(float* s, int n) { }\nvoid launch_gdn(float* s, int n) { gdn_step<<<1,32>>>(s, n); }\n"
	syms := SymbolsOf("src/kernels/cuda/gdn.cu", ref)
	kinds := map[string]string{}
	for _, s := range syms {
		kinds[s.Name] = s.Kind
	}
	if kinds["gdn_step"] != "kernel" || kinds["launch_gdn"] != "func" {
		t.Fatalf("kinds measured from the reference: %v", kinds)
	}
	if got := SymbolsOf("src/x.cpp", "void gdn_step(float* s, int n) { }\n"); got[0].Kind != "func" {
		t.Fatalf("a .cpp function is not a kernel: %v", got)
	}

	ws := t.TempDir()
	write := func(rel, text string) {
		p := filepath.Join(ws, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("include/baloon/fused_gdn_kernels.hpp", "// index arithmetic of the CUDA kernels\ninline int gdn_step(int h) { return h * 128; }\ninline void launch_gdn(float*, int) {}\n")
	write("src/gdn.cu", "__global__ void gdn_step(float* s, int n) { }\nvoid launch_gdn(float* s, int n) { gdn_step<<<1,32>>>(s, n); }\n")

	host := "src/kernels/cuda/gdn.cu\tgdn_step\tinclude/baloon/fused_gdn_kernels.hpp\tgdn_step\nsrc/kernels/cuda/gdn.cu\tlaunch_gdn\tinclude/baloon/fused_gdn_kernels.hpp\tlaunch_gdn\n"
	write(PortMapPath("u1"), host)
	miss := portMissing(ws, "u1", syms)
	if len(miss) != 1 || !strings.Contains(miss[0], "not as a __global__ kernel") || !strings.Contains(miss[0], "gdn_step") {
		t.Fatalf("a kernel ported as a host function must be missing (and only it): %q", miss)
	}

	write(PortMapPath("u1"), "src/kernels/cuda/gdn.cu\tgdn_step\tsrc/gdn.cu\tgdn_step\nsrc/kernels/cuda/gdn.cu\tlaunch_gdn\tsrc/gdn.cu\tlaunch_gdn\n")
	if miss := portMissing(ws, "u1", syms); len(miss) != 0 {
		t.Fatalf("a kernel ported as a kernel passes: %q", miss)
	}
}
