package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// 11/23 00:05 and 00:20 the integration unit's build ran again with nothing in the log saying
// why: a miss names what changed
func TestAMissSaysWhatChanged(t *testing.T) {
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, "pytools"), 0o755)
	os.WriteFile(filepath.Join(ws, "pytools", "e2e_infer.py"), []byte("1"), 0o644)
	os.WriteFile(filepath.Join(ws, "build.sh"), []byte("x"), 0o644)
	m := buildMemo{Key: "k", Inputs: map[string]string{"pytools/e2e_infer.py": fileSHA(filepath.Join(ws, "pytools", "e2e_infer.py"))},
		Dirs: map[string]string{"pytools": dirListing(filepath.Join(ws, "pytools"))}}
	if c := m.memoChanged(ws, "k"); len(c) != 0 {
		t.Fatalf("nothing changed: %v", c)
	}
	os.WriteFile(filepath.Join(ws, "pytools", "e2e_infer.py"), []byte("2"), 0o644)
	os.WriteFile(filepath.Join(ws, "pytools", "patch_import_utils.py"), []byte(""), 0o644)
	got := strings.Join(m.memoChanged(ws, "k2"), "; ")
	if got != "build.sh or its environment; 1 file(s) it read changed (pytools/e2e_infer.py); 1 director(ies) it listed have other entries (pytools/)" {
		t.Fatalf("%q", got)
	}
}

// 11/26 07:21–11:34 nine verifications of uintegration01529aaa82b9 each logged its build kept,
// and each next one ran the whole build again: the end of a verification removed the unit's keep
// directory, its build's record in it. A verification assembled after one has ended finds it.
func TestABuildKeptOutlivesTheEndOfItsVerification(t *testing.T) {
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("no C compiler for the probe")
	}
	withProbeDir(t)
	withKeepRoot(t)
	realRunner(t)
	t.Setenv("EINSTEINOS_FRESH_BUILD", "")
	unit := "umemo_end_test"
	build, test := devVerifyScripts(unit)
	tip, mirror := t.TempDir(), t.TempDir()
	put := func(dir, rel, s string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o755)
	}
	counter := filepath.Join(t.TempDir(), "runs")
	put(tip, "src/a.c", "a")
	put(tip, build, "rm -rf build; mkdir -p build; for f in src/*.c; do cat $f >> build/out; done; echo run >> "+counter+"\n")
	put(tip, test, "test -s build/out\n")
	put(mirror, ".einsteinOS/port/"+unit+".tsv", "row1\n")
	runs := func() int { b, _ := os.ReadFile(counter); return len(b) / 4 }
	verify := func() VerifyResult {
		ws, _, done, err := assembleWorkspaceKept(unit, tip, mirror, []string{".einsteinOS/port/" + unit + ".tsv"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer done()
		return runUnitVerifyAlone(ws, unit, "", UnitRequirements{}, nil, time.Minute, time.Minute)
	}
	if r := verify(); r.Compile != "pass" || r.BuildReused || runs() != 1 {
		t.Fatalf("first: compile=%s reused=%v runs=%d", r.Compile, r.BuildReused, runs())
	}
	// only the port map changed: the build did not read it
	put(mirror, ".einsteinOS/port/"+unit+".tsv", "row1\nrow2\n")
	if r := verify(); !r.BuildReused || runs() != 1 || r.Smoke != "pass" {
		t.Fatalf("after the first verification ended: reused=%v runs=%d smoke=%s", r.BuildReused, runs(), r.Smoke)
	}
}
