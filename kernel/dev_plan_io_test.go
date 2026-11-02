package kernel

import (
	"fmt"
	"strings"
	"testing"
)

// theoryWith builds a theory text with the given modules; a module body is padded to size bytes.
func theoryWith(mods map[string]int, order []string) string {
	var b strings.Builder
	for _, n := range order {
		b.WriteString("## MODULE: " + n + "\n")
		b.WriteString("theorem " + n + "_thm1 : True := trivial\n")
		pad := mods[n] - b.Len()%1
		_ = pad
		b.WriteString(strings.Repeat("x", mods[n]) + "\n\n")
	}
	return b.String()
}

func withPlanner(t *testing.T, fn func(prompt string) string) *int {
	calls := 0
	old := devPlannerRunner
	devPlannerRunner = func(pid int64, repo, prompt string) string { calls++; return fn(prompt) }
	t.Cleanup(func() {
		devPlannerRunner = old
		devPlanMu.Lock()
		devPlanMem = map[string]*DevPlan{}
		devPlanMu.Unlock()
	})
	return &calls
}

func TestGetPlanPlansOnceAndReuses(t *testing.T) {
	var order []string
	sizes := map[string]int{}
	for i := 0; i < 14; i++ {
		n := fmt.Sprintf("Mod%02d.lean", i)
		order = append(order, n)
		sizes[n] = 20000 // 14 x 20KB = 280KB total: needs planning
	}
	theory := theoryWith(sizes, order)
	calls := withPlanner(t, func(prompt string) string {
		return "unit: Mod00, Mod01, Mod02\nunit: Mod03, Mod04, Mod05\nunit: Ghost, Phantom\n" // 8 modules unplaced
	})
	p1 := dev_get_plan(1, "o/r", "sha1", theory, "einsteinOS/dev-1")
	if *calls != 1 {
		t.Fatalf("planner calls=%d want 1", *calls)
	}
	// every module is in exactly one leaf, and no leaf is empty (the 0-byte unit)
	seen := map[string]int{}
	for _, l := range p1.Leaves() {
		if len(l.Modules) == 0 {
			t.Fatal("empty leaf")
		}
		if p1.UnitTheoryBytes(l) == 0 {
			t.Fatal("0-byte unit")
		}
		for _, m := range l.Modules {
			seen[m]++
		}
	}
	if len(seen) != 14 {
		t.Fatalf("covered %d of 14 modules", len(seen))
	}
	for m, c := range seen {
		if c != 1 {
			t.Fatalf("%s in %d units", m, c)
		}
	}
	for _, l := range p1.Leaves() {
		if p1.UnitTheoryBytes(l) > DevUnitMaxTheoryBytes {
			t.Fatalf("unit over budget: %d", p1.UnitTheoryBytes(l))
		}
	}

	// re-entry (what happens after every review FAIL) must NOT call the planner again,
	// even though the planner would now answer differently
	*calls = 0
	devPlannerRunner = func(pid int64, repo, prompt string) string { *calls++; return "unit: Mod13\nunit: Mod00\n" }
	p2 := dev_get_plan(1, "o/r", "sha1", theory, "einsteinOS/dev-1")
	if *calls != 0 {
		t.Fatalf("planner re-ran on re-entry (%d calls)", *calls)
	}
	a, b := p1.Leaves(), p2.Leaves()
	if len(a) != len(b) {
		t.Fatal("plan changed between entries")
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Fatalf("unit %d changed identity: %s -> %s", i, a[i].ID, b[i].ID)
		}
	}
}

func TestGetPlanSmallTheoryNeedsNoPlanner(t *testing.T) {
	theory := theoryWith(map[string]int{"A.lean": 500, "B.lean": 500}, []string{"A.lean", "B.lean"})
	calls := withPlanner(t, func(string) string { return "" })
	p := dev_get_plan(2, "o/r", "sha", theory, "b")
	if *calls != 0 || len(p.Leaves()) != 1 {
		t.Fatalf("calls=%d leaves=%d", *calls, len(p.Leaves()))
	}
}

func TestGetPlanReconcileAddsOnlyNewModules(t *testing.T) {
	sizes := map[string]int{"A.lean": 500, "B.lean": 500}
	theory := theoryWith(sizes, []string{"A.lean", "B.lean"})
	withPlanner(t, func(string) string { return "" })
	p := dev_get_plan(3, "o/r", "sha", theory, "b3")
	p.MarkUnitPass(p.Leaves()[0])
	before := p.Leaves()[0].ID

	sizes["C.lean"] = 500
	theory2 := theoryWith(sizes, []string{"A.lean", "B.lean", "C.lean"})
	p2 := dev_get_plan(3, "o/r", "sha", theory2, "b3")
	l := p2.Leaves()
	if len(l) != 2 {
		t.Fatalf("want old unit + one new unit, got %d", len(l))
	}
	if l[0].ID != before || !p2.UnitPass(l[0]) {
		t.Fatal("the passed unit must be untouched")
	}
	if len(l[1].Modules) != 1 || l[1].Modules[0] != "C.lean" {
		t.Fatalf("new unit = %v", l[1].Modules)
	}
}

func TestNeedsSplitIsNumbersOnly(t *testing.T) {
	p := newDevPlan("o/r", "s", []DevModule{{Name: "Big", Hash: "h", Bytes: 161215}, {Name: "Small", Hash: "h2", Bytes: 1000}},
		[][]string{{"Big"}, {"Small"}}, "plan")
	l := p.Leaves()
	if ok, _ := dev_node_needs_split(p, l[0]); !ok {
		t.Fatal("161 KB single module must trigger a split")
	}
	if ok, _ := dev_node_needs_split(p, l[1]); ok {
		t.Fatal("a 1 KB unit must not")
	}
	for i := 0; i < DevMaxFixAttempts; i++ {
		p.BumpFix(l[1].ID)
	}
	if ok, why := dev_node_needs_split(p, l[1]); !ok || !strings.Contains(why, "fix attempts") {
		t.Fatalf("exhausted fix attempts must trigger a split, got %v %q", ok, why)
	}
}

func bigModuleTheory(items int) string {
	var b strings.Builder
	b.WriteString("## MODULE: Big.lean\n")
	for i := 0; i < items; i++ {
		fmt.Fprintf(&b, "theorem item_%d : True := trivial\n%s\n", i, strings.Repeat("proof ", 50))
	}
	return b.String()
}

func TestSplitSingleModuleBelowModuleLevel(t *testing.T) {
	theory := bigModuleTheory(8)
	withPlanner(t, func(prompt string) string {
		if !strings.Contains(prompt, "item_0") || !strings.Contains(prompt, "may NOT start other conversations") {
			t.Errorf("planner prompt lacks the items / the leaf rule")
		}
		return "group: item_0, item_1, item_2\ngroup: item_3, item_4, bogus\ngroup: item_5\n" // item_6, item_7 unclaimed
	})
	p := newDevPlan("o/r", "s", []DevModule{{Name: "Big.lean", Hash: "h", Bytes: 200000}}, [][]string{{"Big.lean"}}, "plan")
	id := p.Leaves()[0].ID
	kids := dev_split_node(1, p, id, "o/r", "", "b", theory, "oversize")
	if len(kids) != 3 {
		t.Fatalf("kids=%d", len(kids))
	}
	if !strings.Contains(kids[2].Scope, "item_7") {
		t.Fatalf("unclaimed items must land in the last group: %q", kids[2].Scope)
	}
	if strings.Contains(kids[1].Scope, "bogus") {
		t.Fatal("an item that is not in the module must be dropped")
	}
	if len(p.Leaves()) != 3 {
		t.Fatalf("leaves=%d", len(p.Leaves()))
	}
	// a scoped child that must split again splits ITS items only
	withPlanner(t, func(string) string { return "" }) // garbage → deterministic bisect
	sub := dev_split_node(1, p, kids[0].ID, "o/r", "", "b", theory, "again")
	if sub != nil {
		t.Fatalf("a 3-item scope is below the 4-item floor and must refuse to split, got %d", len(sub))
	}
}

func TestSplitFallsBackToBisectWhenPlannerFails(t *testing.T) {
	theory := bigModuleTheory(8)
	withPlanner(t, func(string) string { return "I'm sorry, I can't help with that." })
	p := newDevPlan("o/r", "s", []DevModule{{Name: "Big.lean", Hash: "h", Bytes: 200000}}, [][]string{{"Big.lean"}}, "plan")
	kids := dev_split_node(1, p, p.Leaves()[0].ID, "o/r", "", "b", theory, "oversize")
	if len(kids) != 2 {
		t.Fatalf("planner failure must still split (bisect), got %d", len(kids))
	}
	// multi-module node, planner failure → bisect by bytes
	mods := []DevModule{{Name: "A", Hash: "a", Bytes: 40000}, {Name: "B", Hash: "b", Bytes: 40000}, {Name: "C", Hash: "c", Bytes: 40000}}
	q := newDevPlan("o/r", "s", mods, [][]string{{"A", "B", "C"}}, "plan")
	kids = dev_split_node(1, q, q.Leaves()[0].ID, "o/r", "", "b", "", "too wide")
	if len(kids) != 2 {
		t.Fatalf("kids=%d", len(kids))
	}
	if len(kids[0].Modules)+len(kids[1].Modules) != 3 {
		t.Fatal("bisect lost a module")
	}
}
