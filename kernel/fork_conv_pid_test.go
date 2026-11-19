package kernel

import (
	"sync"
	"testing"
)

// A forked child's conversation is bound to that child, however many forks came after it.
// 11/19 08:12:19 the child of dev_fix_2_pb0569948de ended as a9b38d97 (unit 25's review) and
// 08:17:01 the child of dev_fix_1_pcd027bc02f as 07bce9de (unit 28's review): sixteen units
// forked side by side and each child took "the most recently forked pid" for its own.
func TestEachForkedChildRunsItsOwnConversation(t *testing.T) {
	bootKernel()
	old := convExecFn
	t.Cleanup(func() { convExecFn = old })

	const n = 4
	var mu sync.Mutex
	handed := map[int64]string{}   // pid the fork handed → the conversation it started
	release := make(chan struct{}) // every child waits until all four have been forked
	started := make(chan struct{}, n)
	SetConvExec(func(pid int64, prompt string) (string, string) {
		mu.Lock()
		handed[pid] = "conv-" + prompt
		mu.Unlock()
		SetConvID(pid, "conv-"+prompt)
		started <- struct{}{}
		<-release
		return "done " + prompt, ""
	})

	pids := map[string]int64{}
	for _, name := range []string{"unit25-review", "unit28-dev", "unit36-dev", "unit41-dev"} {
		pid, err := Fork(name, ".")
		if err != nil {
			t.Fatal(err)
		}
		pids[name] = pid
	}
	for i := 0; i < n; i++ {
		<-started
	}
	close(release)
	for name, pid := range pids {
		Wait(pid)
		mu.Lock()
		got := handed[pid]
		mu.Unlock()
		if got != "conv-"+name {
			t.Fatalf("the child forked for %s (pid %d) was handed %q", name, pid, got)
		}
	}
	for name, pid := range pids {
		if c := getMeta(pid).ConvID; c != "conv-"+name {
			t.Errorf("the child forked for %s (pid %d) is bound to %q, want %q", name, pid, c, "conv-"+name)
		}
	}
}

// A child that returns keeps the conversation it was bound to: when its fork returns, nothing
// another conversation set since replaces it. 38affe4a made the binding per pid (SetConvID) but
// left the fork reading the global forkConvID when its per-pid map was empty — and the map was
// never written — so each returning child took whatever conversation was set last anywhere.
func TestAReturningChildKeepsItsOwnConversation(t *testing.T) {
	bootKernel()
	old := convExecFn
	t.Cleanup(func() { convExecFn = old })

	second := make(chan struct{})
	SetConvExec(func(pid int64, prompt string) (string, string) {
		SetConvID(pid, "conv-"+prompt)
		RegisterConv(0, "conv-"+prompt, "o/r", prompt, 0)
		if prompt == "unit36-dev" {
			<-second // unit 28's review starts while unit 36's conversation is still running
		}
		return "", ""
	})
	a, _ := Fork("unit36-dev", ".")
	b, _ := Fork("unit28-review", ".") // starts while unit 36's conversation is still running
	Wait(b)
	close(second)
	Wait(a)
	if c := getMeta(a).ConvID; c != "conv-unit36-dev" {
		t.Fatalf("the child of unit36-dev returned bound to %q", c)
	}
	if r := FindConv("o/r", "unit36-dev", 0); r == nil || r.Status != "done" {
		t.Fatalf("the returning child's own conversation is not marked done: %+v", r)
	}
}

// Why a conversation ended is recorded on its own task. The model layer stored its done and
// termination signals on "the most recently forked pid", another unit's child when units run
// side by side.
func TestAConversationsStopSignalIsItsOwnTasks(t *testing.T) {
	bootKernel()
	old := convExecFn
	t.Cleanup(func() { convExecFn = old })
	hold := make(chan struct{})
	bound := make(chan struct{}, 2)
	SetConvExec(func(pid int64, prompt string) (string, string) {
		SetConvID(pid, "sig-"+prompt)
		bound <- struct{}{}
		<-hold
		return "", ""
	})
	a, _ := Fork("unit32-dev", ".")
	b, _ := Fork("unit36-dev", ".") // forked last
	<-bound
	<-bound
	StoreSignalForConv("sig-unit32-dev", &StopSignal{Reason: "done_marker"})
	close(hold)
	Wait(a)
	Wait(b)
	if s := GetSignal(a); s == nil || s.Reason != "done_marker" {
		t.Fatalf("unit 32's done line was not recorded on its task: %v", s)
	}
	if s := GetSignal(b); s != nil {
		t.Fatalf("unit 32's done line was recorded on unit 36's task: %v", s)
	}
	StoreSignalForConv("not-a-child", &StopSignal{Reason: "done_marker"}) // records nothing, panics not
}
