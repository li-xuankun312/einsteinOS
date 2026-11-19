package kernel

import (
	"testing"
	"time"
)

// Two forks at once: the second does not fill the slots until the first fork's child has
// taken its role — otherwise that child starts with the second fork's role.
func TestASecondForkWaitsForTheFirstChildToTakeItsRole(t *testing.T) {
	defer ForkSlotTaken()
	claimForkSlot("dev_unit_3")
	SetForkRole("dev_unit_3")
	second := make(chan struct{})
	go func() {
		claimForkSlot("dev_unit_4")
		SetForkRole("dev_unit_4")
		close(second)
	}()
	select {
	case <-second:
		t.Fatalf("the second fork filled the slots before the first child took its role")
	case <-time.After(100 * time.Millisecond):
	}
	if got := ConsumeForkRole(); got != "dev_unit_3" {
		t.Fatalf("the first child took %q", got)
	}
	ForkSlotTaken()
	select {
	case <-second:
	case <-time.After(2 * time.Second):
		t.Fatalf("the second fork never got the slots")
	}
	if got := ConsumeForkRole(); got != "dev_unit_4" {
		t.Fatalf("the second child took %q", got)
	}
}
