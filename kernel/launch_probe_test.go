package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCudaProgram builds, in dir, a stand-in libcudart.so whose cudaLaunchKernel does
// nothing, and a program linked against it that launches the "kernel" whose host stub is
// named like nvcc/clang name one (_Z9my_kernelPf), once per call of the loop below.
func fakeCudaProgram(t *testing.T, dir string) (prog string) {
	t.Helper()
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("no C compiler")
	}
	rt := `typedef struct { unsigned x, y, z; } dim3;
int cudaLaunchKernel(const void* f, dim3 g, dim3 b, void** a, unsigned long s, void* st) { return 0; }`
	main := `typedef struct { unsigned x, y, z; } dim3;
int cudaLaunchKernel(const void* f, dim3 g, dim3 b, void** a, unsigned long s, void* st);
void _Z9my_kernelPf(float* p) { (void)p; }
void _Z12other_kernelPi(int* p) { (void)p; }
int main(void) {
  dim3 g = {1, 1, 1};
  for (int i = 0; i < 3; i++) cudaLaunchKernel((const void*)&_Z9my_kernelPf, g, g, 0, 0, 0);
  cudaLaunchKernel((const void*)&_Z12other_kernelPi, g, g, 0, 0, 0);
  return 0;
}`
	os.WriteFile(filepath.Join(dir, "rt.c"), []byte(rt), 0o644)
	os.WriteFile(filepath.Join(dir, "main.c"), []byte(main), 0o644)
	run := func(args ...string) {
		if out, err := exec.Command("cc", args...).CombinedOutput(); err != nil {
			t.Fatalf("cc %v: %v\n%s", args, err, out)
		}
	}
	run("-shared", "-fPIC", "-o", filepath.Join(dir, "libcudart.so"), filepath.Join(dir, "rt.c"))
	prog = filepath.Join(dir, "fake_test")
	run("-o", prog, filepath.Join(dir, "main.c"), "-L"+dir, "-lcudart", "-Wl,-rpath,"+dir)
	return prog
}

func withProbeDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old := launchProbeDir
	launchProbeDir = func() string { return dir }
	t.Cleanup(func() { launchProbeDir = old })
}

func TestLaunchProbeRecordsLaunches(t *testing.T) {
	withProbeDir(t)
	dir := t.TempDir()
	prog := fakeCudaProgram(t, dir)
	lib, err := launchProbeLib()
	if err != nil {
		t.Fatalf("launchProbeLib: %v", err)
	}
	if again, _ := launchProbeLib(); again != lib {
		t.Fatalf("second build gave %q, want the cached %q", again, lib)
	}
	logPath := filepath.Join(dir, "launch.log")
	cmd := exec.Command("bash", "-c", launchProbeEnv(lib, logPath)+shq(prog)+" && true")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("program under the probe: %v\n%s", err, out)
	}
	pl := readProbeLog(logPath)
	var progLoaded bool
	for _, exe := range pl.Loaded {
		progLoaded = progLoaded || exe == prog
	}
	if !progLoaded {
		t.Fatalf("probe not loaded into %s: %v", prog, pl.Loaded)
	}
	if len(pl.Launches) != 2 {
		t.Fatalf("want 2 distinct launches (one per kernel, repeats logged once), got %+v", pl.Launches)
	}
	for _, l := range pl.Launches {
		if l.API != "cudaLaunchKernel" || l.Addr == 0 || !strings.HasSuffix(l.Object, "fake_test") {
			t.Fatalf("launch line: %+v", l)
		}
	}
}

func TestLaunchProbeWithoutLogIsSilent(t *testing.T) {
	withProbeDir(t)
	dir := t.TempDir()
	prog := fakeCudaProgram(t, dir)
	lib, err := launchProbeLib()
	if err != nil {
		t.Fatalf("launchProbeLib: %v", err)
	}
	cmd := exec.Command(prog)
	cmd.Env = append(os.Environ(), "LD_PRELOAD="+lib)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("program with the probe but no log: %v\n%s", err, out)
	}
}

func TestReadProbeLogSkipsBadLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "l")
	os.WriteFile(p, []byte("load\t12\t/bin/x\ngarbage\nlaunch\t12\tcudaLaunchKernel\t0x10\t0x0\t/bin/x\t-\nlaunch\tx\ty\n"), 0o644)
	pl := readProbeLog(p)
	if pl.Loaded[12] != "/bin/x" || len(pl.Launches) != 1 || pl.Launches[0].Symbol != "" || pl.Launches[0].Addr != 0x10 {
		t.Fatalf("readProbeLog = %+v", pl)
	}
	if empty := readProbeLog(filepath.Join(t.TempDir(), "none")); len(empty.Loaded) != 0 || len(empty.Launches) != 0 {
		t.Fatalf("missing log: %+v", empty)
	}
}
