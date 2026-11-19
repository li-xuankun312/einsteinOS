package kernel

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// While the proxy's quota for this account is spent, no unit is started: a conversation
// started now would hold a mirror, a clone and a fork slot for hours and spend its tries on
// refusals. When the quota is back, the units start.
func TestNoUnitIsStartedWhileTheProxysQuotaIsSpent(t *testing.T) {
	old := devRepoLocalDirHook
	devRepoLocalDirHook = func(string) (string, error) { return "", errors.New("no clone in this test") }
	defer func() { devRepoLocalDirHook = old }()
	oldSleep := quotaSleep
	defer func() { quotaSleep = oldSleep }()

	proxyQuotaMu.Lock()
	proxyQuotaUntil = time.Now().Add(2 * time.Hour)
	proxyQuotaMu.Unlock()
	defer func() { proxyQuotaMu.Lock(); proxyQuotaUntil = time.Time{}; proxyQuotaMu.Unlock() }()

	var waits int
	quotaSleep = func(d time.Duration) { // the quota comes back while the run waits
		waits++
		proxyQuotaMu.Lock()
		proxyQuotaUntil = time.Time{}
		proxyQuotaMu.Unlock()
	}
	mods := []DevModule{{Name: "A.lean", Hash: "a", Bytes: 100}}
	p := newDevPlan("o/r", "s", mods, [][]string{{"A.lean"}}, "plan")
	r := &devRun{repo: "o/r", devRepo: "o/d", branch: "b", plan: p,
		raw: map[string]UnitRequirements{p.Leaves()[0].ID: {Sources: []string{"x"}}}}
	var mu sync.Mutex
	started := 0
	attempt := func(u DevPlanNode, idx, total int) (string, bool, bool) {
		mu.Lock()
		started++
		if waits == 0 {
			t.Errorf("a unit started while the quota was spent")
		}
		mu.Unlock()
		r.mu.Lock()
		p.MarkUnitPass(u)
		r.mu.Unlock()
		return "", true, false
	}
	r.schedule("", map[string]bool{}, attempt)
	if waits == 0 || started != 1 {
		t.Fatalf("waits=%d started=%d", waits, started)
	}
}

// While the proxy refuses the account itself ("你当前的车暂时无法使用，请重新选择车辆", 11/19
// 08:29:51), no unit is started beside the ones running: 08:29:55 the last attempt of
// pcd027bc02f was started into it. With none running one starts, and its answered request
// is what lets the others start again.
func TestNoUnitIsStartedBesideTheRunningWhileTheProxyRefusesTheAccount(t *testing.T) {
	old := devRepoLocalDirHook
	devRepoLocalDirHook = func(string) (string, error) { return "", errors.New("no clone in this test") }
	defer func() { devRepoLocalDirHook = old }()
	SetProxyAccountUnusable(`{"message":"你当前的车暂时无法使用，请重新选择车辆。","type":"permission_error"}`)
	defer ProxyAccountAnswered()

	mods := []DevModule{{Name: "A.lean", Hash: "a", Bytes: 100}, {Name: "B.lean", Hash: "b", Bytes: 100}}
	p := newDevPlan("o/r", "s", mods, [][]string{{"A.lean"}, {"B.lean"}}, "plan")
	leaves := p.Leaves()
	r := &devRun{repo: "o/r", devRepo: "o/d", branch: "b", plan: p, raw: map[string]UnitRequirements{
		leaves[0].ID: {Sources: []string{"src/a.cpp"}}, leaves[1].ID: {Sources: []string{"src/b.cpp"}}}}
	var mu sync.Mutex
	active, most, started := 0, 0, 0
	attempt := func(u DevPlanNode, idx, total int) (string, bool, bool) {
		mu.Lock()
		active++
		started++
		if active > most {
			most = active
		}
		first := started == 1
		mu.Unlock()
		if first {
			// the other unit would be started now, were it allowed; then the account answers
			time.Sleep(200 * time.Millisecond)
			ProxyAccountAnswered()
		}
		mu.Lock()
		active--
		mu.Unlock()
		r.mu.Lock()
		p.MarkUnitPass(u)
		r.mu.Unlock()
		return "", true, false
	}
	r.schedule("", map[string]bool{}, attempt)
	if most != 1 || started != 2 {
		t.Fatalf("while the account was refused %d units ran at once (want 1); %d started (want 2)", most, started)
	}
}
