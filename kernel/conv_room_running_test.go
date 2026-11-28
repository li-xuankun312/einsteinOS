package kernel

import (
	"testing"
	"time"
)

// a running conversation refused for the account's concurrency waits for another to end and
// then goes on, instead of ending
func TestARefusedConversationWaitsForRoomAndGoesOn(t *testing.T) {
	ResetConversationRoom()
	defer ResetConversationRoom()
	convRoomMu.Lock()
	saved := convLive
	convLive = 7
	convRoomMu.Unlock()
	defer func() { convRoomMu.Lock(); convLive = saved; convRoomMu.Unlock() }()
	ConcurrentLimitReached() // room 6, 7 running
	done := make(chan bool)
	go func() { done <- WaitConversationRoomRunning("56439a80") }()
	select {
	case <-done:
		t.Fatal("went on with 7 running in a room of 6")
	case <-time.After(100 * time.Millisecond):
	}
	convEnded() // another conversation ended
	select {
	case waited := <-done:
		if !waited {
			t.Fatal("it waited, and said it did not")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not go on after a conversation ended")
	}
	if WaitConversationRoomRunning("x") {
		t.Fatal("within the room it said it waited")
	}
}
