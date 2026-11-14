package kernel

// statement_changes.go — a theorem statement a theory conversation changed after verification
// is recorded with what changed in its shape, and the record goes into the chunk's theory.
//
// 11/04 03:11:55 (chunk 5) `sgn_neg (s : Int) : sgn (-s) = - sgn s` became
// `sgn_samples : sgn 5 = 1 ∧ sgn (-5) = -1 ∧ sgn 0 = 0` — a property for every s replaced by
// three instances — and at 03:13:08 `pooled_rows_fit` gained a hypothesis. Both were applied
// (the sandbox had them; df6de787 stopped blocking corrections) and logged, and nothing else:
// dev and review read the weaker statements as the specification. The change is not blocked
// here either — a correction is what verification is for — but it is measured and written
// next to the module, so whoever implements or reviews it knows which statements are not the
// original reading of the reference.

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// StatementChange is one measured change of a theorem statement.
type StatementChange struct {
	File, Old, New string
	Signals        []string // what changed in the statement's shape
}

var (
	stmtMu      sync.Mutex
	stmtChanges = map[string][]StatementChange{} // conv id -> changes, in order
	binderRe    = regexp.MustCompile(`[({\[][^(){}\[\]]*:[^(){}\[\]]*[)}\]]`)
)

// statementSignals measures how a statement changed: binders (the "for every x" part) lost,
// hypotheses added, or only the claim itself.
func statementSignals(old, new string) []string {
	ob, nb := len(binderRe.FindAllString(old, -1)), len(binderRe.FindAllString(new, -1))
	oq, nq := strings.Count(old, "∀"), strings.Count(new, "∀")
	var sig []string
	if ob+oq > 0 && nb+nq == 0 {
		sig = append(sig, "every variable of the statement is gone: a property stated for all values became a statement about fixed values")
	} else if nb+nq < ob+oq {
		sig = append(sig, fmt.Sprintf("fewer quantified variables (%d → %d)", ob+oq, nb+nq))
	}
	if nb > ob && ob+oq > 0 && nb+nq > 0 {
		sig = append(sig, fmt.Sprintf("more hypotheses/binders (%d → %d): the statement holds in fewer cases", ob, nb))
	}
	if len(sig) == 0 {
		sig = append(sig, "the claim changed")
	}
	return sig
}

// RecordStatementChange records a changed statement for conversation convID.
func RecordStatementChange(convID, file, old, new string) []string {
	sig := statementSignals(old, new)
	stmtMu.Lock()
	stmtChanges[convID] = append(stmtChanges[convID], StatementChange{File: file, Old: old, New: new, Signals: sig})
	stmtMu.Unlock()
	return sig
}

// StatementChangesFor returns the changes recorded for convID.
func StatementChangesFor(convID string) []StatementChange {
	stmtMu.Lock()
	defer stmtMu.Unlock()
	return append([]StatementChange(nil), stmtChanges[convID]...)
}

// renderStatementChanges is the block appended to a chunk's theory.
func renderStatementChanges(ch []StatementChange) string {
	if len(ch) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n/-! [measured by the kernel] STATEMENTS CHANGED AFTER VERIFICATION in this chunk.\n")
	b.WriteString("These are not the first reading of the reference: check each against the reference code before implementing or accepting it.\n")
	for _, c := range ch {
		b.WriteString(fmt.Sprintf("\n- %s\n  was: %s\n  now: %s\n  measured: %s\n", c.File, c.Old, c.New, strings.Join(c.Signals, "; ")))
	}
	b.WriteString("-/\n")
	return b.String()
}
