package kernel

import (
	"log"
	"time"
)

// A fork hands its child the role, termination condition and files through one set of
// pending slots (SetForkRole, SetPendingTermination, SetForkFiles), which the child takes when
// its conversation starts (model's RunConversation and completionLoop). Two forks at once —
// units of a dev run that run side by side — would each have the other's child take its
// slots. The slots are therefore one fork's at a time: a fork claims them before it fills
// them, and they are free again when its child has taken them (ForkSlotTaken), or when the
// fork itself fails.
var forkSlot = make(chan struct{}, 1)

// forkSlotWait: how long a fork waits for the slots before it goes on anyway — a child that
// never starts must not stop every later fork.
var forkSlotWait = 3 * time.Minute

// claimForkSlot waits until no other fork's child is still to take the slots.
func claimForkSlot(role string) {
	select {
	case forkSlot <- struct{}{}:
	case <-time.After(forkSlotWait):
		log.Printf("fork_slot: %s waited %s for the previous fork's child to take its role; going on", role, forkSlotWait)
	}
}

// ForkSlotTaken frees the slots once a child has taken its role, termination and files.
func ForkSlotTaken() {
	select {
	case <-forkSlot:
	default:
	}
}
