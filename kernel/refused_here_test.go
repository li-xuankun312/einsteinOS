package kernel

import "testing"

func TestTheCommandsRefusedHereAreSummedAtTheConversationsEnd(t *testing.T) {
	for _, w := range []string{".git-credentials", ".ssh", ".git-credentials", "github_token"} {
		NoteRefusedHere("c421eabe", w)
	}
	NoteRefusedHere("", ".ssh")
	if got := RefusedHereSummary("c421eabe"); got != "; 4 command(s) not run here for touching the kernel's credentials or directories: .git-credentials ×2, .ssh ×1, github_token ×1" {
		t.Fatalf("%q", got)
	}
	if got := RefusedHereSummary("c421eabe"); got != "" {
		t.Fatalf("taken twice: %q", got)
	}
}
