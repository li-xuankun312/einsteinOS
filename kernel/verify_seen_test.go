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
