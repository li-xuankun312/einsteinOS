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

func TestTheBudgetSentenceIsComposedFromWhatThePromptHolds(t *testing.T) {
	oldC, oldT := ConvRoundCeiling, TurnToolBudget
	t.Cleanup(func() { ConvRoundCeiling, TurnToolBudget = oldC, oldT })
	ConvRoundCeiling, TurnToolBudget = 20, 40

	// a theory chunk: what it embeds, by extension, most first
	var tl sourceTally
	for _, f := range []struct {
		p string
		n int
	}{{"src/a.cu", 100}, {"src/b.cpp", 50}, {"src/c.cu", 30}, {"include/d.hpp", 20}, {"Makefile", 5}} {
		tl.Add(f.p, f.n)
	}
	got := BudgetBlock(tl.Theory(), RoundBudget("theorize_chunk_3"))
	want := "the information is complete. this time you read a lot of source code: .cu ×2, .cpp ×1, .hpp ×1, Makefile ×1 (5 file(s), 205 lines) " +
		"and write its .lean theory, one file per module. start.\n" +
		"using up the tool calls is fine: up to 40 in one round, and the kernel resets you for the next one (up to 20 rounds).\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}

	// a dev unit: its modules' theory, its own condition's rounds, at the very end of the prompt
	r := &devRun{pid: 1, repo: "o/r", devRepo: "o/d", branch: "b", task: "t", gpuAll: "0"}
	plan := newDevPlan("o/r", "s", []DevModule{{Name: "A.lean", Hash: "h", Bytes: 3000}, {Name: "B.lean", Hash: "g", Bytes: 2200}},
		[][]string{{"A.lean", "B.lean"}}, "p")
	r.plan = plan
	p := r.unitPrompt(plan.Leaves()[0], 0, 1, 1, DevFailure{}, "b")
	tail := "the information is complete. this time you read a lot of theory: LAYER 1/2 of 2 module(s) (6 KB of theory) " +
		"and write the code for them, with the build and test scripts. start.\n" +
		"using up the tool calls is fine: up to 40 in one round, and the kernel resets you for the next one (up to 20 rounds).\n"
	// the budget is inserted; the prompt ends as the shell does, with the user's own sentence
	if !strings.Contains(p, tail) || !strings.HasSuffix(p, "。工具调用次数满了就跟我说，我会帮你重置，一直满就一直重置") {
		t.Fatalf("dev prompt does not hold its budget inside the shell:\n%s", p[max(0, len(p)-800):])
	}
	if RoundBudgetOf(devUnitCondition()) != 20 {
		t.Fatal("dev: MaxRounds(30) under a 20-round ceiling")
	}
	// the theory instruction itself states where sorry counts come from
	if !strings.Contains(formatTheorizeInstruction(), "a theorem that uses sorry is not proved yet") || !strings.Contains(formatTheorizeInstruction(), "the files under "+ProofReferencePath()) {
		t.Fatal("sorry line")
	}
	for _, s := range []string{got, p} {
		if strings.Contains(s, "%!") {
			t.Fatal("format damage")
		}
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
	if got := OutputsSorryNote(); got != "[measured on this machine] no sorry in the 1 .lean file(s) in "+TheoryOutputsDir+"\nfiles there now: A.lean (28 bytes)\n" {
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

// 11/04: a round spent on `ls /mnt/user-data/outputs` after each writeback. The note names
// the files, so the listing is already there.
func TestTheWritebackNoteNamesTheFilesNotOnlyTheirCount(t *testing.T) {
	old := outputsLeanFn
	t.Cleanup(func() { outputsLeanFn = old })
	outputsLeanFn = func(string) []chunkFile {
		return []chunkFile{{Name: "GgufReader.lean", Content: []byte("theorem a : True := trivial")},
			{Name: "MtpDrafter.lean", Content: []byte("theorem b : True := trivial")}}
	}
	n := OutputsSorryNote()
	if !strings.Contains(n, "GgufReader.lean (") || !strings.Contains(n, "MtpDrafter.lean (") {
		t.Fatalf("%s", n)
	}
}
