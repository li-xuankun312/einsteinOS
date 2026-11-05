package kernel

import (
	"os"
	"strings"
	"testing"
)

// measured on Strata 01529aaa's own CMakeLists.txt (testdata/strata)
func TestTheReferencesRegisteredTestsAreMeasured(t *testing.T) {
	cm, err := os.ReadFile("testdata/strata/CMakeLists.txt")
	if err != nil {
		t.Fatal(err)
	}
	old := refGitFn
	defer func() { refGitFn = old }()
	refGitFn = func(dir string, args ...string) (string, error) {
		if args[0] == "ls-tree" {
			return "CMakeLists.txt\nsrc/a.cpp\n", nil
		}
		return string(cm), nil
	}
	tests, cuda := refIntegrationTests("Niko1221/Strata", "01529aaa82b9")
	byName := map[string]string{}
	for _, x := range tests {
		byName[x.Name] = x.Command
	}
	for _, n := range []string{"cuda_device_selftest", "kv_q8_parity", "ple_reader_selftest"} {
		if byName[n] == "" {
			t.Errorf("%s not measured (%d tests)", n, len(tests))
		}
	}
	if !cuda {
		t.Fatal("the reference builds CUDA")
	}
	// the oracle parity tests need a llama.cpp checkout passed in by path; the expert-arena
	// test needs pack/full/experts.bin — neither is in the reference
	_, excl, _ := refIntegrationTestsWithExcluded("Niko1221/Strata", "01529aaa82b9")
	if !strings.Contains(excl["native_mmvq_parity"], "STRATA_ORACLE_SOURCE_DIR") || byName["native_mmvq_parity"] != "" {
		t.Fatalf("oracle parity: %q", excl["native_mmvq_parity"])
	}
	t.Logf("%d required, %d excluded: %v", len(tests), len(excl), excl)
	for n := range byName {
		if strings.Contains(n, "${") {
			t.Fatalf("unexpanded: %s", n)
		}
	}
	sc := integrationScript("Niko1221/Strata", "01529aaa82b9", tests)
	if !strings.Contains(sc, `( cd "$B" && strata-device --selftest )`) || !strings.HasSuffix(sc, "exit $fail\n") {
		t.Fatalf("%s", sc[:400])
	}
	blk := UnitRequirements{Tests: tests, RequireGPU: true}.IntegrationBlock("Niko1221/Strata", "01529aaa82b9", IntegrationUnitID("01529aaa82b9"))
	if !strings.Contains(blk, ".einsteinOS/verify/uintegration01529aaa82b9/build.sh") || !strings.Contains(blk, "GPU device") {
		t.Fatalf("%s", blk[:300])
	}
	t.Logf("%d registered tests measured", len(tests))
}

func TestTheIntegrationUnitIsTheLastUnitAndPassesOnItsOwn(t *testing.T) {
	p := newDevPlan("ref/proj", "sha", mods("A"), [][]string{{"A"}}, "plan")
	if !p.SetIntegrationUnit("uintegrationx") || p.SetIntegrationUnit("uintegrationx") {
		t.Fatal("set once")
	}
	l := p.Leaves()
	last := l[len(l)-1]
	if !last.Integration || p.UnitPass(last) {
		t.Fatal("last, not passed")
	}
	p.MarkUnitPass(last)
	if !p.UnitPass(last) {
		t.Fatal("pass")
	}
}
