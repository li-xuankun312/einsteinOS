package kernel

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// Units that nothing links run side by side; a unit linked to a running one waits for it; the
// integration unit runs last, alone.
func TestIndependentUnitsRunSideBySideAndTheIntegrationUnitLastAlone(t *testing.T) {
	old := devRepoLocalDirHook
	devRepoLocalDirHook = func(string) (string, error) { return "", errors.New("no clone in this test") }
	defer func() { devRepoLocalDirHook = old }()

	mods := []DevModule{{Name: "A.lean", Hash: "a", Bytes: 100}, {Name: "B.lean", Hash: "b", Bytes: 100}, {Name: "C.lean", Hash: "c", Bytes: 100}}
	p := newDevPlan("o/r", "s", mods, [][]string{{"A.lean"}, {"B.lean"}, {"C.lean"}}, "plan")
	p.SetIntegrationUnit("uintegration")
	leaves := p.Leaves()
	id := map[string]string{}
	for _, l := range leaves {
		if len(l.Modules) == 1 {
			id[l.Modules[0]] = l.ID
		}
	}
	r := &devRun{repo: "o/r", devRepo: "o/d", branch: "b", plan: p, raw: map[string]UnitRequirements{
		id["A.lean"]:   {Sources: []string{"src/x.cpp"}},
		id["B.lean"]:   {Sources: []string{"src/y.cpp"}},
		id["C.lean"]:   {Sources: []string{"src/x.cpp"}}, // linked to A
		"uintegration": {},
	}}

	var mu sync.Mutex
	runningNow := map[string]bool{}
	var together [][2]string
	order := []string{}
	attempt := func(u DevPlanNode, idx, total int) (string, bool, bool) {
		mu.Lock()
		for other := range runningNow {
			together = append(together, [2]string{other, u.ID})
		}
		runningNow[u.ID] = true
		order = append(order, u.ID)
		mu.Unlock()
		time.Sleep(60 * time.Millisecond)
		mu.Lock()
		delete(runningNow, u.ID)
		mu.Unlock()
		r.mu.Lock()
		p.MarkUnitPass(u)
		r.mu.Unlock()
		return "", true, false
	}
	_, err, requeued := r.schedule("", map[string]bool{}, attempt)
	if err != nil || requeued {
		t.Fatalf("%v %v", err, requeued)
	}
	pair := func(a, b string) bool {
		for _, t := range together {
			if (t[0] == a && t[1] == b) || (t[0] == b && t[1] == a) {
				return true
			}
		}
		return false
	}
	if !pair(id["A.lean"], id["B.lean"]) {
		t.Fatalf("independent units did not run side by side: %v", together)
	}
	if pair(id["A.lean"], id["C.lean"]) {
		t.Fatalf("units that share a source ran at once")
	}
	if len(order) != 4 || order[3] != "uintegration" {
		t.Fatalf("order %v", order)
	}
	for _, tp := range together {
		if tp[0] == "uintegration" || tp[1] == "uintegration" {
			t.Fatalf("the integration unit ran beside %v", tp)
		}
	}
}

// A unit marked to run alone (its delta met another unit's commit) has no other unit beside it.
func TestAUnitMarkedAloneRunsWithNoOtherBesideIt(t *testing.T) {
	old := devRepoLocalDirHook
	devRepoLocalDirHook = func(string) (string, error) { return "", errors.New("no clone in this test") }
	defer func() { devRepoLocalDirHook = old }()
	mods := []DevModule{{Name: "A.lean", Hash: "a", Bytes: 100}, {Name: "B.lean", Hash: "b", Bytes: 100}}
	p := newDevPlan("o/r", "s", mods, [][]string{{"A.lean"}, {"B.lean"}}, "plan")
	leaves := p.Leaves()
	r := &devRun{repo: "o/r", devRepo: "o/d", branch: "b", plan: p, raw: map[string]UnitRequirements{
		leaves[0].ID: {Sources: []string{"x"}}, leaves[1].ID: {Sources: []string{"y"}},
	}}
	r.markAlone(leaves[0].ID)
	var mu sync.Mutex
	n, most := 0, 0
	attempt := func(u DevPlanNode, idx, total int) (string, bool, bool) {
		mu.Lock()
		n++
		if n > most {
			most = n
		}
		mu.Unlock()
		time.Sleep(40 * time.Millisecond)
		mu.Lock()
		n--
		mu.Unlock()
		r.mu.Lock()
		p.MarkUnitPass(u)
		r.mu.Unlock()
		return "", true, false
	}
	r.schedule("", map[string]bool{}, attempt)
	if most != 1 {
		t.Fatalf("%d units ran at once", most)
	}
}
