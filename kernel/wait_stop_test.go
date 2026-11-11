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
