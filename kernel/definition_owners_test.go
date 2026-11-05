package kernel

import "testing"

// 11/05: generate.cpp was a source of 4 units and the 3 pieces split from u042aaf67ce each had
// to port the same 90 definitions. Every definition now has exactly one owning unit, and
// split siblings divide their node's files between them.
func TestEveryDefinitionIsPortedByExactlyOneUnit(t *testing.T) {
	p := newDevPlan("ref/proj", "sha", mods("A", "B", "C", "D"), [][]string{{"A"}, {"B"}, {"C", "D"}}, "plan")
	l := p.Leaves()
	kids := p.Split(l[2].ID, [][]string{{"C"}, {"D"}}, nil, "test")
	l = p.Leaves()
	gen := RefSymbol{File: "src/program/generate.cpp", Name: "main"}
	a := RefSymbol{File: "src/a.cpp", Name: "fa"}
	b := RefSymbol{File: "src/b.cpp", Name: "fb"}
	r := &devRun{repo: "o/r", plan: p, raw: map[string]UnitRequirements{
		l[0].ID:    {Sources: []string{"src/program/generate.cpp"}, Symbols: []RefSymbol{gen}},
		l[1].ID:    {Sources: []string{"src/program/generate.cpp", "src/a.cpp"}, Symbols: []RefSymbol{gen, a}},
		kids[0].ID: {Sources: []string{"src/a.cpp", "src/b.cpp"}, Symbols: []RefSymbol{a, b}},
		kids[1].ID: {Sources: []string{"src/a.cpp", "src/b.cpp"}, Symbols: []RefSymbol{a, b}},
	}}
	got := map[string][]RefSymbol{}
	for _, leaf := range l {
		got[leaf.ID] = r.requirements(leaf).Symbols
	}
	if len(got[l[0].ID]) != 1 || len(got[l[1].ID]) != 1 || got[l[1].ID][0].Name != "fa" {
		t.Fatalf("generate.cpp's main belongs to the first unit only: %v", got)
	}
	// a.cpp went to unit 2; of the split pair, ukid1 gets file 0 (a.cpp, already owned) and
	// ukid2 file 1 (b.cpp)
	if len(got[kids[0].ID]) != 0 || len(got[kids[1].ID]) != 1 || got[kids[1].ID][0].Name != "fb" {
		t.Fatalf("siblings divide the files: %v", got)
	}
}

// 11/05 u208dfb4222: "sources []; definitions to port 0" — nothing measured, so anything
// written would have passed. It is closed without a conversation instead.
func TestAUnitWithNothingOfItsOwnToBuildIsClosedWithoutAConversation(t *testing.T) {
	p := newDevPlan("ref/proj", "sha", mods("A"), [][]string{{"A"}}, "plan")
	u := p.Leaves()[0]
	r := &devRun{repo: "o/r", plan: p, raw: map[string]UnitRequirements{u.ID: {Unmeasured: []string{"GatedResidual"}, RefRead: true}}}
	_, passed, requeue := r.runUnit(u, 0, 1)
	if !passed || requeue || !p.UnitPass(u) {
		t.Fatalf("closed: passed=%v requeue=%v", passed, requeue)
	}
}

// a reference that could not be read is not "nothing to build"
func TestAnUnreadReferenceDoesNotCloseAUnit(t *testing.T) {
	p := newDevPlan("ref/proj", "sha", mods("A"), [][]string{{"A"}}, "plan")
	u := p.Leaves()[0]
	r := &devRun{repo: "o/r", plan: p, raw: map[string]UnitRequirements{u.ID: {Unmeasured: []string{"A"}}}}
	if req := r.requirements(u); req.RefRead {
		t.Fatal("not read")
	}
}

// 11/05 u0868e10073 owned 0 of its 90 definitions and still had to write GPU code
func TestGPUAndISARequirementsFollowTheOwnedFiles(t *testing.T) {
	p := newDevPlan("ref/proj", "sha", mods("A", "B"), [][]string{{"A"}, {"B"}}, "plan")
	l := p.Leaves()
	k := RefSymbol{File: "src/k.cu", Name: "kern"}
	c := RefSymbol{File: "src/c.cpp", Name: "cpu"}
	r := &devRun{repo: "o/r", plan: p, raw: map[string]UnitRequirements{
		l[0].ID: {Sources: []string{"src/k.cu"}, Symbols: []RefSymbol{k}, GPUSources: []string{"src/k.cu"}, RefRead: true},
		l[1].ID: {Sources: []string{"src/k.cu", "src/c.cpp"}, Symbols: []RefSymbol{k, c}, GPUSources: []string{"src/k.cu"},
			FlagFiles: map[string][]string{"src/k.cu": {"-mavx2"}}, ISAFlags: []string{"-mavx2"}, RefRead: true},
	}}
	if got := r.requirements(l[1]); got.GPU() || len(got.ISAFlags) != 0 || len(got.Symbols) != 1 {
		t.Fatalf("the .cu belongs to unit 1: %+v", got)
	}
	if !r.requirements(l[0]).GPU() {
		t.Fatal("unit 1 keeps its GPU requirement")
	}
}
