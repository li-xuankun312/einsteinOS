package kernel

import (
	"reflect"
	"strings"
	"testing"
)

func leanFile(name, src string) chunkFile {
	return chunkFile{RemotePath: TheoryOutputsDir + "/" + name, Name: name, Content: []byte(src)}
}

func TestSorrySitesAreMeasuredPerDeclaration(t *testing.T) {
	files := []chunkFile{
		leanFile("Clean.lean", `/-! # MODULE: clean — no sorry here, says this comment -/
-- sorry in a line comment does not count
theorem sorryFree_ok : True := trivial
def sorryless := 1
`),
		leanFile("Two.lean", `namespace X
theorem a_le_b (a b : Nat) (h : a < b) : a ≤ b := by
  sorry
@[simp] lemma helper : 1 = 1 := rfl
private theorem p_two : 2 = 2 := sorry
noncomputable def table : Nat := by
  exact sorry
example : True := by sorry
end X
`),
	}
	got := sorrySites(files)
	want := []sorrySite{{File: "Two.lean", Decls: []string{"a_le_b", "p_two", "table", "example"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if sorryCount(got) != 4 {
		t.Fatal("count")
	}
}

func TestChunkSorryPromptIsFactsAndTheReference(t *testing.T) {
	oldDir, oldSub := proofRefDir, proofRefSub
	t.Cleanup(func() { proofRefDir, proofRefSub = oldDir, oldSub })
	proofRefDir, proofRefSub = "/tmp/ref_x/", "/proofs/"
	p := chunkSorryPrompt([]sorrySite{{File: "A.lean", Decls: []string{"t1", "t2"}}, {File: "B.lean", Decls: []string{"t3"}}})
	for _, want := range []string{"Continue. 3 declaration(s) in 2 file(s) still use sorry:", "  A.lean: t1, t2\n", "  B.lean: t3\n", "/tmp/ref_x/proofs/"} {
		if !strings.Contains(p, want) {
			t.Errorf("missing %q in\n%s", want, p)
		}
	}
	if strings.Contains(p, "%!") || strings.Contains(p, "openai") {
		t.Errorf("format damage or a literal instead of the variable:\n%s", p)
	}
}

// the loop, with the conversation and its files stubbed
func TestChunkFillSorryContinuesWhileItHelps(t *testing.T) {
	oldS, oldC, oldF, oldG, oldR := sorrySettledFn, sorryContinueFn, sorryFilesFn, sorryForgetFn, chunkSorryRounds
	t.Cleanup(func() {
		sorrySettledFn, sorryContinueFn, sorryFilesFn, sorryForgetFn, chunkSorryRounds = oldS, oldC, oldF, oldG, oldR
	})
	two := []chunkFile{leanFile("A.lean", "theorem x : True := sorry\ntheorem y : True := sorry\n")}
	one := []chunkFile{leanFile("A.lean", "theorem x : True := trivial\ntheorem y : True := sorry\n")}
	none := []chunkFile{leanFile("A.lean", "theorem x : True := trivial\ntheorem y : True := trivial\n")}

	run := func(seq [][]chunkFile, settled bool, rounds int) (prompts []string, left []sorrySite) {
		i := 0
		sorrySettledFn = func(string) bool { return settled }
		sorryContinueFn = func(_, p string) { prompts = append(prompts, p); i++ }
		sorryFilesFn = func(string) []chunkFile { return seq[min(i, len(seq)-1)] }
		sorryForgetFn = func(string) {}
		chunkSorryRounds = rounds
		return prompts, chunk_fill_sorry("conv", "chunk 1/1")
	}
	// fixed in two rounds
	p, left := run([][]chunkFile{two, one, none}, true, 3)
	if len(p) != 2 || len(left) != 0 || !strings.Contains(p[0], "A.lean: x, y") || !strings.Contains(p[1], "A.lean: y") {
		t.Fatalf("prompts %q left %+v", p, left)
	}
	// a round that removes nothing ends it
	p, left = run([][]chunkFile{one, one}, true, 3)
	if len(p) != 1 || sorryCount(left) != 1 {
		t.Fatalf("no progress: prompts %d left %+v", len(p), left)
	}
	// bounded
	p, _ = run([][]chunkFile{two, one, none}, true, 1)
	if len(p) != 1 {
		t.Fatalf("bound: %d", len(p))
	}
	// not settled: measured, not continued
	p, left = run([][]chunkFile{two}, false, 3)
	if len(p) != 0 || sorryCount(left) != 2 {
		t.Fatalf("unsettled: %d %+v", len(p), left)
	}
	// clean or nothing downloaded: no continue
	if p, left = run([][]chunkFile{none}, true, 3); len(p) != 0 || left != nil {
		t.Fatalf("clean: %d %+v", len(p), left)
	}
	if p, left = run([][]chunkFile{nil}, true, 3); len(p) != 0 || left != nil {
		t.Fatalf("no files: %d %+v", len(p), left)
	}
}

func TestTheoryPromptTakesTheReferenceAsArguments(t *testing.T) {
	oldU, oldD, oldS := proofRefURL, proofRefDir, proofRefSub
	t.Cleanup(func() { proofRefURL, proofRefDir, proofRefSub = oldU, oldD, oldS })
	proofRefURL, proofRefDir, proofRefSub = "https://example.org/r.git", "/tmp/r", "lean/P"
	p := formatTheorizeInstruction()
	for _, want := range []string{"git clone https://example.org/r.git /tmp/r", "files under /tmp/r/lean/P/"} {
		if !strings.Contains(p, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{"write sorry", "openai", "%!"} {
		if strings.Contains(p, bad) {
			t.Errorf("contains %q", bad)
		}
	}
}
