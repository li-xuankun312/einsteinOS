package kernel

import (
	"testing"
	"time"
)

// A unit holds the run's plan only while it does not wait on a conversation: while one unit's
// conversation runs, another unit takes the plan; when the conversation returns, the first
// unit has it again before it goes on.
func TestAUnitLetsGoOfThePlanWhileItsConversationRuns(t *testing.T) {
	r := &devRun{}
	r.mu.Lock()
	conversation := make(chan struct{})
	other := make(chan struct{})
	go func() {
		r.mu.Lock() // another unit, recording its verdict
		close(other)
		r.mu.Unlock()
	}()
	select {
	case <-other:
		t.Fatalf("another unit took the plan while this one was not waiting")
	case <-time.After(50 * time.Millisecond):
	}
	go func() { <-other; close(conversation) }()
	r.whileWaiting(func() { <-conversation })
	if r.mu.TryLock() {
		t.Fatalf("the unit does not hold the plan again after its conversation")
	}
	r.mu.Unlock()
}
