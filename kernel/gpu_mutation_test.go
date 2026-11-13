package kernel

import (
	"strings"
	"testing"
	"time"
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

// 11/13 unit u7663fc74b8's test returned 0 on cudaSuccess alone: rebuilt with
// router_top10_kernel's body skipped it still passes, and that is what is measured. A test
// that reads what the kernel wrote fails on the skipped body.
func TestATestThatOnlyChecksTheLaunchIsFound(t *testing.T) {
	realRunner(t)
	ws := t.TempDir()
	unit := "u7663fc74b8"
	build, test := devVerifyScripts(unit)
	write(t, ws, build, "true")
	write(t, ws, "src/kernels/cuda/router_top10.cu", "__global__ void router_top10_kernel(int* idx) {\n    idx[0] = 1;\n}\n")
	write(t, ws, PortMapPath(unit), "src/kernels/cuda/router_top10.cu\trouter_top10_kernel\tsrc/kernels/cuda/router_top10.cu\trouter_top10_kernel\n")
	own := map[string]bool{"router_top10_kernel": true}
	files := kernelFiles(ws, unit, own, nil, own)
	if len(files["router_top10_kernel"]) != 1 {
		t.Fatalf("%v", files)
	}
	// "runs" the kernel: the result is there only if the body is not skipped
	checks := "! grep -q 'body skipped' src/kernels/cuda/router_top10.cu"
	for name, c := range map[string]struct {
		script    string
		unchecked bool
	}{"launch only": {"true", true}, "reads the result": {checks, false}} {
		write(t, ws, test, c.script)
		unchecked, inconclusive := resultUnchecked(ws, build, test, "", files, time.Minute, time.Minute)
		if (len(unchecked) == 1) != c.unchecked || len(inconclusive) != 0 {
			t.Fatalf("%s: unchecked %v inconclusive %v", name, unchecked, inconclusive)
		}
	}
	// a build that breaks with the body skipped is not judged
	write(t, ws, build, "! grep -q 'body skipped' src/kernels/cuda/router_top10.cu")
	write(t, ws, test, "true")
	if u, inc := resultUnchecked(ws, build, test, "", files, time.Minute, time.Minute); len(u) != 0 || len(inc) != 1 {
		t.Fatalf("unchecked %v inconclusive %v", u, inc)
	}
}
