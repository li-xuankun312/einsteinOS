package kernel

import (
	"strings"
	"testing"
)

func launchGateInput() GateInput {
	req := UnitRequirements{Sources: []string{"src/gdn.cu"}, GPUSources: []string{"src/gdn.cu"}}
	return GateInput{Req: req, Changed: []string{"src/baloon/gdn.cu"}, Compilers: corex,
		Build: ProcTrace{Cmdlines: []string{"/usr/local/corex/bin/clang++ -x cuda --cuda-gpu-arch=ivcore10 -c src/baloon/gdn.cu"},
			Exes: []string{"/usr/local/corex/bin/clang++"}},
		Kernels: map[string][]string{"src/baloon/gdn.cu": {"gdn_step"}}, KernelsMeasured: true,
		Test: ProcTrace{Devices: []string{"/dev/iluvatar0"}, ProbeLoaded: []string{"/ws/build/test_gdn"}}}
}

// 11/04: the device was opened and the checks ran on the CPU
func TestGateKernelNotLaunched(t *testing.T) {
	in := launchGateInput()
	in.Test.Launches = []KernelLaunch{{Name: "repo_old_kernel"}}
	kind, text := unitGates(in)
	if kind != "gpu_kernel_not_launched" || !strings.Contains(text, "gdn_step") || !strings.Contains(text, "repo_old_kernel") {
		t.Fatalf("kind=%q text=%q", kind, text)
	}
	in.Test.Launches = append(in.Test.Launches, KernelLaunch{Name: "gdn_step"})
	if _, text := unitGates(in); strings.Contains(text, "gpu_kernel_not_launched") {
		t.Fatalf("launched kernel still failed: %q", text)
	}
}

func TestGateKernelNotLaunchedNeedsAMeasurement(t *testing.T) {
	in := launchGateInput()
	in.Test.ProbeLoaded = nil // the probe was in no process: nothing to judge on here
	if _, text := unitGates(in); strings.Contains(text, "gpu_kernel_not_launched") {
		t.Fatalf("judged without a measurement: %q", text)
	}
	in = launchGateInput()
	in.Test.ProbeErr = "no C compiler"
	if _, text := unitGates(in); strings.Contains(text, "gpu_kernel_not_launched") {
		t.Fatalf("judged with a failed probe: %q", text)
	}
}

func TestLaunchesText(t *testing.T) {
	if got := launchesText(nil); got != "none" {
		t.Fatalf("got %q", got)
	}
	if got := launchesText([]KernelLaunch{{Name: "b"}, {Name: "a"}, {API: "cuLaunchKernel"}}); got != "a b (+1 unnamed)" {
		t.Fatalf("got %q", got)
	}
}

func TestLaunchUnmeasured(t *testing.T) {
	if got := launchUnmeasured(ProcTrace{ProbeErr: "no C compiler"}); !strings.Contains(got, "no C compiler") {
		t.Fatalf("probe error: %q", got)
	}
	if got := launchUnmeasured(ProcTrace{}); got != "" {
		t.Fatalf("unprobed trace judged: %q", got)
	}
	if got := launchUnmeasured(ProcTrace{ProbeLoaded: []string{"/bin/true"}, Devices: []string{"/dev/iluvatar0"}, Launches: []KernelLaunch{{Name: "k"}}}); got != "" {
		t.Fatalf("measured launches judged unmeasured: %q", got)
	}
}

// a test binary with the runtime linked in: its launches never reach the probe
func TestGateLaunchUnmeasuredStaticRuntime(t *testing.T) {
	dir := t.TempDir()
	fakeCudaProgram(t, dir) // writes rt.c and main.c
	static := dir + "/static_test"
	if out, err := execCC("-o", static, dir+"/main.c", dir+"/rt.c"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	in := launchGateInput()
	in.Test.ProbeLoaded = []string{"/usr/bin/bash", static}
	kind, text := unitGates(in)
	if kind != "gpu_launch_unmeasured" || !strings.Contains(text, "static_test") || !strings.Contains(text, "statically") {
		t.Fatalf("kind=%q text=%q", kind, text)
	}
	if strings.Contains(text, "gpu_kernel_not_launched") {
		t.Fatalf("an unmeasured test was also judged as launching nothing: %q", text)
	}
}

func TestRepoKernelsSkipsGitAndBuild(t *testing.T) {
	ws := t.TempDir()
	for f, src := range map[string]string{
		"src/a.cu":        "__global__ void ka(int*) {}",
		"include/b.cuh":   "__global__ void kb(int*);",
		"build/gen.cu":    "__global__ void generated(int*) {}",
		".git/x.cu":       "__global__ void inside_git(int*) {}",
		"src/host.cpp":    "__global__ void not_cuda(int*) {}",
	} {
		writeFileAll(t, ws+"/"+f, src)
	}
	got := repoKernels(ws)
	if !got["ka"] || !got["kb"] || len(got) != 2 {
		t.Fatalf("repoKernels = %v", got)
	}
}

// 11/06: the integration unit's 28 tests passed with its wrappers running unit invariants
func TestGateIntegrationKernelNotLaunched(t *testing.T) {
	in := GateInput{Req: UnitRequirements{Tests: []RefTest{{Name: "gdn_parity"}, {Name: "pool_test"}}, RequireGPU: true},
		Compilers: corex, Build: ProcTrace{Exes: []string{"/usr/local/corex/bin/clang++"}},
		Test:        ProcTrace{Devices: []string{"/dev/iluvatar0"}, ProbeLoaded: []string{"/ws/build/gdn_parity"}, Launches: []KernelLaunch{{API: "cuLaunchKernel"}}},
		RepoKernels: map[string]bool{"gdn_step": true, "rope_apply": true}}
	kind, text := unitGates(in)
	if kind != "integration_kernel_not_launched" || !strings.Contains(text, "gdn_step") {
		t.Fatalf("kind=%q text=%q", kind, text)
	}
	in.Test.Launches = append(in.Test.Launches, KernelLaunch{Name: "rope_apply"})
	if _, text := unitGates(in); strings.Contains(text, "integration_kernel_not_launched") {
		t.Fatalf("a launched repository kernel still failed: %q", text)
	}
}
