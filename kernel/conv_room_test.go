package kernel

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// When the server says the account has too many conversations at once (rate_limit_error
// concurrent_limit_reached), no further conversation is started until one of those running
// ends: the room is what the server took, the running less the refused. 11/19 08:10–08:17
// three conversations (49edf410, ef4f180a, a2983f70) were refused so at their first request,
// sixteen units' dev and review conversations side by side, and the run went on starting more.
func TestNoConversationIsStartedBeyondWhatTheServerTook(t *testing.T) {
	bootKernel()
	old := convExecFn
	t.Cleanup(func() { convExecFn = old; ResetConversationRoom() })
	ResetConversationRoom()

	release := make(chan struct{})
	started := make(chan string, 8)
	SetConvExec(func(pid int64, prompt string) (string, string) {
		ForkSlotTaken() // the child has taken what its fork handed it, as the model's does
		started <- prompt
		if prompt == "refused" {
			ConcurrentLimitReached() // the server refuses this one: three were running with it
			return `[Error] {"type":"error","error":{"type":"rate_limit_error","details":{"error_code":"concurrent_limit_reached"}}}`, ""
		}
		<-release
		return "", ""
	})
	for _, p := range []string{"unit25-review", "unit28-dev", "unit36-dev"} {
		if _, err := ForkWithRole(p, ".", "role_"+p); err != nil {
			t.Fatal(err)
		}
		<-started
	}
	r, _ := ForkWithRole("refused", ".", "role_refused")
	<-started
	Wait(r)
	if room := ConversationRoom(); room != 3 {
		t.Fatalf("room after the refusal %d, want 3 (the running ones)", room)
	}

	next := make(chan int64, 1)
	go func() {
		pid, _ := ForkWithRole("unit41-dev", ".", "role_unit41-dev")
		next <- pid
	}()
	select {
	case p := <-started:
		t.Fatalf("%s was started beside the three the server took", p)
	case <-time.After(300 * time.Millisecond):
	}
	release <- struct{}{} // one of the three ends
	if p := <-started; p != "unit41-dev" {
		t.Fatalf("started %s", p)
	}
	close(release)
	Wait(<-next)
}

// The run starts no more units than the server takes conversations at once: a unit started
// past it would hold a mirror and a clone while its fork waits.
func TestNoMoreUnitsRunThanTheServerTakesConversations(t *testing.T) {
	old := devRepoLocalDirHook
	devRepoLocalDirHook = func(string) (string, error) { return "", errors.New("no clone in this test") }
	defer func() { devRepoLocalDirHook = old }()
	ResetConversationRoom()
	defer ResetConversationRoom()
	convRoomMu.Lock()
	convLive = 2
	convRoomMu.Unlock()
	ConcurrentLimitReached() // two running, one refused: the server takes one
	convRoomMu.Lock()
	convLive = 0
	convRoomMu.Unlock()

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
		mu.Unlock()
		time.Sleep(100 * time.Millisecond)
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
		t.Fatalf("%d units ran at once with room for 1; %d started", most, started)
	}
}
