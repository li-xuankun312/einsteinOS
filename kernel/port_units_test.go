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
