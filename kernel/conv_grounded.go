package kernel

// conv_grounded.go — the part of a conversation's text written after this machine's
// results had reached it.
//
// A tool call runs twice: first in the conversation's own sandbox, whose output the
// model sees at once, then here, where the kernel runs it and writes the result back
// in the NEXT message. Text in the same turn as a tool call was therefore written from
// the sandbox's output, never this machine's. The hardware probe showed it: its report
// ("lspci listed no GPU …") came in the turn of its one command, from the sandbox.
//
// GroundedRound is the rule; the model layer reports each round's text through
// NoteRoundText, and a reader asks ConvGrounded.

import (
	"strings"
	"sync"
)

// GroundedRound: a round's text describes this machine only if the round made no tool
// call of its own (its text cannot rest on that call's sandbox output) and results from
// this machine were already written back to the conversation earlier.
func GroundedRound(roundTools int, localResultsDelivered bool) bool {
	return roundTools == 0 && localResultsDelivered
}

var (
	convGroundedMu sync.Mutex
	convGrounded   = map[string]*strings.Builder{}
)

// NoteRoundText records one round's text if GroundedRound holds for it.
func NoteRoundText(convID, text string, roundTools int, localResultsDelivered bool) {
	if convID == "" || strings.TrimSpace(text) == "" || !GroundedRound(roundTools, localResultsDelivered) {
		return
	}
	convGroundedMu.Lock()
	defer convGroundedMu.Unlock()
	b := convGrounded[convID]
	if b == nil {
		b = &strings.Builder{}
		convGrounded[convID] = b
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString(strings.TrimSpace(StripDoneMarker(text)))
}

// ConvGrounded returns the grounded text of a conversation ("" when there is none).
func ConvGrounded(convID string) string {
	convGroundedMu.Lock()
	defer convGroundedMu.Unlock()
	if b := convGrounded[convID]; b != nil {
		return b.String()
	}
	return ""
}
