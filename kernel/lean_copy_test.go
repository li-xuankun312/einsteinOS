package kernel

import "testing"

const leanCopyTheory = `## MODULE: lean/RouterTopKSelection.lean
-- the theory's text
theorem topk_le (n k : Nat) (h : k ≤ n) : min k n = k := by
  exact Nat.min_eq_left h

def RT_MAX_THREADS : Nat := 512
`

// 11/13 unit u7663fc74b8: lean/u766/*.lean were sed-cut from theory.md — every declaration
// is the theory's, whatever the comments and line breaks around it.
func TestALeanDeclarationCutFromTheTheoryIsACopy(t *testing.T) {
	lean := `-- RouterTopKSelection.lean, written by the unit (it says)
theorem topk_le (n k : Nat) (h : k ≤ n) :
    min k n = k := by
  exact Nat.min_eq_left h   -- same proof
`
	got := leanCopied(lean, leanCopyTheory)
	if len(got) != 1 || got[0].Name != "topk_le" || got[0].Kind != "theorem" {
		t.Fatalf("%+v", got)
	}
}

// A statement the unit proves its own way is not a copy, nor is a one-line definition that
// can only be written one way.
func TestTheUnitsOwnProofIsNotACopy(t *testing.T) {
	lean := `theorem topk_le (n k : Nat) (h : k ≤ n) : min k n = k := by
  omega

def RT_MAX_THREADS : Nat := 512
`
	if got := leanCopied(lean, leanCopyTheory); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}
