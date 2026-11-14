package kernel

import (
	"strings"
	"testing"
	"time"
)

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

// The gate: a unit whose Lean is the theory's text does not pass, whatever its scripts say.
func TestAUnitWhoseLeanIsTheTheorysDoesNotPass(t *testing.T) {
	realRunner(t)
	ws := t.TempDir()
	build, test := devVerifyScripts("u7663fc74b8")
	write(t, ws, build, "true")
	write(t, ws, test, "true")
	write(t, ws, theoryPath, leanCopyTheory)
	write(t, ws, "lean/u766/RouterTopKSelection.lean", "theorem topk_le (n k : Nat) (h : k ≤ n) : min k n = k := by\n  exact Nat.min_eq_left h\n")
	r := runUnitVerifyWith(ws, "u7663fc74b8", "", UnitRequirements{}, []string{"lean/u766/RouterTopKSelection.lean"}, time.Minute, time.Minute)
	if r.Passed() || r.Gate != "lean_copied" || !strings.Contains(r.GateText, "lean/u766/RouterTopKSelection.lean: theorem topk_le") {
		t.Fatalf("gate %q %q", r.Gate, r.GateText)
	}
	write(t, ws, "lean/u766/RouterTopKSelection.lean", "theorem topk_le (n k : Nat) (h : k ≤ n) : min k n = k := by\n  omega\n")
	if r := runUnitVerifyWith(ws, "u7663fc74b8", "", UnitRequirements{}, []string{"lean/u766/RouterTopKSelection.lean"}, time.Minute, time.Minute); !r.Passed() {
		t.Fatalf("own proof: gate %q %q", r.Gate, r.GateText)
	}
}
