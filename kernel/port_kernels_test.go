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

// A unit whose port map names kernels it ports, in files the branch already held, is not sent
// back gpu_kernel_missing because the CUDA files it wrote this time define none.
func TestPortedKernelsInExistingFilesAreTheUnitsKernels(t *testing.T) {
	in := GateInput{Req: UnitRequirements{GPUSources: []string{"src/kernels/cuda/native_flash_attn.cu"}},
		Kernels: map[string][]string{"tests/kucc7_u4/test_ucc7.cu": nil}, KernelsMeasured: true}
	if _, text := unitGates(in); !strings.Contains(text, "gpu_kernel_missing") {
		t.Fatalf("no kernel anywhere: %s", text)
	}
	in.PortKernels = map[string]bool{"flash_kernel": true, "qsa_attend_kernel": true}
	if _, text := unitGates(in); strings.Contains(text, "gpu_kernel_missing") {
		t.Fatalf("its ported kernels are its kernels: %s", text)
	}
}

// gpu_kernel_missing says how many kernels the unit's port map names and which kernels its
// test launched: 11/10 02:07 unit u61716a9a8a's test launched eleven kernels of an existing
// file that its port map did not yet name.
func TestKernelMissingSaysWhatThePortMapAndTheTestShow(t *testing.T) {
	in := GateInput{UnitID: "u61716a9a8a", Req: UnitRequirements{GPUSources: []string{"src/kernels/cuda/verify_kernels.cu"}},
		Kernels: map[string][]string{"tests/ku617/test_u617.cu": nil}, KernelsMeasured: true,
		Test: ProcTrace{Launches: []KernelLaunch{{Name: "mtp_select_kernel"}, {Name: "dense_steps_kernel"}}}}
	_, text := unitGates(in)
	for _, want := range []string{".einsteinOS/port/u61716a9a8a.tsv names: 0", "dense_steps_kernel", "mtp_select_kernel"} {
		if !strings.Contains(text, want) {
			t.Fatalf("lacks %q: %s", want, text)
		}
	}
}

// The verification's gate input carries the port map's kernels: unit u61716a9a8a's files as
// zeng-bin3123/baloon b0c60cf holds them (its port map, the existing file its kernels are in,
// and the file it added), through unitGateInput, the function the verification calls.
func TestTheVerificationGivesTheGatesThePortMapsKernels(t *testing.T) {
	ws, err := filepath.Abs("testdata/u617")
	if err != nil {
		t.Fatal(err)
	}
	req := UnitRequirements{GPUSources: []string{"src/kernels/cuda/verify_kernels.cu"}}
	changed := []string{".einsteinOS/port/u61716a9a8a.tsv", "tests/ku617/test_u617.cu"}
	test := ProcTrace{Devices: []string{"/dev/iluvatar0"}, ProbeLoaded: []string{"/ws/build/test_u617"},
		Launches: []KernelLaunch{{API: "cudaLaunchKernel", Name: "dense_steps_kernel"}, {API: "cudaLaunchKernel", Name: "mtp_select_kernel"}}}
	in := unitGateInput(ws, "u61716a9a8a", req, changed, ProcTrace{}, test, NoToolchainBuild{})
	if len(in.PortKernels) != 15 || !in.PortKernels["mtp_select_kernel"] || in.UnitID != "u61716a9a8a" {
		t.Fatalf("port kernels %d %v, unit %q", len(in.PortKernels), in.PortKernels, in.UnitID)
	}
	if _, text := unitGates(in); strings.Contains(text, "gpu_kernel_missing") || strings.Contains(text, "gpu_kernel_not_launched") {
		t.Fatalf("its ported kernels exist and launched: %s", text)
	}
}

// A kernel in a file outside the tests that the unit's port map writes nothing into fails the
// unit: unit u61716a9a8a's src/kernels/cuda/verify_commit_dev.cu (baloon b0c60cf), through the
// verification's own gate input; its test file's kernels do not.
func TestAKernelInAFileThePortMapDoesNotWriteFails(t *testing.T) {
	ws, _ := filepath.Abs("testdata/u617")
	req := UnitRequirements{GPUSources: []string{"src/kernels/cuda/verify_kernels.cu"}}
	test := ProcTrace{Devices: []string{"/dev/iluvatar0"}, ProbeLoaded: []string{"/ws/t"}, Launches: []KernelLaunch{{Name: "mtp_select_kernel"}}}
	changed := []string{".einsteinOS/port/u61716a9a8a.tsv", "src/kernels/cuda/verify_commit_dev.cu"}
	_, text := unitGates(unitGateInput(ws, "u61716a9a8a", req, changed, ProcTrace{}, test, NoToolchainBuild{}))
	if !strings.Contains(text, "kernel_unported") || !strings.Contains(text, "accept_prefix_kernel") || !strings.Contains(text, "commit_rows_kernel") {
		t.Fatalf("%s", text)
	}
	in := unitGateInput(ws, "u61716a9a8a", req, []string{".einsteinOS/port/u61716a9a8a.tsv"}, ProcTrace{}, test, NoToolchainBuild{})
	in.Kernels = map[string][]string{"tests/ku617/test_u617.cu": {"probe_kernel"}}
	if _, text := unitGates(in); strings.Contains(text, "kernel_unported") {
		t.Fatalf("a test file's kernel: %s", text)
	}
}
