package kernel

// conv_tail.go — "is this conversation's last turn finished and persisted?",
// answered with a GET.
//
// Why this replaces ProbeConvBusy
// --------------------------------
// ProbeConvBusy POSTs a real completion ("continue", sonnet, effort low) and
// reads the HTTP status: wiggle_container_in_use means busy; a 200 means idle
// and the kernel then calls StopResponse. So every "probe" of an idle
// conversation appended a human message and a cancelled assistant message to it,
// spent quota, and — worse — could start a turn on a conversation whose previous
// turn was merely not persisted yet. Chunk 1 of the Strata run shows what an
// unpersisted turn plus a write does: five "Continue"s, each answered "I don't
// have any prior context", while the real 12-file reply was persisted later,
// after them (conversation export e39ca4ef).
//
// The tail uses the browser's own download form (consistency=eventual +
// return_dangling_human_message=true), which is the only one that exposes a
// human message that has no reply yet.

import (
	"encoding/json"
	"fmt"
	"sync"
)

// ConvTail is the state of the end of a conversation.
type ConvTail struct {
	Messages   int    // number of chat_messages
	LastSender string // "human" | "assistant" | ""
	LastStop   string // stop_reason of the last message when it is an assistant message
}

// Settled: the last message is an assistant message that has a stop_reason, i.e.
// the turn has finished and been persisted. A trailing human message (the
// "dangling" one) or an assistant message without stop_reason means a turn is
// still in flight or not yet visible.
func (t ConvTail) Settled() bool {
	return t.LastSender == "assistant" && t.LastStop != ""
}

// NeverStarted: nothing was ever stored. This is what an account/rate-limit
// failure at fork time looks like (0 messages) — there is nothing to wait for.
func (t ConvTail) NeverStarted() bool { return t.Messages == 0 }

// Working: something was sent and no finished reply is visible yet.
func (t ConvTail) Working() bool { return !t.NeverStarted() && !t.Settled() }

// ConvTailFromJSON reads the tail out of a conversation download.
func ConvTailFromJSON(raw []byte) (ConvTail, error) {
	var conv struct {
		ChatMessages []struct {
			Sender     string `json:"sender"`
			StopReason string `json:"stop_reason"`
		} `json:"chat_messages"`
	}
	if err := json.Unmarshal(raw, &conv); err != nil {
		return ConvTail{}, fmt.Errorf("parse conversation: %w", err)
	}
	t := ConvTail{Messages: len(conv.ChatMessages)}
	if n := len(conv.ChatMessages); n > 0 {
		last := conv.ChatMessages[n-1]
		t.LastSender = last.Sender
		if last.Sender == "assistant" {
			t.LastStop = last.StopReason
		}
	}
	return t, nil
}

var (
	convTailMu sync.Mutex
	convTailFn func(convID string) ([]byte, error)
)

// SetConvTailFn registers the raw, eventual+dangling conversation download.
func SetConvTailFn(fn func(convID string) ([]byte, error)) {
	convTailMu.Lock()
	convTailFn = fn
	convTailMu.Unlock()
}

// ConvSettled reads the tail of convID. err != nil means "could not tell" and
// must be treated as inconclusive — never as permission to send anything.
func ConvSettled(convID string) (settled bool, tail ConvTail, err error) {
	convTailMu.Lock()
	fn := convTailFn
	convTailMu.Unlock()
	if fn == nil {
		return false, ConvTail{}, fmt.Errorf("no conv tail function set")
	}
	raw, err := fn(convID)
	if err != nil {
		return false, ConvTail{}, err
	}
	tail, err = ConvTailFromJSON(raw)
	if err != nil {
		return false, ConvTail{}, err
	}
	return tail.Settled(), tail, nil
}

// remoteBusy is the read-only replacement for ProbeConvBusy, with the same shape
// so call sites keep their structure: busy=true means a turn is in flight or not
// yet visible; err != nil is inconclusive. A conversation with no messages is not
// busy — it never started.
func remoteBusy(convID string) (bool, error) {
	_, tail, err := ConvSettled(convID)
	if err != nil {
		return false, err
	}
	return tail.Working(), nil
}

// continueAllowed: only a settled conversation may be sent another message.
// Anything else (still generating, not yet persisted, cannot tell) must wait.
func continueAllowed(convID string) bool {
	settled, _, err := ConvSettled(convID)
	return err == nil && settled
}
