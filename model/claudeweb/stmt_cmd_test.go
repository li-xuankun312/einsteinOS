package claudeweb

import (
	"os"
	"path/filepath"
	"testing"
)

// a statement changed by a python edit is measured like one changed by str_replace
func TestAStatementChangedByACommandIsMeasured(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "NativePlePostops.lean")
	os.WriteFile(f, []byte("theorem sgn_neg (s : Int) : sgn (-s) = - sgn s := by\n  simp\ntheorem keep : True := trivial\n"), 0o644)
	before := leanStatements(dir)
	os.WriteFile(f, []byte("theorem sgn_samples : sgn 5 = 1 := by\n  decide\ntheorem keep : True := trivial\n"), 0o644)
	d := statementDiffs(before, leanStatements(dir))
	if len(d) != 1 || d[0][0] != "NativePlePostops.lean" || d[0][2] != "theorem sgn_samples : sgn 5 = 1" {
		t.Fatalf("%v", d)
	}
}
