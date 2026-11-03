package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundBudgetIsWhatTheConversationReallyGets(t *testing.T) {
	old := ConvRoundCeiling
	t.Cleanup(func() { ConvRoundCeiling = old })
	ConvRoundCeiling = 20
	if got := RoundBudget("theorize_chunk_3"); got != 20 {
		t.Fatalf("theory chunk: MaxRounds(40) under a 20-round ceiling gets %d", got)
	}
	if got := RoundBudget("hw_probe"); got != 3 {
		t.Fatalf("hw_probe: %d", got)
	}
	ConvRoundCeiling = 100
	if got := RoundBudget("theorize_chunk_3"); got != 40 {
		t.Fatalf("raised ceiling: %d", got)
	}
	// the default role's own limit, read from its condition, not a number written here
	if got, want := RoundBudget("no_such_role_xyz"), roundLimitOf(TerminationForRole("no_such_role_xyz")); want > 0 && got != want {
		t.Fatalf("default role: %d, its condition says %d", got, want)
	}
}

func TestTheoryPromptStatesTheBudgetAndWhereSorryCountsComeFrom(t *testing.T) {
	oldC, oldT := ConvRoundCeiling, TurnToolBudget
	t.Cleanup(func() { ConvRoundCeiling, TurnToolBudget = oldC, oldT })
	ConvRoundCeiling, TurnToolBudget = 17, 33
	p := formatTheorizeInstruction()
	for _, want := range []string{"up to 17 rounds, and up to 33 tool calls in each round",
		"the kernel continues you in the next round", "a theorem that uses sorry is not proved yet: the proofs under " + ProofReferencePath()} {
		if !strings.Contains(p, want) {
			t.Errorf("missing %q", want)
		}
	}
	if !strings.HasPrefix(p, "BUDGET") || strings.Contains(p, "%!") {
		t.Errorf("budget first, no format damage:\n%s", p[:min(400, len(p))])
	}
}

func TestOutputsSorryNoteIsMeasuredOnTheMirroredFiles(t *testing.T) {
	old := outputsLeanFn
	t.Cleanup(func() { outputsLeanFn = old })

	dir := t.TempDir()
	write := func(name, src string) { os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644) }
	outputsLeanFn = func(string) []chunkFile { return readLeanFiles(dir) }

	if OutputsSorryNote() != "" {
		t.Fatal("no .lean files: no note")
	}
	write("A.lean", "theorem a : True := trivial\n")
	write("notes.md", "sorry\n") // not a .lean file
	if got := OutputsSorryNote(); got != "[measured on this machine] no sorry in the 1 .lean file(s) in "+TheoryOutputsDir+"\n" {
		t.Fatalf("clean: %q", got)
	}
	write("B.lean", "theorem b1 : True := by\n  sorry\ntheorem b2 : True := sorry -- still\n")
	got := OutputsSorryNote()
	for _, want := range []string{"2 declaration(s) in 1 of the 2 .lean file(s)", "  B.lean: b1, b2\n", ProofReferencePath()} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}

	// only theory conversations get it; a continued one by its registered role
	if WritebackNote("dev_unit_1", "") != "" {
		t.Fatal("a dev conversation got the theory note")
	}
	if WritebackNote("theorize_chunk_2", "") != got {
		t.Fatal("theory chunk")
	}
	conv := "5a5a5a5a-cont-0000"
	RegisterConv(0, conv, "o/r", "theorize_chunk_4", 0)
	if WritebackNote("", conv) != got {
		t.Fatal("a continued theory conversation (no fork role) did not get the note")
	}
}
