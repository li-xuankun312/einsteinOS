package kernel

// mirror_catchup.go — the kernel brings a conversation's mirror up to what its remote ran
// before it judges the conversation's work.
//
// The kernel verifies the mirror, and the mirror only holds what the stream carried. 11/17
// dev_unit_8 (157e4fa7): round 10's stream carried no tool call and ended without a
// stop_reason at 06:41:47; the remote went on to edit five Lean proofs and run its tests, and
// dev_collect waited until 06:45 for it to finish — then the kernel verified a mirror without
// any of it (lean_unproved), and the next attempt started from that mirror. The remote's own
// account of its work (its text, its done line) is not read here: what the kernel judges is
// the tree, verified on this machine.

import "sync"

var (
	mirrorCatchUpMu sync.Mutex
	mirrorCatchUpFn func(convID string) (ran []string, err error)
)

// SetMirrorCatchUpFn registers how a conversation's mirror is brought up to its remote: run
// here, in order, the tool calls the stream did not carry; ran names them.
func SetMirrorCatchUpFn(fn func(convID string) (ran []string, err error)) {
	mirrorCatchUpMu.Lock()
	mirrorCatchUpFn = fn
	mirrorCatchUpMu.Unlock()
}

// mirrorCatchUp runs the registered catch-up for convID; with none registered it runs nothing.
func mirrorCatchUp(convID string) ([]string, error) {
	mirrorCatchUpMu.Lock()
	fn := mirrorCatchUpFn
	mirrorCatchUpMu.Unlock()
	if fn == nil || convID == "" {
		return nil, nil
	}
	return fn(convID)
}
