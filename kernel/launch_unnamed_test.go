package kernel

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Launches that cannot be named leave which kernels ran unmeasured, and say why: a stripped
// executable has no symbol table to name its stubs from. 11/09 12:02-12:29 a unit was sent
// back gpu_kernel_not_launched three times on "(+7 unnamed)" launches.
func TestUnnamedLaunchesAreUnmeasuredAndSayWhy(t *testing.T) {
	if _, err := exec.LookPath("strip"); err != nil {
		t.Skip("no strip")
	}
	withProbeDir(t)
	dir := t.TempDir()
	prog := fakeCudaProgram(t, dir)
	lib, err := launchProbeLib()
	if err != nil {
		t.Fatal(err)
	}
	run := func(p string) []KernelLaunch {
		logPath := filepath.Join(t.TempDir(), "launch.log")
		if out, err := exec.Command("bash", "-c", launchProbeEnv(lib, logPath)+shq(p)).CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return resolveLaunches(readProbeLog(logPath))
	}
	named := run(prog)
	if len(launchedNames(named)) != 2 {
		t.Fatalf("the unstripped program's kernels are named: %+v", named)
	}
	trace := ProcTrace{Devices: []string{"/dev/iluvatar0"}, ProbeLoaded: []string{prog}, Launches: named}
	if u := launchUnmeasured(trace); u != "" {
		t.Fatalf("named launches are measured: %s", u)
	}

	stripped := filepath.Join(dir, "fake_test_stripped")
	if out, err := exec.Command("strip", "--strip-all", "-o", stripped, prog).CombinedOutput(); err != nil {
		t.Fatalf("strip: %v %s", err, out)
	}
	ls := run(stripped)
	if len(ls) != 2 || len(launchedNames(ls)) != 0 {
		t.Fatalf("the stripped program's launches have no names: %+v", ls)
	}
	trace = ProcTrace{Devices: []string{"/dev/iluvatar0"}, ProbeLoaded: []string{stripped}, Launches: ls}
	u := launchUnmeasured(trace)
	if !strings.Contains(u, "none could be named") || !strings.Contains(u, "fake_test_stripped") {
		t.Fatalf("unmeasured: %q", u)
	}
	kind, text := unitGates(GateInput{Req: UnitRequirements{RequireGPU: true}, Test: trace})
	if kind != "gpu_launch_unmeasured" || strings.Contains(text, "gpu_kernel_not_launched") {
		t.Fatalf("%s: %s", kind, text)
	}
}

// The verification log line says why launches had no name, with a count per reason.
func TestTheLogSaysWhyLaunchesHadNoName(t *testing.T) {
	tr := ProcTrace{ProbeLoaded: []string{"/ws/t"}, Launches: []KernelLaunch{
		{API: "cudaLaunchKernel", Name: "moe_combine_rows_kernel"},
		{API: "cudaLaunchKernel", Why: "/ws/libk.so has no symbol table (stripped?)"},
		{API: "cudaLaunchKernel", Why: "/ws/libk.so has no symbol table (stripped?)"},
		{API: "cuLaunchKernel", Why: "a driver-API launch (cuLaunchKernel) passes a handle, not a function"},
	}}
	s := tr.LaunchSummaryOr("not measured")
	if !strings.Contains(s, "no symbol table (stripped?) (2)") || !strings.Contains(s, "passes a handle, not a function (1)") {
		t.Fatalf("%s", s)
	}
}
