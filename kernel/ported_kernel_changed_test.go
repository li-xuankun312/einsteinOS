package kernel

import (
	"strings"
	"testing"
)

// 11/26 the integration conversation rewrote rms_norm_weighted_kernel, pcd027bc02f's port, to sum
// in double, and eight verifications failed bf16_gemv_parity with nothing saying that a kernel
// another unit ported differed from what that unit verified
func TestAPortedKernelTheIntegrationUnitChangedIsSaid(t *testing.T) {
	mirror := t.TempDir()
	branch := "__global__ void rms_norm_weighted_kernel(float* x) {\n    float acc = 0.f;\n    if (x) { acc += 1; }\n}\n__global__ void silu_kernel(float* x) { }\n"
	now := "__global__ void rms_norm_weighted_kernel(float* x) {\n    double acc = 0.0;\n    if (x) { acc += 1; }\n}\n__global__ void silu_kernel(float* x) { }\n__global__ void rms_norm_bf16w_inplace_kernel(float* x) { }\n"
	putTree(t, mirror, map[string]string{
		".einsteinOS/port/pcd027bc02f.tsv": "src/kernels/cuda/elementwise.cu\trms_norm_kernel\tsrc/kernels/gpu/ew_ops.cu\trms_norm_weighted_kernel\n" +
			"src/kernels/cuda/elementwise.cu\tsilu_kernel\tsrc/kernels/gpu/ew_ops.cu\tsilu_kernel\n",
		"src/kernels/gpu/ew_ops.cu": now,
	})
	got := portedKernelsChanged(mirror, "uint", func(f string) ([]byte, bool) {
		if f == "src/kernels/gpu/ew_ops.cu" {
			return []byte(branch), true
		}
		return nil, false
	})
	if len(got) != 1 || got[0] != "src/kernels/gpu/ew_ops.cu rms_norm_weighted_kernel" {
		t.Fatalf("got %q", got)
	}
	if s := portedChangedText(got); !strings.Contains(s, "1 kernel(s) another unit's port map writes are defined otherwise than on that branch: src/kernels/gpu/ew_ops.cu rms_norm_weighted_kernel") {
		t.Fatal(s)
	}
	if portedChangedText(nil) != "" {
		t.Fatal("none and something said")
	}
	if kernelDefinitionText(branch, "rms_norm_weighted_kernel") != "rms_norm_weighted_kernel(float* x) {\n    float acc = 0.f;\n    if (x) { acc += 1; }\n}" {
		t.Fatalf("%q", kernelDefinitionText(branch, "rms_norm_weighted_kernel"))
	}
}
