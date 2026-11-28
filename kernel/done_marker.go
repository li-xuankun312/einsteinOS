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

// DevDoneInToolRound tells a dev unit it can end on its last check: a done marker in the
// message that runs the final build/test ends the conversation there when every command
// exits 0 on the real machine (model/claudeweb doneInToolRound); otherwise the results come
// back and the conversation goes on.
const DevDoneInToolRound = "you may write that line in the same message that runs your final build and test: if every command in it exits 0 on the real machine, the conversation ends there; if any fails, you get the results and continue.\n"

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
