package kernel

import (
	"context"
	"testing"
	"time"
)

// A task that ignores SIGINT is cancelled, and Wait waits for it to end; one that ends on
// neither is marked abandoned.
func TestAnUnresponsiveTaskIsCancelledAndWaitedFor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &TaskMeta{Ctx: ctx, Cancel: cancel, Done: make(chan struct{})}
	go func() { <-ctx.Done(); time.Sleep(50 * time.Millisecond); close(m.Done) }()
	start := time.Now()
	stopUnresponsive(990002, m, time.Second)
	if m.Abandoned || time.Since(start) < 40*time.Millisecond {
		t.Fatalf("it ended on cancel and was waited for: abandoned=%v after %v", m.Abandoned, time.Since(start))
	}
	stuck := &TaskMeta{Done: make(chan struct{})}
	stopUnresponsive(990003, stuck, 50*time.Millisecond)
	if !stuck.Abandoned {
		t.Fatal("a task that does not end is abandoned")
	}
}

// The unit's next conversation is not started while the one given up on still runs.
func TestTheNextConversationWaitsForTheAbandonedOne(t *testing.T) {
	const pid = int64(990004)
	m := &TaskMeta{Done: make(chan struct{}), Abandoned: true}
	metaMu.Lock()
	taskMeta[pid] = m
	metaMu.Unlock()
	defer func() { metaMu.Lock(); delete(taskMeta, pid); metaMu.Unlock() }()
	go func() { time.Sleep(80 * time.Millisecond); close(m.Done) }()
	start := time.Now()
	waitAbandoned(pid, "o/r", "uintegratione5509707730d")
	if time.Since(start) < 70*time.Millisecond {
		t.Fatal("returned while the conversation ran")
	}
	m2 := &TaskMeta{Done: make(chan struct{})}
	metaMu.Lock()
	taskMeta[pid] = m2
	metaMu.Unlock()
	start = time.Now()
	waitAbandoned(pid, "o/r", "u")
	if time.Since(start) > 20*time.Millisecond {
		t.Fatal("a conversation that was not given up on is not waited for here")
	}
}
