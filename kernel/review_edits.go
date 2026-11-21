package kernel

// review_edits.go — a review judges the tree the kernel verified; it does not change it.
//
// The mirror is measured before the review and after it. Files the review changed mean its
// verdict was not of the verified tree: a PASS becomes a FAIL that carries what it changed (so
// the fix conversation, whose tree is verified, can make the change), and a FAIL carries it
// too. 11/14 08:32–09:20 unit u41a85163f4's review rewrote NgramPleHash.lean and
// PerLayerEmbeddingBlock.lean, changed a theorem's statement in NativePlePostops.lean, and
// passed the unit; none of it reached the branch. 19:28–19:31 unit ub848524604's review
// fixed carve, runProd and pick in the same way.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	reviewChangedPassFmt = "FAIL: the review changed the unit's files (%[1]s), so its PASS was not of the tree the kernel verified; what it changed:\n%[2]s"
	reviewChangedFailFmt = "%[1]s\nthe review also changed the unit's files (%[2]s):\n%[3]s"
	reviewEditDiffMax    = 4000
)

// reviewEditVerdict: verdict, given what the review changed in the mirror since before (its
// manifest when the review started). shown gives a file's text on the verified branch.
func reviewEditVerdict(verdict, mirror string, before fileDigest, shown func(file string) string) string {
	changed, removed, err := treeDelta(mirror, before)
	if err != nil || len(changed)+len(removed) == 0 {
		return verdict
	}
	var names []string
	var diff strings.Builder
	for _, f := range changed {
		names = append(names, f)
		now, _ := os.ReadFile(filepath.Join(mirror, filepath.FromSlash(f)))
		diff.WriteString(lineDiff(f, shown(f), string(now)))
	}
	for _, f := range removed {
		names = append(names, f+" (removed)")
	}
	d := truncDiff(diff.String(), reviewEditDiffMax)
	if strings.HasPrefix(verdict, "PASS") {
		return fmt.Sprintf(reviewChangedPassFmt, strings.Join(names, ", "), d)
	}
	return fmt.Sprintf(reviewChangedFailFmt, verdict, strings.Join(names, ", "), d)
}

// lineDiff: the lines of was and now that differ, as "-"/"+" lines under the file's name —
// the lines in order, without context; enough for a conversation to repeat the change.
func lineDiff(file, was, now string) string {
	a, b := strings.Split(was, "\n"), strings.Split(now, "\n")
	inB := map[string]int{}
	for _, l := range b {
		inB[l]++
	}
	inA := map[string]int{}
	for _, l := range a {
		inA[l]++
	}
	var out strings.Builder
	out.WriteString("--- " + file + "\n")
	for _, l := range a {
		if inB[l] > 0 {
			inB[l]--
			continue
		}
		out.WriteString("-" + l + "\n")
	}
	for _, l := range b {
		if inA[l] > 0 {
			inA[l]--
			continue
		}
		out.WriteString("+" + l + "\n")
	}
	return out.String()
}
