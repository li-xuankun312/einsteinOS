package kernel

import (
	"strings"
	"testing"
)

func TestAKernelsBodyIsSkippedAndItsDeclarationLeft(t *testing.T) {
	src := `__global__ void router_top10_kernel(const float* l, int n, int* idx);
template <int K>
__global__ void __launch_bounds__(512) router_top10_kernel(const float* l, int n = (1 + 2), int* idx)
{
    idx[0] = 1;
}
__global__ void other_kernel(int* p) { p[0] = 2; }
`
	got, n := skipKernelBodies(src, "router_top10_kernel")
	if n != 1 || !strings.Contains(got, "int* idx)\n{"+kernelSkipStmt+"\n    idx[0] = 1;") {
		t.Fatalf("%d\n%s", n, got)
	}
	if !strings.Contains(got, "int n, int* idx);") || !strings.Contains(got, "other_kernel(int* p) { p[0] = 2; }") {
		t.Fatalf("declaration or another kernel changed:\n%s", got)
	}
	if _, n := skipKernelBodies(src, "missing_kernel"); n != 0 {
		t.Fatal(n)
	}
}
