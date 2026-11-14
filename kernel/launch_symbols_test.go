package kernel

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestKernelIdent(t *testing.T) {
	for sym, want := range map[string]string{
		"_Z9my_kernelPf":                 "my_kernel",
		"_Z9my_kernelILi4EEvPf":          "my_kernel",
		"_ZN6baloon3gdn8gdn_stepEPKfPfi": "gdn_step",
		"_ZN2nsL6kernelILi4EEEvPf":       "kernel",
		"_Z23__device_stub__fused_opPf":  "fused_op",
		"plain_c_kernel":                 "plain_c_kernel",
		"_Zbroken":                       "_Zbroken",
	} {
		if got := kernelIdent(sym); got != want {
			t.Errorf("kernelIdent(%q) = %q, want %q", sym, got, want)
		}
	}
}

// the probe's addresses resolve to the kernels' names through the program's .symtab
// (the stubs are not exported, so dladdr had no name for them)
func TestResolveLaunchesNamesTheKernels(t *testing.T) {
	withProbeDir(t)
	dir := t.TempDir()
	prog := fakeCudaProgram(t, dir)
	lib, err := launchProbeLib()
	if err != nil {
		t.Fatalf("launchProbeLib: %v", err)
	}
	logPath := filepath.Join(dir, "launch.log")
	if out, err := exec.Command("bash", "-c", launchProbeEnv(lib, logPath)+shq(prog)).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ls := resolveLaunches(readProbeLog(logPath))
	got := launchedNames(ls)
	if !got["my_kernel"] || !got["other_kernel"] || len(got) != 2 {
		t.Fatalf("launched names = %v (launches %+v)", got, ls)
	}
}

func TestResolveLaunchesLeavesDriverHandlesUnnamed(t *testing.T) {
	ls := resolveLaunches(probeLog{Launches: []probeLaunch{{PID: 1, API: "cuLaunchKernel", Addr: 0x1234, Object: "?"}}})
	if len(ls) != 1 || ls[0].Name != "" || ls[0].Symbol != "" {
		t.Fatalf("driver launch: %+v", ls)
	}
}

// A test started by a relative path ("build/test_x") gets that relative path from dladdr as
// the object of its launches; the kernel, in another directory, names them from the
// executable the probe was loaded into. 11/11 14:45 unit u63af4bc057: four launches were
// unnamed because "build/test_u63af_gpu could not be read", and the unit was sent back
// gpu_launch_unmeasured.
func TestLaunchesOfARelativelyStartedTestAreNamed(t *testing.T) {
	withProbeDir(t)
	dir := t.TempDir()
	prog := fakeCudaProgram(t, dir)
	lib, err := launchProbeLib()
	if err != nil {
		t.Fatalf("launchProbeLib: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "launch.log")
	rel := "./" + filepath.Base(prog)
	if out, err := exec.Command("bash", "-c", "cd "+shq(dir)+" && "+launchProbeEnv(lib, logPath)+rel).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pl := readProbeLog(logPath)
	if len(pl.Launches) == 0 || filepath.IsAbs(pl.Launches[0].Object) {
		t.Skipf("dladdr gave an absolute object here (%+v); nothing to resolve", pl.Launches)
	}
	ls := resolveLaunches(pl)
	got := launchedNames(ls)
	if !got["my_kernel"] || !got["other_kernel"] || len(got) != 2 {
		t.Fatalf("launched names = %v (launches %+v)", got, ls)
	}
	for _, l := range ls {
		if l.Object != prog {
			t.Fatalf("the launch's object is the executable the probe was loaded into: %q, want %q", l.Object, prog)
		}
	}
}
