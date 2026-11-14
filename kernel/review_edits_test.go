package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What a review changes in the mirror turns its PASS into a FAIL that carries the change.
// 11/14 19:30 unit ub848524604's review fixed carve in MtpDraftChain.lean in the mirror and
// passed the unit; the branch kept "carve (base + bump n) ns".
func TestAReviewThatChangesTheUnitDoesNotPassIt(t *testing.T) {
	mirror := t.TempDir()
	merged := "def carve (base : Nat) : List Nat → List Nat\n  | [] => []\n  | n :: rest => base :: carve (base + bump n) ns\n"
	put := func(f, body string) {
		os.MkdirAll(filepath.Dir(filepath.Join(mirror, f)), 0o755)
		os.WriteFile(filepath.Join(mirror, f), []byte(body), 0o644)
	}
	put("lean/ub848/MtpDraftChain.lean", merged)
	put("src/core/session.cpp", "int x;\n")
	before, err := snapshotTree(mirror)
	if err != nil {
		t.Fatal(err)
	}
	shown := func(f string) string {
		if f == "lean/ub848/MtpDraftChain.lean" {
			return merged
		}
		return ""
	}
	if v := reviewEditVerdict("PASS", mirror, before, shown); v != "PASS" {
		t.Fatalf("nothing changed: %s", v)
	}
	// the review's sed at 19:30:44
	put("lean/ub848/MtpDraftChain.lean", strings.Replace(merged, "bump n) ns", "bump n) rest", 1))
	v := reviewEditVerdict("PASS", mirror, before, shown)
	if !strings.HasPrefix(v, "FAIL: the review changed the unit's files (lean/ub848/MtpDraftChain.lean)") ||
		!strings.Contains(v, "-  | n :: rest => base :: carve (base + bump n) ns") || !strings.Contains(v, "+  | n :: rest => base :: carve (base + bump n) rest") {
		t.Fatalf("a PASS after changing the unit: %s", v)
	}
	if v := reviewEditVerdict("FAIL: carve does not type-check", mirror, before, shown); !strings.HasPrefix(v, "FAIL: carve does not type-check\nthe review also changed") {
		t.Fatalf("a FAIL keeps its reason and carries the change: %s", v)
	}
}
