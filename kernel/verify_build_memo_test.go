package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// 11/22 every verification of the integration unit rebuilt everything for 6m26s, also the
// ones that changed only a python file build.sh does not read: a build whose files read and
// directories listed did not change is not run again, its outputs put back
func TestABuildWhoseInputsDidNotChangeIsNotRunAgain(t *testing.T) {
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("no C compiler for the probe")
	}
	withProbeDir(t)
	withKeepRoot(t)
	realRunner(t)
	t.Setenv("EINSTEINOS_FRESH_BUILD", "")
	unit := "umemo_test"
	ws := filepath.Join(os.TempDir(), "einsteinos_verify_"+unit)
	os.RemoveAll(ws)
	defer os.RemoveAll(ws)
	build, test := devVerifyScripts(unit)
	write := func(rel, s string) {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o755)
	}
	write("src/a.c", "a")
	write("pytools/e2e_infer.py", "print(1)")
	// the build lists src/, reads each file there with cat, counts its runs outside the
	// workspace, and writes build/out
	counter := filepath.Join(t.TempDir(), "runs")
	write(build, "rm -rf build; mkdir -p build; for f in src/*.c; do cat $f >> build/out; done; echo run >> "+counter+"\n")
	write(test, "test -s build/out\n")
	verify := func() VerifyResult {
		return runUnitVerifyAlone(ws, unit, "", UnitRequirements{}, nil, time.Minute, time.Minute)
	}
	runs := func() int { b, _ := os.ReadFile(counter); return len(b) / 4 }

	if r := verify(); r.Compile != "pass" || r.BuildReused || runs() != 1 {
		t.Fatalf("first: compile=%s reused=%v runs=%d out=%q", r.Compile, r.BuildReused, runs(), r.BuildOut)
	}
	os.RemoveAll(filepath.Join(ws, "build")) // a workspace assembled fresh has no build/
	write("pytools/e2e_infer.py", "print(2)")
	r := verify()
	if !r.BuildReused || runs() != 1 || r.Smoke != "pass" {
		t.Fatalf("a change the build does not read: reused=%v runs=%d smoke=%s", r.BuildReused, runs(), r.Smoke)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "build", "out")); string(b) != "a" {
		t.Fatalf("the build's output was not put back: %q", b)
	}
	write("src/a.c", "A")
	if r := verify(); r.BuildReused || runs() != 2 {
		t.Fatalf("a file the build read changed and it was reused: runs=%d", runs())
	}
	write("src/b.c", "b")
	if r := verify(); r.BuildReused || runs() != 3 {
		t.Fatalf("a file added to a directory the build listed and it was reused: runs=%d", runs())
	}
	write(build, "rm -rf build; mkdir -p build; for f in src/*.c; do cat $f >> build/out; done; echo run >> "+counter+"; true\n")
	if r := verify(); r.BuildReused || runs() != 4 {
		t.Fatalf("build.sh changed and it was reused: runs=%d", runs())
	}
	t.Setenv("EINSTEINOS_FRESH_BUILD", "1")
	if r := verify(); r.BuildReused || runs() != 5 {
		t.Fatalf("EINSTEINOS_FRESH_BUILD=1 reused a build: runs=%d", runs())
	}
}
