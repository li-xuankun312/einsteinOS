package kernel

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func withKeepRoot(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old := verifyKeepRoot
	verifyKeepRoot = func() string { return dir }
	t.Cleanup(func() { verifyKeepRoot = old })
}

// 11/22 two verifications that changed one python file rebuilt the whole project: the build
// directory comes back, and the files whose content did not change get back their times
func TestAVerificationBuildsOnTheLastBuildDirectory(t *testing.T) {
	withKeepRoot(t)
	t.Setenv("EINSTEINOS_FRESH_BUILD", "")
	tip, mirror := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(tip, "src"), 0o755)
	os.WriteFile(filepath.Join(tip, "src", "k.cu"), []byte("kernel"), 0o644)
	os.MkdirAll(filepath.Join(mirror, "pytools"), 0o755)
	os.WriteFile(filepath.Join(mirror, "pytools", "e2e.py"), []byte("v1"), 0o644)
	unit := "ukeep_test"

	ws, seeded, done, err := assembleWorkspaceKept(unit, tip, mirror, []string{"pytools/e2e.py"}, nil)
	if err != nil || seeded {
		t.Fatalf("first: %v seeded=%v", err, seeded)
	}
	defer os.RemoveAll(ws)
	if filepath.Base(ws) != "einsteinos_verify_"+unit {
		t.Fatalf("the workspace is not the unit's own path: %s", ws)
	}
	// the build wrote its directory; the sources' times are what the build saw
	os.MkdirAll(filepath.Join(ws, "build"), 0o755)
	os.WriteFile(filepath.Join(ws, "build", "k.o"), []byte("obj"), 0o644)
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	os.Chtimes(filepath.Join(ws, "src", "k.cu"), old, old)
	os.Chtimes(filepath.Join(ws, "pytools", "e2e.py"), old, old)
	done()
	if _, err := os.Stat(ws); err == nil {
		t.Fatal("the workspace was left behind")
	}

	// the next attempt changes only the python file
	os.WriteFile(filepath.Join(mirror, "pytools", "e2e.py"), []byte("v2"), 0o644)
	ws2, seeded, done2, err := assembleWorkspaceKept(unit, tip, mirror, []string{"pytools/e2e.py"}, nil)
	if err != nil || !seeded {
		t.Fatalf("second: %v seeded=%v", err, seeded)
	}
	defer done2()
	if b, _ := os.ReadFile(filepath.Join(ws2, "build", "k.o")); string(b) != "obj" {
		t.Fatal("the last build directory is not back")
	}
	if st, _ := os.Stat(filepath.Join(ws2, "src", "k.cu")); !st.ModTime().Equal(old) {
		t.Fatalf("an unchanged source did not get its time back: %v", st.ModTime())
	}
	if st, _ := os.Stat(filepath.Join(ws2, "pytools", "e2e.py")); st.ModTime().Equal(old) {
		t.Fatal("a changed file got the old time: make would not rebuild what depends on it")
	}
	if b, _ := os.ReadFile(filepath.Join(ws2, "pytools", "e2e.py")); string(b) != "v2" {
		t.Fatal("the delta was not laid over the tip")
	}
}

func TestAFreshBuildIsAskedForByTheEnvironment(t *testing.T) {
	withKeepRoot(t)
	t.Setenv("EINSTEINOS_FRESH_BUILD", "1")
	tip, mirror := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(tip, "a.c"), []byte("x"), 0o644)
	ws, seeded, done, err := assembleWorkspaceKept("ufresh", tip, mirror, nil, nil)
	if err != nil || seeded || filepath.Base(ws) == "einsteinos_verify_ufresh" {
		t.Fatalf("%v %v %s", err, seeded, ws)
	}
	done()
	if _, err := os.Stat(ws); err == nil {
		t.Fatal("left behind")
	}
}
