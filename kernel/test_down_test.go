package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// downFixture: a unit workspace whose build/fake_test ran under the probe with the crash
// directory set, as 11/24 17:40 router_top10_parity did, and whose test.sh was noted as started.
func downFixture(t *testing.T) (ws, crash, logPath, prog string) {
	t.Helper()
	withProbeDir(t)
	ws, crash = t.TempDir(), t.TempDir()
	t.Setenv("EINSTEINOS_CRASH_DIR", crash)
	os.MkdirAll(filepath.Join(ws, "build"), 0o755)
	os.MkdirAll(filepath.Join(ws, "src"), 0o755)
	os.WriteFile(filepath.Join(ws, "src", "other.cu"), []byte("#include \"decl.hpp\"\n__global__ void\nother_kernel(int* p) { (void)p; }\n"), 0o644)
	os.WriteFile(filepath.Join(ws, "src", "decl.hpp"), []byte("__global__ void other_kernel(int* p);\n"), 0o644)
	os.WriteFile(filepath.Join(ws, "src", "caller.cu"), []byte("void run() { other_kernel<<<1,1>>>(0); }\n"), 0o644)
	prog = fakeCudaProgram(t, filepath.Join(ws, "build"))
	lib, err := launchProbeLib()
	if err != nil {
		t.Fatalf("launchProbeLib: %v", err)
	}
	logPath = filepath.Join(crash, "einsteinos-launch-198570202.log")
	noteCrashStage("uint", ".einsteinOS/verify/uint/test.sh", logPath)
	if out, err := exec.Command("bash", "-c", launchProbeEnv(lib, logPath)+"export EINSTEINOS_LAUNCH_SYNC=1; "+shq(prog)).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return ws, crash, logPath, prog
}

func bootAt(t *testing.T, at time.Time) {
	t.Helper()
	old := machineBootTimeFn
	machineBootTimeFn = func() time.Time { return at }
	t.Cleanup(func() { machineBootTimeFn = old })
}

// 11/24 17:40:14 test.sh started, no end was noted and the node booted again: the last launch the
// probe noted is named from the executable the next build makes at the same path, with the files
// that define that kernel, kept on the crash directory and logged once
func TestTheTestTheMachineWentDownInIsMeasured(t *testing.T) {
	ws, crash, logPath, prog := downFixture(t)
	os.Remove(prog) // /tmp went with the machine
	bootAt(t, time.Now().Add(time.Minute))
	fakeCudaProgram(t, filepath.Join(ws, "build")) // the next verification's build

	d, r, first := noteTestDown(ws, "uint")
	if d == nil || !first {
		t.Fatalf("no down test read: %+v %v", d, first)
	}
	if d.Exe != prog || d.Kernels != 2 || d.LaunchLog != logPath || d.Test != ".einsteinOS/verify/uint/test.sh" {
		t.Fatalf("down: %+v", d)
	}
	if r.Kernel != "other_kernel" || len(r.Sources) != 1 || r.Sources["src/other.cu"] == "" || r.ExeSHA == "" {
		t.Fatalf("record: %+v", r)
	}
	if fact := testDownFact(d, r); !strings.Contains(fact, "the 2nd distinct kernel of "+prog) || !strings.Contains(fact, "other_kernel, defined in src/other.cu") {
		t.Fatalf("fact: %s", fact)
	}
	if _, err := os.Stat(filepath.Join(crash, "einsteinos-down-198570202.json")); err != nil {
		t.Fatalf("record not kept: %v", err)
	}
	// the next verification reads the record kept, measured once
	if d2, r2, again := noteTestDown(ws, "uint"); d2 == nil || again || r2.Kernel != "other_kernel" {
		t.Fatalf("second read: %+v %+v %v", d2, r2, again)
	}
}

// a test whose end was noted, or a machine that has not booted since it started, is not down
func TestATestThatEndedOrStillRunsIsNotDown(t *testing.T) {
	_, crash, _, _ := downFixture(t)
	bootAt(t, time.Now().Add(-time.Hour))
	if d := lastTestDown(crash, "uint"); d != nil {
		t.Fatalf("the machine has not booted since the test started: %+v", d)
	}
	bootAt(t, time.Now().Add(time.Minute))
	if d := lastTestDown(crash, "uother"); d != nil {
		t.Fatalf("another unit's test: %+v", d)
	}
	CrashNote("unit uint: .einsteinOS/verify/uint/test.sh ended (exit 0)")
	if d := lastTestDown(crash, "uint"); d != nil {
		t.Fatalf("its end was noted: %+v", d)
	}
	t.Setenv("EINSTEINOS_CRASH_DIR", "")
	if d, _, _ := noteTestDown(t.TempDir(), "uint"); d != nil {
		t.Fatal("no crash directory, nothing read")
	}
}

// the executable gone and not made again: the launch is said to be unresolved, with why
func TestAnUnresolvedLastLaunchSaysWhy(t *testing.T) {
	ws, _, _, prog := downFixture(t)
	os.Remove(prog)
	bootAt(t, time.Now().Add(time.Minute))
	d, r, _ := noteTestDown(ws, "uint")
	if d == nil || r.Kernel != "" || r.Why == "" {
		t.Fatalf("%+v %+v", d, r)
	}
	if fact := testDownFact(d, r); !strings.Contains(fact, "unresolved") {
		t.Fatalf("fact: %s", fact)
	}
}

// while the files defining the kernel the machine went down with are as they were, the unit's
// test.sh is not run: the verification says so with what was measured; once one of them changes
// it runs again
func TestATestTheMachineWentDownInIsNotRunAgainUnchanged(t *testing.T) {
	ws, _, _, prog := downFixture(t)
	os.Remove(prog)
	bootAt(t, time.Now().Add(time.Minute))
	fakeCudaProgram(t, filepath.Join(ws, "build"))
	test := ".einsteinOS/verify/uint/test.sh"
	os.MkdirAll(filepath.Join(ws, filepath.Dir(test)), 0o755)
	os.WriteFile(filepath.Join(ws, test), []byte("true\n"), 0o755)
	ran := withRunner(t, func(string) (int, string) { return 0, "" })
	verify := func() VerifyResult {
		return runUnitTestStage(ws, "uint", "", UnitRequirements{}, nil, time.Minute, time.Minute, "", ".einsteinOS/verify/uint/build.sh", test,
			VerifyResult{Compile: "pass", Smoke: "missing", BuildExit: 0, TestExit: -1}, time.Now())
	}
	for i := 0; i < 2; i++ {
		res := verify()
		if res.Smoke != "skipped" || res.Gate != "machine_down_during_test" || !strings.Contains(res.GateText, "other_kernel, defined in src/other.cu") ||
			!strings.Contains(res.GateText, "src/other.cu, defining other_kernel, are as they were") {
			t.Fatalf("verification %d: %+v", i+1, res)
		}
		if len(*ran) != 0 {
			t.Fatalf("verification %d ran test.sh: %v", i+1, *ran)
		}
		if res.Passed() {
			t.Fatal("a held test passed")
		}
	}
	os.WriteFile(filepath.Join(ws, "src", "other.cu"), []byte("__global__ void other_kernel(int* p) { if (p) *p = 0; }\n"), 0o644)
	if res := verify(); res.Gate == "machine_down_during_test" {
		t.Fatalf("its source changed and it was held: %+v", res)
	}
	if len(*ran) != 1 || !strings.Contains((*ran)[0], test) {
		t.Fatalf("its source changed and test.sh did not run: %v", *ran)
	}
}

func TestWhatTheMachineWentDownWithIsComparedByWhatIsKnown(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "x")
	os.WriteFile(exe, []byte("one"), 0o755)
	h, _ := fileSHA256(exe)
	d := &testDown{Exe: exe}
	if same, why := testDownSame(dir, d, &testDownRecord{ExeSHA: h}, false); !same || !strings.Contains(why, "builds the same") {
		t.Fatalf("%v %s", same, why)
	}
	os.WriteFile(exe, []byte("two"), 0o755)
	if same, _ := testDownSame(dir, d, &testDownRecord{ExeSHA: h}, false); same {
		t.Fatal("a different executable was held")
	}
	if same, _ := testDownSame(dir, d, &testDownRecord{}, true); !same {
		t.Fatal("nothing known: the first verification is held")
	}
	if same, _ := testDownSame(dir, d, &testDownRecord{}, false); same {
		t.Fatal("nothing known: later verifications run")
	}
}

// the gate's text, not an empty smoke error, is what the unit's attempt fails with
func TestAHeldTestFailsTheAttemptWithItsGate(t *testing.T) {
	v := VerifyResult{Compile: "pass", Smoke: "skipped", Gate: "machine_down_during_test", GateText: "machine_down_during_test: x"}
	if st := v.Status(); st["gate_error"] != v.GateText {
		t.Fatalf("%v", st)
	}
}

// 11/25 01:49 the file defining router_top10_wave_kernel had changed, test.sh ran, and the
// machine went down at the same kernel again: a kernel the machine went down with twice is not
// let run by a change of its files, until EINSTEINOS_TEST_DOWN_RELEASE names it
func TestAKernelTheMachineWentDownWithTwiceIsHeldThoughItsFilesChange(t *testing.T) {
	ws, crash, _, prog := downFixture(t)
	os.Remove(prog)
	bootAt(t, time.Now().Add(time.Minute))
	fakeCudaProgram(t, filepath.Join(ws, "build"))
	d, r, first := noteTestDown(ws, "uint")
	if d == nil || r.Kernel != "other_kernel" || r.Unit != "uint" || r.Test != d.Test || r.At == "" {
		t.Fatalf("%+v %+v", d, r)
	}
	os.WriteFile(filepath.Join(ws, "src", "other.cu"), []byte("__global__ void other_kernel(int* p) { if (p) *p = 0; }\n"), 0o644)
	if same, why := testDownSame(ws, d, r, first); same {
		t.Fatalf("down once, its file changed: held (%s)", why)
	}
	// an earlier time, on another launch log, at the same kernel
	saveTestDownRecord(filepath.Join(crash, "einsteinos-down-882494937.json"),
		&testDownRecord{Unit: "uint", Test: d.Test, At: "2026/11/24 17:40:14.827", Kernel: "other_kernel", Sources: map[string]string{"src/other.cu": "0121486c"}})
	if n := len(testDownsOfKernel(crash, "other_kernel")); n != 2 {
		t.Fatalf("downs: %d", n)
	}
	same, why := testDownSame(ws, d, r, first)
	if !same || !strings.Contains(why, "went down at other_kernel 2 times") || !strings.Contains(why, testDownReleaseEnv) {
		t.Fatalf("down twice, its file changed: %v %s", same, why)
	}
	t.Setenv(testDownReleaseEnv, "x_kernel, other_kernel")
	if same, why := testDownSame(ws, d, r, first); same {
		t.Fatalf("released: held (%s)", why)
	}
}
