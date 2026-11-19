package kernel

import (
	"log"
	"sync"
	"time"
)

// A fork hands its child the role, termination condition and files through one set of
// pending slots (SetForkRole, SetPendingTermination, SetForkFiles), which the child takes when
// its conversation starts (model's RunConversation and completionLoop, the role last). Two
// forks at once — units of a dev run side by side (dev_schedule.go) — would each have the
// other's child take its slots. The slots are therefore one fork's at a time: a fork claims
// them before it fills them; they are free again when a child takes the role
// (ConsumeForkRole), when the fork fails, or when the fork's child ends without having taken
// them — that last only for the fork's own claim, never another's.
var (
	forkSlotMu    sync.Mutex
	forkSlotHeld  bool
	forkSlotGen   int64
	forkSlotFreed = make(chan struct{})
)

// forkSlotWait: how long a fork waits for the slots before it goes on anyway.
var forkSlotWait = 3 * time.Minute

// claimForkSlot waits until no other fork's child is still to take the slots; it returns this
// claim's generation.
func claimForkSlot(role string) int64 {
	deadline := time.Now().Add(forkSlotWait)
	for {
		forkSlotMu.Lock()
		if !forkSlotHeld || time.Now().After(deadline) {
			if forkSlotHeld {
				log.Printf("fork_slot: %s waited %s for the previous fork's child to take its role; going on", role, forkSlotWait)
			}
			forkSlotHeld = true
			forkSlotGen++
			g := forkSlotGen
			forkSlotMu.Unlock()
			return g
		}
		freed := forkSlotFreed
		forkSlotMu.Unlock()
		select {
		case <-freed:
		case <-time.After(time.Until(deadline)):
		}
	}
}

// releaseForkSlot frees the slots if claim gen still holds them (gen 0: whoever holds them).
func releaseForkSlot(gen int64) {
	forkSlotMu.Lock()
	defer forkSlotMu.Unlock()
	if forkSlotHeld && (gen == 0 || gen == forkSlotGen) {
		forkSlotHeld = false
		close(forkSlotFreed)
		forkSlotFreed = make(chan struct{})
	}
}

// ForkSlotTaken frees the slots once a child has taken what its fork handed it.
func ForkSlotTaken() { releaseForkSlot(0) }

// releaseForkSlotWhenDone frees claim gen when the fork's child ends, if nothing freed it before.
func releaseForkSlotWhenDone(pid int64, gen int64) {
	m := getMeta(pid)
	if m == nil || m.Done == nil {
		releaseForkSlot(gen)
		return
	}
	go func() {
		<-m.Done
		releaseForkSlot(gen)
	}()
}
