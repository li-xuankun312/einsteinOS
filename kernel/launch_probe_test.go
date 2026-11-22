package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// a GPU unit's test.sh runs under the probe and its launches come back in the trace
func TestGPUUnitTestRunsUnderTheLaunchProbe(t *testing.T) {
	withProbeDir(t)
	realRunner(t)
	withFakeToolchain(t)
	ws := t.TempDir()
	prog := fakeCudaProgram(t, t.TempDir())
	build, test := devVerifyScripts("ug1")
	os.MkdirAll(filepath.Join(ws, filepath.Dir(build)), 0o755)
	os.WriteFile(filepath.Join(ws, build), []byte("exit 0\n"), 0o755)
	os.WriteFile(filepath.Join(ws, test), []byte(shq(prog)+"\n"), 0o755)
	req := UnitRequirements{Sources: []string{"src/k.cu"}, GPUSources: []string{"src/k.cu"}}
	res := runUnitVerifyWith(ws, "ug1", "", req, nil, time.Minute, time.Minute)
	if res.Smoke != "pass" {
		t.Fatalf("smoke=%s out=%s", res.Smoke, res.TestOut)
	}
	if !launchedNames(res.Test.Launches)["my_kernel"] {
		t.Fatalf("launches not measured: %+v (probe err %q)", res.Test.Launches, res.Test.ProbeErr)
	}
	found := false
	for _, e := range res.Test.ProbeLoaded {
		found = found || e == prog
	}
	if !found {
		t.Fatalf("ProbeLoaded = %v, want it to include %s", res.Test.ProbeLoaded, prog)
	}

	// a CPU unit's test runs without the probe
	cpu := runUnitVerifyWith(ws, "ug1", "", UnitRequirements{Sources: []string{"src/a.cpp"}}, nil, time.Minute, time.Minute)
	if len(cpu.Test.Launches) != 0 || len(cpu.Test.ProbeLoaded) != 0 {
		t.Fatalf("CPU unit ran under the probe: %+v", cpu.Test)
	}
}

func execCC(args ...string) ([]byte, error) { return exec.Command("cc", args...).CombinedOutput() }

// TestTheProbeRecordsWhichProcessPrintedTokenLines: a C program printing through stdio, a
// C++ one through std::cout and a python through print each have their "prompt:" and
// "generated:" lines logged under their own pid, and what the run prints is unchanged.
func TestTheProbeRecordsWhichProcessPrintedTokenLines(t *testing.T) {
	withProbeDir(t)
	dir := t.TempDir()
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("no C compiler")
	}
	lib, err := launchProbeLib()
	if err != nil {
		t.Fatalf("launchProbeLib: %v", err)
	}
	c := filepath.Join(dir, "c.c")
	os.WriteFile(c, []byte(`#include <stdio.h>
int main(void){ printf("generated: %d %d\n", 11, 12); puts("prompt: 3 4"); printf("generated:"); for(int i=0;i<3;i++) printf(" %d", i+20); putchar('\n'); fprintf(stderr, "other line\n"); return 0; }`), 0o644)
	cprog := filepath.Join(dir, "cprog")
	if out, err := exec.Command("cc", "-O2", "-D_FORTIFY_SOURCE=2", "-o", cprog, c).CombinedOutput(); err != nil {
		t.Fatalf("cc: %v\n%s", err, out)
	}
	script := shq(cprog) + `; bash -c "echo 'generated: 7'; echo 'not generated: 1'"`
	if _, err := exec.LookPath("python3"); err == nil {
		script += `; python3 -c 'print("hello"); print("generated: 1 2 3", end="")'`
	}
	logPath := filepath.Join(dir, "launch.log")
	out, err := exec.Command("bash", "-c", launchProbeEnv(lib, logPath)+"( "+script+" ) 2>&1").CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	for _, l := range []string{"generated: 11 12\nprompt: 3 4\ngenerated: 20 21 22\n", "other line\n", "generated: 7\nnot generated: 1\n"} {
		if !strings.Contains(string(out), l) {
			t.Fatalf("the run's output changed under the probe (no %q):\n%s", l, out)
		}
	}
	pl := readProbeLog(logPath)
	byExe := map[string][]string{}
	for pid, lines := range pl.Out {
		byExe[filepath.Base(pl.Loaded[pid])] = append(byExe[filepath.Base(pl.Loaded[pid])], lines...)
	}
	if got := strings.Join(byExe["cprog"], "|"); got != "generated: 11 12|prompt: 3 4|generated: 20 21 22" {
		t.Fatalf("C program's token lines: %q (all: %v)", got, byExe)
	}
	if got := strings.Join(byExe["bash"], "|"); got != "generated: 7" {
		t.Fatalf("bash's token lines: %q (all: %v)", got, byExe)
	}
	for exe, lines := range byExe {
		if strings.HasPrefix(exe, "python3") && strings.Join(lines, "|") != "generated: 1 2 3" {
			t.Fatalf("python's token lines: %q", lines)
		}
	}
}

func TestAnOutLineKeepsItsTabs(t *testing.T) {
	p := filepath.Join(t.TempDir(), "l")
	os.WriteFile(p, []byte("out\t42\tgenerated:\t1\t2\nload\t42\t/x\n"), 0o644)
	pl := readProbeLog(p)
	if len(pl.Out[42]) != 1 || pl.Out[42][0] != "generated:\t1\t2" || pl.Loaded[42] != "/x" {
		t.Fatalf("%+v", pl)
	}
}

// TestTheProbeRecordsTheCodeAProcessReads: a python's imports (sources or their cached
// bytecode), a library it loads by its bare name (by the library's own path) and a source a C
// program opens are logged under the process that read them; a file written is not.
func TestTheProbeRecordsTheCodeAProcessReads(t *testing.T) {
	withProbeDir(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	dir := t.TempDir()
	lib, err := launchProbeLib()
	if err != nil {
		t.Fatalf("launchProbeLib: %v", err)
	}
	os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	os.WriteFile(filepath.Join(dir, "pkg", "__init__.py"), []byte("X = 1\n"), 0o644)
	logPath := filepath.Join(dir, "launch.log")
	py := `import sys, ctypes; sys.path.insert(0, "` + dir + `"); import pkg; ctypes.CDLL("libc.so.6"); open("` + dir + `/written.py", "w").write("")`
	if out, err := exec.Command("bash", "-c", launchProbeEnv(lib, logPath)+"python3 -B -c "+shq(py)).CombinedOutput(); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	pl := readProbeLog(logPath)
	var all []string
	for _, c := range pl.Code {
		all = append(all, c...)
	}
	joined := strings.Join(all, "\n")
	if !strings.Contains(joined, filepath.Join(dir, "pkg", "__init__.py")) {
		t.Fatalf("the imported package's source is not recorded:\n%s", joined)
	}
	if strings.Contains(joined, "written.py") {
		t.Fatalf("a file opened for writing is recorded:\n%s", joined)
	}
	libc := false
	for _, c := range all {
		libc = libc || (strings.HasPrefix(c, "/") && strings.Contains(filepath.Base(c), "libc.so.6"))
	}
	if !libc {
		t.Fatalf("a library loaded by its bare name is not recorded by its path:\n%s", joined)
	}
}
