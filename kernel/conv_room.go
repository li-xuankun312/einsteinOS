package kernel

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
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
	now := time.Now()
	// a refusal within the wait after a place was given back: the place was not there — it is
	// taken back and the next is given only after twice the wait. 11/24 03:47:50 and 04:32:52
	// each place given back after 10m was followed by the refusal of a running part (layer_norm,
	// moe), whose conversation ended for it
	if !convRoomWidened.IsZero() && now.Sub(convRoomWidened) < convRoomQuietNow() {
		if took >= convRoom && convRoom > 1 {
			took = convRoom - 1
		}
		if convRoomQuietCur < convRoomQuietMax {
			convRoomQuietCur = 2 * convRoomQuietNow()
			if convRoomQuietCur > convRoomQuietMax {
				convRoomQuietCur = convRoomQuietMax
			}
		}
		log.Printf(convRoomBackFmt, convLive, took, convRoomQuietCur)
	}
	if convRoom == 0 || took < convRoom {
		log.Printf("conv_room: the server refused a conversation for the account's concurrency with %d running — no more than %d run at once until %v pass without a refusal", convLive, took, convRoomQuietNow())
		convRoom = took
	}
	convRoomSince = now
	convRoomRefused = now
	saveConvRoomLocked()
}

// convRoomFile: where the room measured is kept across restarts — 11/24 09:35:11 a restart began
// with no room known, started six parts beside the unit's conversation, and the server refused
// two of them at once (09:41:15, 09:41:36), each ending with its first round
var convRoomFile = func() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".einsteinos", "conv_room.json")
}

// convRoomKeep: how long a room measured before a restart still holds after it.
const convRoomKeep = 2 * time.Hour

type convRoomSaved struct {
	Room    int       `json:"room"`
	Refused time.Time `json:"refused"`
}

func saveConvRoomLocked() {
	p := convRoomFile()
	if p == "" || convRoom <= 0 {
		return
	}
	b, _ := json.Marshal(convRoomSaved{Room: convRoom, Refused: convRoomRefused})
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, b, 0o600)
}

var convRoomLoaded bool

// loadConvRoomLocked: the room measured before a restart, when it was measured within convRoomKeep.
func loadConvRoomLocked() {
	if convRoomLoaded {
		return
	}
	convRoomLoaded = true
	p := convRoomFile()
	if p == "" || convRoom != 0 {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var sv convRoomSaved
	if json.Unmarshal(b, &sv) != nil || sv.Room <= 0 || time.Since(sv.Refused) > convRoomKeep {
		return
	}
	convRoom, convRoomSince, convRoomRefused = sv.Room, sv.Refused, sv.Refused
	log.Printf("conv_room: %d conversations at once, as the server refused more %v ago before this restart", sv.Room, time.Since(sv.Refused).Round(time.Minute))
}

// %[1]d running, %[2]d the room, %[3]v the wait now
const convRoomBackFmt = "conv_room: refused again with %[1]d running within the wait after a place was given back — %[2]d run at once, and the next place is given after %[3]v"

var (
	convRoomQuietCur time.Duration // the wait now (0: convRoomQuiet)
	convRoomWidened  time.Time     // when a place was last given back
	convRoomRefused  time.Time     // the last refusal
)

// convRoomQuietMax: the longest wait before a place is given back.
const convRoomQuietMax = 2 * time.Hour

func convRoomQuietNow() time.Duration {
	if convRoomQuietCur <= 0 {
		return convRoomQuiet
	}
	return convRoomQuietCur
}

// ConversationRoom: how many conversations may run at once now (0: no bound measured).
func ConversationRoom() int {
	convRoomMu.Lock()
	defer convRoomMu.Unlock()
	loadConvRoomLocked()
	widenLocked()
	return convRoom
}

func widenLocked() {
	for convRoom > 0 && time.Since(convRoomSince) >= convRoomQuietNow() {
		// a place given back that held a whole wait without a refusal: the wait is the base again
		if !convRoomWidened.IsZero() && convRoomRefused.Before(convRoomWidened) {
			convRoomQuietCur = 0
		}
		convRoom++
		convRoomSince = convRoomSince.Add(convRoomQuietNow())
		convRoomWidened = time.Now()
		log.Printf("conv_room: %v without a refusal — %d conversations may run at once", convRoomQuietNow(), convRoom)
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
	convRoomQuietCur, convRoomWidened, convRoomRefused = 0, time.Time{}, time.Time{}
	convRoomLoaded = true
	convRoomMu.Unlock()
	convRoomCond.Broadcast()
}

// WaitConversationRoomRunning blocks a running conversation the server refused for the account's
// concurrency until the conversations running, itself among them, are within the room measured
// — another has ended or convRoomQuiet gave a place back — so it sends its round again instead
// of ending. 11/23 02:19:23 the integration's part conversation 56439a80 was refused at its
// third round with 7 running and ended with an error, its work cut where it stood.
// It says whether it waited: a refusal with the room not exceeded here (another account's
// conversations, a count that is off) is waited out by the caller instead.
func WaitConversationRoomRunning(conv string) bool {
	convRoomMu.Lock()
	defer convRoomMu.Unlock()
	said := false
	for {
		widenLocked()
		if convRoom == 0 || convLive <= convRoom {
			return said
		}
		if !said {
			said = true
			log.Printf("conv_room: %s was refused for the account's concurrency — it waits, %d conversations running, the server took %d at once, and sends its round again", conv, convLive, convRoom)
		}
		wake := time.AfterFunc(time.Until(convRoomSince.Add(convRoomQuiet)), convRoomCond.Broadcast)
		convRoomCond.Wait()
		wake.Stop()
	}
}
