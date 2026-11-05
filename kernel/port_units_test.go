package kernel

import "testing"

// 11/04: 58 of the commit's code files had theory, the other ~150 had no unit. Every
// uncovered file gets a port unit, one per directory, and its pass is its own.
func TestEveryReferenceCodeFileHasAUnit(t *testing.T) {
	p := newDevPlan("ref/proj", "sha", mods("A"), [][]string{{"A"}}, "plan")
	all := []string{"src/spec/controller.cpp", "src/core/graph.cpp", "src/core/layer.cpp", "serve/server.py"}
	covered := map[string]bool{"src/spec/controller.cpp": true}
	if n := p.AddPortUnits(all, covered); n != 2 {
		t.Fatalf("src/core and serve: %d", n)
	}
	if n := p.AddPortUnits(all, covered); n != 0 {
		t.Fatal("added twice")
	}
	leaves := p.Leaves()
	if len(leaves) != 3 || len(leaves[2].Sources) != 2 || leaves[2].Sources[0] != "src/core/graph.cpp" || leaves[1].Sources[0] != "serve/server.py" {
		t.Fatalf("%+v", leaves)
	}
	if p.UnitPass(leaves[1]) {
		t.Fatal("a port unit has not passed until it passes")
	}
	p.MarkUnitPass(leaves[1])
	if !p.UnitPass(leaves[1]) || p.UnitPass(leaves[2]) {
		t.Fatal("its pass is its own")
	}
	b, _ := p.Marshal()
	q, err := unmarshalDevPlan(b)
	if err != nil || len(q.PortUnits) != 2 || !q.PortPass[leaves[1].ID] {
		t.Fatalf("survives plan.json: %v %+v", err, q)
	}
}

func TestPassesJudgedByAnotherGateSetAreJudgedAgain(t *testing.T) {
	p := newDevPlan("ref/proj", "sha", mods("A"), [][]string{{"A"}}, "plan")
	p.MarkUnitPass(p.Leaves()[0])
	if n := p.RejudgeIfGatesChanged(); n != 1 || p.UnitPass(p.Leaves()[0]) {
		t.Fatalf("reopened %d", n)
	}
	p.MarkUnitPass(p.Leaves()[0])
	if n := p.RejudgeIfGatesChanged(); n != 0 || !p.UnitPass(p.Leaves()[0]) {
		t.Fatal("same gate set: kept")
	}
}

// third_party/ggml/ggml-common.h (78 definitions) is vendored, not the reference's own code
func TestVendoredReferenceCodeIsNotAPortRequirement(t *testing.T) {
	for f, want := range map[string]bool{"third_party/ggml/ggml-common.h": true, "src/vendor/x.h": true, "src/core/layer.cpp": false, "include/extern_api.hpp": false} {
		if refVendoredRe.MatchString(f) != want {
			t.Errorf("%s: %v", f, !want)
		}
	}
	old := refGitFn
	defer func() { refGitFn = old }()
	refGitFn = func(dir string, args ...string) (string, error) {
		if args[0] == "ls-tree" {
			return "third_party/ggml/ggml-common.h\nsrc/a.cpp\n", nil
		}
		return "int f() { return 1; }\n", nil
	}
	if got := refPortSources("o/r", "sha"); len(got) != 1 || got[0] != "src/a.cpp" {
		t.Fatalf("%v", got)
	}
	r := requirementsForSources("o/r", "sha", []string{"third_party/ggml/ggml-common.h", "src/a.cpp"})
	if len(r.Sources) != 1 || len(r.Vendored) != 1 || len(r.Symbols) != 1 {
		t.Fatalf("%+v", r)
	}
}
