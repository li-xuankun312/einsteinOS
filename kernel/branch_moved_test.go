package kernel

import (
	"os"
	"path/filepath"
	"testing"
)

// A unit's delta is not copied over a file another unit changed on the branch since the
// unit's mirror was primed: that would undo the other unit's commit.
func TestADeltaIsNotCopiedOverAFileTheBranchChangedMeanwhile(t *testing.T) {
	tip := t.TempDir()
	put := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(tip, p)), 0o755)
		os.WriteFile(filepath.Join(tip, p), []byte(s), 0o644)
	}
	put("include/kern_geometry.hpp", "v1")
	put("src/a.cpp", "a")
	base := fileDigest{}
	for _, p := range []string{"include/kern_geometry.hpp", "src/a.cpp"} {
		d, _ := digestFile(filepath.Join(tip, p))
		base[p] = d
	}
	// another unit's commit, after this unit's mirror was primed
	put("include/kern_geometry.hpp", "v2 by the other unit")
	put("src/new_by_other.cpp", "x")

	if got := branchMovedUnder(tip, base, []string{"src/a.cpp", "src/mine.cpp"}, nil); len(got) != 0 {
		t.Fatalf("files the branch did not change were taken for moved: %v", got)
	}
	got := branchMovedUnder(tip, base, []string{"include/kern_geometry.hpp", "src/new_by_other.cpp"}, nil)
	if len(got) != 2 {
		t.Fatalf("the files the other unit changed or created: %v", got)
	}
}
