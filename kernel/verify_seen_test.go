package kernel

import "testing"

func TestAConversationThatRanItsVerifyScriptsHereIsRecorded(t *testing.T) {
	if VerifySeen("c1") {
		t.Fatal("not yet")
	}
	NoteVerifySeen("c1")
	NoteVerifySeen("")
	if !VerifySeen("c1") || VerifySeen("") {
		t.Fatal("recorded by conversation")
	}
}

// A done line's verification on this machine, sent back to the conversation, is the unit's
// scripts seen — once; a verification that did not run is not.
func TestADoneLinesVerificationIsTheUnitsScriptsSeen(t *testing.T) {
	p := &DevPlan{}
	if notePreverifySeen(p, "uintegration01529aaa82b9", UnitVerifyOutcome{Verified: false}) || p.VerifySeen["uintegration01529aaa82b9"] {
		t.Fatal("a verification that did not run counted")
	}
	if !notePreverifySeen(p, "uintegration01529aaa82b9", UnitVerifyOutcome{Verified: true}) || !p.VerifySeen["uintegration01529aaa82b9"] {
		t.Fatal("a verification on this machine did not count")
	}
	if notePreverifySeen(p, "uintegration01529aaa82b9", UnitVerifyOutcome{Verified: true}) {
		t.Fatal("counted again: the plan would be stored at every done line")
	}
}
