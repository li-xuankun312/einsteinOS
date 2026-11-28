package kernel

import (
	"strings"
	"testing"
)

// 11/28 the part embed added theorem gatherPerRow2048 : 2048 / 8 = 256 := by decide: a theorem the
// attempt adds whose statement is numerals alone is listed; one the branch had, one with binders
// and one naming a definition are not
func TestLeanTheoremsStatingNumeralsAloneAreListed(t *testing.T) {
	tip, mirror := t.TempDir(), t.TempDir()
	f := "lean/Baloon/PrefillKernels.lean"
	putTree(t, tip, map[string]string{f: "theorem gatherPerRow : 2560 / 8 = 320 := by decide\n"})
	putTree(t, mirror, map[string]string{f: "theorem gatherPerRow : 2560 / 8 = 320 := by decide\n" +
		"/-- geometry -/\ntheorem gatherPerRow2048 : 2048 / 8 = 256 := by decide\n" +
		"theorem rowsFit (n : Nat) : n / 8 ≤ n := by omega\n" +
		"theorem widthOk : gatherWidth 2048 = 256 := by rfl\n"})
	got := leanConstantTheorems(tip, mirror, []string{f, "src/x.cu"})
	if !strings.Contains(got, "lean/Baloon/PrefillKernels.lean gatherPerRow2048 : 2048 / 8 = 256\n") ||
		strings.Count(got, "\n") != 3 {
		t.Fatalf("%q", got)
	}
	if leanConstantTheorems(tip, tip, []string{f}) != "" {
		t.Fatal("a theorem the branch had was listed")
	}
}
