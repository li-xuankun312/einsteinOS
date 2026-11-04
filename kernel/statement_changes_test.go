package kernel

import (
	"strings"
	"testing"
)

func TestAStatementChangeIsMeasuredAndWrittenIntoTheChunk(t *testing.T) {
	sig := statementSignals("theorem sgn_neg (s : Int) : sgn (-s) = - sgn s",
		"theorem sgn_samples : sgn 5 = 1 ∧ sgn (-5) = -1 ∧ sgn 0 = 0")
	if !strings.Contains(strings.Join(sig, ";"), "fixed values") {
		t.Fatalf("∀ s → three instances must be called out: %v", sig)
	}
	sig = statementSignals("theorem pooled_rows_fit (pos max_cells d : Nat) (hp : pos < max_cells) (hd : d < D) : x < y",
		"theorem pooled_rows_fit (pos max_cells d : Nat) (hp : pos < max_cells) (hd : d < D) (hs : slotOf pos = R - 1) : x < y")
	if !strings.Contains(strings.Join(sig, ";"), "more hypotheses") {
		t.Fatalf("an added hypothesis must be called out: %v", sig)
	}
	if sig := statementSignals("theorem a : 680 + 584 = 1264", "theorem a : 688 + 592 = 1280"); sig[0] != "the claim changed" {
		t.Fatalf("a corrected number is a changed claim: %v", sig)
	}
	RecordStatementChange("conv-x", "NativePlePostops.lean", "theorem sgn_neg (s : Int) : P s", "theorem sgn_samples : P 5")
	out := renderStatementChanges(StatementChangesFor("conv-x"))
	for _, want := range []string{"STATEMENTS CHANGED", "NativePlePostops.lean", "was: theorem sgn_neg", "fixed values"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
}
