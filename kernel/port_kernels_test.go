package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// When the port map names ported kernels, those are the unit's own kernels: a kernel of the
// unit's test file launched in their place does not pass gpu_kernel_not_launched.
func TestThePortedKernelsAreTheUnitsOwn(t *testing.T) {
	ws := t.TempDir()
	write := func(f, s string) {
		p := filepath.Join(ws, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
	}
	write("src/cu/s2_expert_grouped.cu", "__global__ void hit_select_kernel(const int* ids, int* out) {}\nvoid host_helper() {}\n")
	write("tests/k63_ube4/test_ube4.cu", "__global__ void partition_kernel(int* p) {}\n")
	write(PortMapPath("ube4f27df84"), "src/cu/s2_expert_grouped.cu\thit_select_kernel\tsrc/cu/s2_expert_grouped.cu\thit_select_kernel\nsrc/cu/s2_expert_grouped.cu\thost_helper\tsrc/cu/s2_expert_grouped.cu\thost_helper\n")
	pk := portKernels(ws, "ube4f27df84")
	if len(pk) != 1 || !pk["hit_select_kernel"] {
		t.Fatalf("ported kernels: %v", pk)
	}
	in := GateInput{Req: UnitRequirements{GPUSources: []string{"src/cu/s2_expert_grouped.cu"}}, Compilers: []string{"clang++"},
		Changed:         []string{"src/cu/s2_expert_grouped.cu", "tests/k63_ube4/test_ube4.cu"},
		Build:           ProcTrace{Exes: []string{"/usr/local/corex/bin/clang++"}, Cmdlines: []string{"/usr/local/corex/bin/clang++ -x cuda -c src/cu/s2_expert_grouped.cu", "/usr/local/corex/bin/clang++ -x cuda -c tests/k63_ube4/test_ube4.cu"}},
		Test:            ProcTrace{Devices: []string{"/dev/iluvatar0"}, ProbeLoaded: []string{"/ws/t"}, Launches: []KernelLaunch{{API: "cudaLaunchKernel", Name: "partition_kernel"}}},
		Kernels:         map[string][]string{"src/cu/s2_expert_grouped.cu": {"hit_select_kernel"}, "tests/k63_ube4/test_ube4.cu": {"partition_kernel"}},
		KernelsMeasured: true, PortKernels: pk}
	if _, text := unitGates(in); !strings.Contains(text, "gpu_kernel_not_launched") {
		t.Fatalf("the test file's kernel stood in for the port: %s", text)
	}
	in.Test.Launches = append(in.Test.Launches, KernelLaunch{API: "cudaLaunchKernel", Name: "hit_select_kernel"})
	if _, text := unitGates(in); strings.Contains(text, "gpu_kernel_not_launched") {
		t.Fatalf("the ported kernel ran: %s", text)
	}
}
