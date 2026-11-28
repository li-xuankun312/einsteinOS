package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The integration unit ports the files the reference builds each registered test from:
// Strata's gdn_parity is add_executable(gdn_parity src/kernels/gdn_parity.cpp). On 11/06 the
// integration unit passed with a gdn_parity.cu that #included another unit's test and ran its
// invariant checks — a test of the same name, not the reference's test.
func TestIntegrationTestsCarryTheirReferenceSources(t *testing.T) {
	cm, err := os.ReadFile("testdata/strata/CMakeLists.txt")
	if err != nil {
		t.Fatal(err)
	}
	parity := "static float cpu_ref(float x) { return x; }\nint run_parity(int n) { return n; }\nint main() { return run_parity(0); }\n"
	old := refGitFn
	defer func() { refGitFn = old }()
	refGitFn = func(dir string, args ...string) (string, error) {
		switch {
		case args[0] == "ls-tree":
			return "CMakeLists.txt\nsrc/kernels/gdn_parity.cpp\n", nil
		case strings.HasSuffix(args[len(args)-1], "gdn_parity.cpp"):
			return parity, nil
		}
		return string(cm), nil
	}
	tests, _ := refIntegrationTests("Niko1221/Strata", "01529aaa82b9")
	var gdn RefTest
	for _, x := range tests {
		if x.Name == "gdn_parity" {
			gdn = x
		}
	}
	if len(gdn.Sources) != 1 || gdn.Sources[0] != "src/kernels/gdn_parity.cpp" {
		t.Fatalf("gdn_parity's sources: %v", gdn.Sources)
	}
	srcs, syms := integrationTestSymbols("", "01529aaa82b9", tests)
	if len(srcs) != 1 || len(syms) == 0 {
		t.Fatalf("sources %v, definitions %v", srcs, syms)
	}

	// a wrapper of the same name ports none of them; the reference's test, ported, does
	ws := t.TempDir()
	wrapper := "#include \"../k32_gpu_kernels/test_kg.cu\"\nint main() { test_gdn(); return 0; }\n"
	os.MkdirAll(filepath.Join(ws, "tests/k39_integration"), 0o755)
	os.WriteFile(filepath.Join(ws, "tests/k39_integration/gdn_parity.cu"), []byte(wrapper), 0o644)
	id := IntegrationUnitID("01529aaa82b9")
	if miss := portMissing(ws, id, syms); len(miss) != len(syms) {
		t.Fatalf("a wrapper ports nothing of the reference's test: %q", miss)
	}
	os.WriteFile(filepath.Join(ws, "tests/k39_integration/gdn_parity.cpp"), []byte(parity), 0o644)
	var tsv strings.Builder
	for _, s := range syms {
		tsv.WriteString(s.File + "\t" + s.Name + "\ttests/k39_integration/gdn_parity.cpp\t" + s.Name + "\n")
	}
	os.MkdirAll(filepath.Join(ws, ".einsteinOS/port"), 0o755)
	os.WriteFile(filepath.Join(ws, PortMapPath(id)), []byte(tsv.String()), 0o644)
	if miss := portMissing(ws, id, syms); len(miss) != 0 {
		t.Fatalf("the reference's test ported: %q", miss)
	}
}
