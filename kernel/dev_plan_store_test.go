package kernel

import (
	"reflect"
	"testing"
)

func mods(names ...string) []DevModule {
	var out []DevModule
	for _, n := range names {
		out = append(out, DevModule{Name: n, Hash: shortHash("body-" + n), Bytes: 1000})
	}
	return out
}

func TestUnitIDIsOrderIndependentAndSetBased(t *testing.T) {
	a := devUnitID([]string{"A.lean", "B.lean", "C.lean"}, "")
	b := devUnitID([]string{"C.lean", "A.lean", "B.lean"}, "")
	if a != b {
		t.Fatalf("same module set, different order gave different ids: %s vs %s", a, b)
	}
	if devUnitID([]string{"A.lean", "B.lean"}, "") == a {
		t.Fatal("different module sets must differ")
	}
	if devUnitID([]string{"A.lean"}, "") == devUnitID([]string{"A.lean"}, "thm group 1") {
		t.Fatal("a scope inside a module must change the identity")
	}
}

func TestMatchModuleName(t *testing.T) {
	canon := []string{"ActivationQuantLayout.lean", "QuantizedMatVecKernels.lean", "QuantizedMatVecKernelsExtra.lean", "KvQ8PagedCache.lean"}
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"ActivationQuantLayout.lean", "ActivationQuantLayout.lean", true}, // exact
		{"ActivationQuantLayout", "ActivationQuantLayout.lean", true},      // no suffix
		{"activationquantlayout", "ActivationQuantLayout.lean", true},      // case
		{"KvQ8PagedCache (paged kv cache)", "KvQ8PagedCache.lean", true},   // parenthetical
		{"/mnt/user-data/outputs/KvQ8PagedCache.lean", "KvQ8PagedCache.lean", true},
		{"QuantizedMatVecKernels", "QuantizedMatVecKernels.lean", true}, // exact beats prefix of the longer one
		{"QuantizedMatVec", "", false},                                  // ambiguous prefix: two candidates
		{"Kv", "KvQ8PagedCache.lean", true},                             // unique prefix
		{"NoSuchModule", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := matchModuleName(c.in, canon)
		if ok != c.ok || got != c.want {
			t.Errorf("match(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParsePlanDropsZeroMatchLineAndDuplicates(t *testing.T) {
	m := mods("A.lean", "B.lean", "C.lean", "D.lean")
	out := "noise\nunit: A, B\nunit: Ghost1, Ghost2\nunit: B, C\nunit: \n"
	groups, unmatched, unclaimed := parseDevPlanOutput(out, m)
	want := [][]string{{"A.lean", "B.lean"}, {"C.lean"}}
	if !reflect.DeepEqual(groups, want) {
		t.Fatalf("groups=%v want %v", groups, want)
	}
	if len(unmatched) != 2 {
		t.Fatalf("unmatched=%v", unmatched)
	}
	if !reflect.DeepEqual(unclaimed, []string{"D.lean"}) {
		t.Fatalf("unclaimed=%v", unclaimed)
	}
	// the old parser made a 0-byte unit out of the all-ghost line; none of the groups may be empty
	for _, g := range groups {
		if len(g) == 0 {
			t.Fatal("empty group")
		}
	}
}

func TestRebalanceGroups(t *testing.T) {
	size := map[string]int{"a": 30, "b": 30, "c": 30, "big": 500, "d": 10}
	out, over := rebalanceGroups([][]string{{"a", "b", "c", "big", "d"}}, size, 64, 10)
	want := [][]string{{"a", "b"}, {"c"}, {"big"}, {"d"}}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("out=%v want %v", out, want)
	}
	if !reflect.DeepEqual(over, []string{"big"}) {
		t.Fatalf("oversize=%v", over)
	}
	// width cap: 26 tiny modules must not stay in one unit
	var names []string
	sz := map[string]int{}
	for i := 0; i < 26; i++ {
		n := string(rune('a' + i))
		names = append(names, n)
		sz[n] = 1
	}
	out, _ = rebalanceGroups([][]string{names}, sz, 1<<20, 10)
	if len(out) != 3 || len(out[0]) != 10 || len(out[2]) != 6 {
		t.Fatalf("26 modules at cap 10 should give 10/10/6, got %v", out)
	}
}

func TestPassStateLivesOnModulesNotPositions(t *testing.T) {
	m := mods("A", "B", "C", "D")
	p := newDevPlan("o/r", "sha1", m, [][]string{{"A", "B"}, {"C", "D"}}, "plan")
	leaves := p.Leaves()
	p.MarkUnitPass(leaves[0])
	if !p.UnitPass(leaves[0]) || p.UnitPass(leaves[1]) {
		t.Fatal("only unit 1 passed")
	}
	// A different grouping of the SAME modules (what a re-plan used to do): a unit made
	// only of passed modules is still pass; one that mixes in an unpassed module is not.
	q := &DevPlan{Modules: p.Modules, State: p.State, FixAttempts: p.FixAttempts}
	if !q.UnitPass(DevPlanNode{Modules: []string{"B"}}) {
		t.Fatal("B passed as part of unit 1; a unit {B} must read as pass")
	}
	if q.UnitPass(DevPlanNode{Modules: []string{"B", "C"}}) {
		t.Fatal("{B,C} contains unpassed C")
	}
}

func TestFixAttemptsKeyedByUnitIDNotPosition(t *testing.T) {
	p := newDevPlan("o/r", "s", mods("A", "B", "C"), [][]string{{"A"}, {"B"}, {"C"}}, "plan")
	l := p.Leaves()
	// third unit fails once: its count is 1, not 3 (the position)
	if got := p.BumpFix(l[2].ID); got != 1 {
		t.Fatalf("first failure of unit 3 counted as %d", got)
	}
	// first unit can reach the cap (it never could when the count was its position)
	for i := 0; i < DevMaxFixAttempts; i++ {
		p.BumpFix(l[0].ID)
	}
	if p.FixAttempts[l[0].ID] != DevMaxFixAttempts {
		t.Fatalf("unit 1 attempts %d", p.FixAttempts[l[0].ID])
	}
}

func TestSplitReplacesNodeByChildrenInPlace(t *testing.T) {
	p := newDevPlan("o/r", "s", mods("A", "B", "C"), [][]string{{"A"}, {"B"}, {"C"}}, "plan")
	b := p.Leaves()[1]
	kids := p.Split(b.ID, [][]string{{"B"}, {"B"}}, []string{"thm group 1", "thm group 2"}, "oversize")
	if len(kids) != 2 {
		t.Fatalf("kids=%v", kids)
	}
	l := p.Leaves()
	if len(l) != 4 {
		t.Fatalf("leaves=%d want 4 (A, B#1, B#2, C)", len(l))
	}
	if l[0].Modules[0] != "A" || l[1].Scope != "thm group 1" || l[2].Scope != "thm group 2" || l[3].Modules[0] != "C" {
		t.Fatalf("order not preserved: %+v", l)
	}
	// the module is not "passed" until BOTH scopes pass
	p.MarkUnitPass(l[1])
	if p.UnitPass(l[2]) {
		t.Fatal("scope 2 must not inherit scope 1's pass")
	}
	// a one-way "split" is refused
	if p.Split(l[0].ID, [][]string{{"A"}}, nil, "x") != nil {
		t.Fatal("split into <2 children must be refused")
	}
}

func TestReconcileKeepsPassedUnchangedModules(t *testing.T) {
	m := mods("A", "B", "C")
	p := newDevPlan("o/r", "sha1", m, [][]string{{"A", "B"}, {"C"}}, "plan")
	p.MarkUnitPass(p.Leaves()[0])

	// theory changed: B's proof body changed, D is new, C vanished
	m2 := mods("A", "B", "D")
	m2[1].Hash = shortHash("B-changed")
	added, changed := p.Reconcile("sha1", m2)
	if !changed {
		t.Fatal("changed not reported")
	}
	if !reflect.DeepEqual(added, []string{"D"}) {
		t.Fatalf("added=%v", added)
	}
	if p.State["A"].Pass != true {
		t.Fatal("A did not change, must stay PASS")
	}
	if p.State["B"].Pass {
		t.Fatal("B's content changed, its PASS must be dropped")
	}
	for _, n := range p.Nodes {
		for _, mod := range n.Modules {
			if mod == "C" {
				t.Fatal("vanished module C still in a node")
			}
		}
	}
	// unchanged theory: nothing happens
	p2 := newDevPlan("o/r", "sha1", m, [][]string{{"A", "B", "C"}}, "plan")
	if _, ch := p2.Reconcile("sha1", m); ch {
		t.Fatal("identical theory reported as changed")
	}
}

func TestPlanRoundTrip(t *testing.T) {
	p := newDevPlan("o/r", "sha1", mods("A", "B"), [][]string{{"A"}, {"B"}}, "plan")
	p.MarkUnitPass(p.Leaves()[0])
	p.BumpFix(p.Leaves()[1].ID)
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	q, err := unmarshalDevPlan(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, q) {
		t.Fatalf("round trip differs:\n%+v\n%+v", p, q)
	}
	if _, err := unmarshalDevPlan([]byte(`{"version":99}`)); err == nil {
		t.Fatal("unknown plan version must be refused, not read as an empty plan")
	}
}
