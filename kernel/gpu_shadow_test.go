package kernel

import (
	"strings"
	"testing"
	"time"
)

// 11/13 unit ua1c077067b's test file defined eleven kernels of the same names as its ports
// and launched those; the probe saw the names and the gate passed.
func TestATestThatRedefinesAPortedKernelDoesNotPass(t *testing.T) {
	realRunner(t)
	ws := t.TempDir()
	unit := "ua1c077067b"
	build, test := devVerifyScripts(unit)
	write(t, ws, build, "true")
	write(t, ws, test, "true")
	write(t, ws, "src/kernels/cuda/qsa.cu", "__global__ void kv_append_kernel(float* p, int n) { p[0] = 1; }\n")
	write(t, ws, PortMapPath(unit), "src/kernels/cuda/qsa.cu\tkv_append_kernel\tsrc/kernels/cuda/qsa.cu\tkv_append_kernel\n")
	req := UnitRequirements{
		Symbols:   []RefSymbol{{File: "src/kernels/cuda/qsa.cu", Kind: "kernel", Name: "kv_append_kernel"}},
		FileNames: map[string]map[string]bool{"src/kernels/cuda/qsa.cu": {"kv_append_kernel": true}},
	}
	changed := []string{"src/kernels/cuda/qsa.cu", "tests/ua1c077067b/test_gpu.cu", PortMapPath(unit)}
	write(t, ws, "tests/ua1c077067b/test_gpu.cu", "__global__ void kv_append_kernel(float* p, int n) {}\nint main() { return 0; }\n")
	r := runUnitVerifyWith(ws, unit, "", req, changed, time.Minute, time.Minute)
	if r.Passed() || r.Gate != "kernel_shadowed_in_test" || !strings.Contains(r.GateText, "tests/ua1c077067b/test_gpu.cu: kv_append_kernel") {
		t.Fatalf("gate %q %q", r.Gate, r.GateText)
	}
	// a test that calls the ported kernel instead defines none of its own
	write(t, ws, "tests/ua1c077067b/test_gpu.cu", "#include \"../../src/kernels/cuda/qsa.cu\"\nint main() { kv_append_kernel<<<1, 1>>>(nullptr, 0); return 0; }\n")
	if r := runUnitVerifyWith(ws, unit, "", req, changed, time.Minute, time.Minute); r.Gate == "kernel_shadowed_in_test" {
		t.Fatalf("a test that calls the ported kernel: %q", r.GateText)
	}
}
