package kernel

// done_marker.go — a conversation says "I am finished" in a way the kernel can
// read without knowing what the task was.
//
// Before this, the only way out of a conversation that had stopped calling tools
// was ConsecutiveTextRounds{4}: three or four more calls, each answered with a
// summary of work that was already done, each waiting its turn in the 9-per-10-
// minutes window (Strata run: 3-4 such rounds at the end of EVERY theory chunk).
// The theory prompt even told the child "write a brief summary and stop", and the
// kernel did not believe it.
//
// The contract now: the prompt (formatted with DoneInstruction) asks the
// conversation to end its final message with DoneMarker alone on the last line.
// A text-only round that ends with the marker, in a conversation the server
// reports as settled (the turn finished and was persisted — never while a
// response may still be generating), ends the conversation at once. Without the
// marker nothing changes: the old counting still applies. The marker means
// nothing about the task; any role can use it.

import (
	"fmt"
	"strings"
)

// DoneMarker is the line a conversation ends with when it is completely finished.
const DoneMarker = "EINSTEINOS_DONE"

// DoneInstruction is the sentence prompts use to ask for the marker; it is
// formatted from DoneMarker so the two cannot drift apart.
func DoneInstruction() string {
	return fmt.Sprintf("when you are completely finished, end your final message with this line alone: %s\n"+
		"(only then: do not write it while anything is still unfinished.)\n", DoneMarker)
}

// lastLine returns the last non-empty line of s, trimmed of markdown noise.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, " \t\r\n"), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.Trim(strings.TrimSpace(lines[len(lines)-1]), "*`_ \t")
}

// HasDoneMarker reports whether text ends with the marker line.
func HasDoneMarker(text string) bool { return lastLine(text) == DoneMarker }

// StripDoneMarker removes the marker line, so a result that is parsed as the
// conversation's answer (a verdict, a plan) never carries it.
func StripDoneMarker(text string) string {
	if !HasDoneMarker(text) {
		return text
	}
	t := strings.TrimRight(text, " \t\r\n")
	i := strings.LastIndex(t, "\n")
	if i < 0 {
		return ""
	}
	return strings.TrimRight(t[:i], " \t\r\n")
}

// DoneMarkerCond fires on a text-only round whose text ends with DoneMarker and
// whose conversation is settled.
type DoneMarkerCond struct{}

func (c *DoneMarkerCond) Check(e ToolEvent) *StopSignal {
	if e.HasTools || !e.Settled || !HasDoneMarker(e.Text) {
		return nil
	}
	return &StopSignal{Reason: "done_marker"}
}
func (c *DoneMarkerCond) Reset()         {}
func (c *DoneMarkerCond) String() string { return "DoneMarker" }

// WithDoneMarker lets any condition also end on the marker. The base condition
// still decides everything the marker does not.
func WithDoneMarker(base TerminationCondition) TerminationCondition {
	if base == nil {
		return &DoneMarkerCond{}
	}
	return OrConditions(&DoneMarkerCond{}, base)
}

// ---- NoProgress: a conversation whose job is to produce files, and has produced none ----

// NoProgress stops a conversation after Rounds rounds in which nothing has appeared,
// where "appeared" is whatever the caller's Produced says (for a dev unit: a file in the
// local mirror that differs from the branch).
//
// Why: the account allows 40 requests per 5 hours (the 2026/11/02 18:53 message: 已使用40,
// 请2026-11-03 05:08:28后重试, a window that opened at 16:08), and every round of a
// conversation is a request. In that run the dev conversations that wrote nothing ran 12, 17,
// 20 and 22+ rounds each — a full window spent on rounds that produced nothing, then 2h15m
// waiting for it to reopen. The other limits (ConsecutiveTextRounds, MaxRounds) do not stop
// a conversation that alternates a tool call with a paragraph of text forever.
type NoProgress struct {
	Rounds   int
	Produced func() bool

	lastRound int
	lastValue bool
}

func (c *NoProgress) Check(e ToolEvent) *StopSignal {
	if c.Produced == nil || c.Rounds <= 0 || e.Round < c.Rounds {
		return nil
	}
	if e.Round != c.lastRound { // Check runs per tool event: look once per round
		c.lastRound, c.lastValue = e.Round, c.Produced()
	}
	if c.lastValue {
		return nil
	}
	return &StopSignal{Reason: fmt.Sprintf("no_progress=%d_rounds", c.Rounds)}
}
func (c *NoProgress) Reset()         { c.lastRound, c.lastValue = 0, false }
func (c *NoProgress) String() string { return fmt.Sprintf("NoProgress(%d)", c.Rounds) }
