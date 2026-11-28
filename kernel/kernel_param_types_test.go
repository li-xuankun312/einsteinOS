package kernel

import (
	"strings"
	"testing"
)

func TestKernelParamTypesReadsTypesWithoutNames(t *testing.T) {
	src := "// __global__ void gdn_step_kernel(int no)\n__global__ void __launch_bounds__(256) gdn_step_kernel(float* __restrict__ state, const float* __restrict__ q,\n    int cols, long long n, unsigned int, const int32_t* table = nullptr) {}\n__global__ void none() {}\n"
	got, ok := kernelParamTypes(src, "gdn_step_kernel")
	if !ok || strings.Join(got, "|") != "float*|const float*|int|long long|unsigned int|const int32_t*" {
		t.Fatalf("%v %q", ok, got)
	}
	if got, ok := kernelParamTypes(src, "none"); !ok || len(got) != 0 {
		t.Fatalf("%v %q", ok, got)
	}
	if _, ok := kernelParamTypes(src, "absent_kernel"); ok {
		t.Fatal("found")
	}
}
