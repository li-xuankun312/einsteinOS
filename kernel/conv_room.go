package kernel

import (
	"log"
	"sync"
	"time"
)

// The server limits how many conversations of an account run at once, and says so when a
// request goes past it: rate_limit_error, concurrent_limit_reached ("representativeClaim":
// "concurrents"). 11/19 08:10–08:17 three conversations were refused so at their first request
// (49edf410 unit 25's review, ef4f180a unit 28's dev, a2983f70 unit 36's dev): sixteen units'
// dev and review conversations side by side, bounded only by the request window, which counts
// requests over time, not conversations at once.
//
// The room is measured from the refusals, as 104ecb85 measures the request window: when the
// server refuses one, the room is what it took — the conversations running, less the refused
// one. No conversation is started past it until one ends; every convRoomQuiet without a
// refusal gives one place back. Before any refusal there is no bound here.
var (
	convRoomMu    sync.Mutex
	convRoomCond  = sync.NewCond(&convRoomMu)
	convLive      int       // forked conversations running now (Fork's goroutine counts them)
	convRoom      int       // 0: unbounded
	convRoomSince time.Time // the last refusal, or the last place given back
)

// convRoomQuiet: how long without a refusal gives one place back (the proxy's window).
var convRoomQuiet = 10 * time.Minute

// ConcurrentLimitReached: the server refused a conversation for the account's concurrency
// (the model layer calls it from the refused conversation, which still counts as running).
func ConcurrentLimitReached() {
	convRoomMu.Lock()
	defer convRoomMu.Unlock()
	took := convLive - 1
	if took < 1 {
		took = 1
	}
	if convRoom == 0 || took < convRoom {
		log.Printf("conv_room: the server refused a conversation for the account's concurrency with %d running — no more than %d run at once until %v pass without a refusal", convLive, took, convRoomQuiet)
		convRoom = took
	}
	convRoomSince = time.Now()
}

// ConversationRoom: how many conversations may run at once now (0: no bound measured).
func ConversationRoom() int {
	convRoomMu.Lock()
	defer convRoomMu.Unlock()
	widenLocked()
	return convRoom
}

func widenLocked() {
	for convRoom > 0 && time.Since(convRoomSince) >= convRoomQuiet {
		convRoom++
		convRoomSince = convRoomSince.Add(convRoomQuiet)
		log.Printf("conv_room: %v without a refusal — %d conversations may run at once", convRoomQuiet, convRoom)
	}
}

// waitConversationRoom blocks a fork until a conversation more may run (called before the
// fork claims its slots, so a waiting fork holds nothing another needs).
func waitConversationRoom(role string) {
	convRoomMu.Lock()
	defer convRoomMu.Unlock()
	said := false
	for {
		widenLocked()
		if convRoom == 0 || convLive < convRoom {
			return
		}
		if !said {
			said = true
			log.Printf("conv_room: %s waits — %d conversations run, the server took %d at once", role, convLive, convRoom)
		}
		wake := time.AfterFunc(time.Until(convRoomSince.Add(convRoomQuiet)), convRoomCond.Broadcast)
		convRoomCond.Wait()
		wake.Stop()
	}
}

func convStarted() {
	convRoomMu.Lock()
	convLive++
	convRoomMu.Unlock()
}

func convEnded() {
	convRoomMu.Lock()
	convLive--
	convRoomMu.Unlock()
	convRoomCond.Broadcast()
}

// ResetConversationRoom: no bound measured (tests).
func ResetConversationRoom() {
	convRoomMu.Lock()
	convRoom, convRoomSince = 0, time.Time{}
	convRoomMu.Unlock()
	convRoomCond.Broadcast()
}
