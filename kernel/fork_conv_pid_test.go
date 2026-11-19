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
