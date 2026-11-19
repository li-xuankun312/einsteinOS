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
