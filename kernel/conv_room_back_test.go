package kernel

import (
	"testing"
	"time"
)

// 11/24 03:47:50 and 04:32:52 each place given back after the wait was followed by a running part's
// refusal: a refusal within the wait after a place was given back takes the place back and
// doubles the wait; a place that holds a whole wait brings the wait back to the base
func TestAPlaceGivenBackAndRefusedAgainDoublesTheWait(t *testing.T) {
	old := convRoomQuiet
	convRoomQuiet = 40 * time.Millisecond
	ResetConversationRoom()
	defer func() { convRoomQuiet = old; ResetConversationRoom() }()

	convRoomMu.Lock()
	convLive = 7
	convRoomMu.Unlock()
	ConcurrentLimitReached() // 7 running: the server took 6
	if ConversationRoom() != 6 {
		t.Fatalf("room %d", ConversationRoom())
	}
	time.Sleep(50 * time.Millisecond)
	if ConversationRoom() != 7 {
		t.Fatalf("a place is given back after the wait: %d", ConversationRoom())
	}
	convRoomMu.Lock()
	convLive = 7
	convRoomMu.Unlock()
	ConcurrentLimitReached() // refused again right after: the place was not there
	if r := ConversationRoom(); r != 6 {
		t.Fatalf("the place given back is taken back: %d", r)
	}
	if convRoomQuietNow() != 80*time.Millisecond {
		t.Fatalf("the wait is doubled: %v", convRoomQuietNow())
	}
	time.Sleep(50 * time.Millisecond)
	if ConversationRoom() != 6 {
		t.Fatal("a place given back before the doubled wait")
	}
	convRoomMu.Lock()
	convLive = 0
	convRoomMu.Unlock()
}
