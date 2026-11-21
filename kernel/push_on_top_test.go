package kernel

import (
	"errors"
	"testing"
)

// A push rejected because another machine moved the branch is written again on top of the
// branch and pushed again; any other rejection stays final. The rejection text is GitPushRetry's.
func TestAPushTheRemoteOutranIsWrittenAgainOnTopOfIt(t *testing.T) {
	moved := errors.New("push of einsteinOS/dev-3b59928b8f rejected by the remote — not retrying: ! [rejected] einsteinOS/dev-3b59928b8f -> einsteinOS/dev-3b59928b8f (non-fast-forward)")
	writes, pushes := 0, 0
	err := pushOnTopOfTheRemote("einsteinOS/dev-3b59928b8f", 3, func() error { writes++; return nil }, func() error {
		pushes++
		if pushes == 1 {
			return moved
		}
		return nil
	})
	if err != nil || writes != 2 || pushes != 2 {
		t.Fatalf("err=%v writes=%d pushes=%d", err, writes, pushes)
	}

	policy := errors.New("push of x rejected by the remote — not retrying: ! [remote rejected] x -> x (protected branch hook declined)")
	writes, pushes = 0, 0
	err = pushOnTopOfTheRemote("x", 3, func() error { writes++; return nil }, func() error { pushes++; return policy })
	if err != policy || writes != 1 || pushes != 1 {
		t.Fatalf("a policy rejection is final: err=%v writes=%d pushes=%d", err, writes, pushes)
	}

	writes, pushes = 0, 0
	err = pushOnTopOfTheRemote("x", 3, func() error { writes++; return nil }, func() error { pushes++; return moved })
	if err != moved || pushes != 3 {
		t.Fatalf("bounded: err=%v pushes=%d", err, pushes)
	}
}
