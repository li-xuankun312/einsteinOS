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

	mirror := t.TempDir()
	os.MkdirAll(filepath.Join(mirror, "include"), 0o755)
	os.WriteFile(filepath.Join(mirror, "include", "kern_geometry.hpp"), []byte("v1 edited here"), 0o644)
	if got := branchMovedUnder(tip, mirror, base, []string{"src/a.cpp", "src/mine.cpp"}, nil); len(got) != 0 {
		t.Fatalf("files the branch did not change were taken for moved: %v", got)
	}
	got := branchMovedUnder(tip, mirror, base, []string{"include/kern_geometry.hpp", "src/new_by_other.cpp"}, nil)
	if len(got) != 2 {
		t.Fatalf("the files the other unit changed or created: %v", got)
	}
}

// 11/23 08:44:05 the integration's conversations had pushed their own mirror's files to the wip
// branch, and the verification was refused over them: a tip that holds what the mirror holds is
// not another's change
func TestATipThatHoldsTheMirrorsFilesIsNotMovedUnderIt(t *testing.T) {
	tip, mirror := t.TempDir(), t.TempDir()
	put := func(dir, p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644)
	}
	put(tip, "src/rt/embed_head.cpp", "v1")
	base := fileDigest{}
	d, _ := digestFile(filepath.Join(tip, "src/rt/embed_head.cpp"))
	base["src/rt/embed_head.cpp"] = d
	// the conversation's edit, in its mirror and pushed by it to the branch
	put(mirror, "src/rt/embed_head.cpp", "v2 NativeEmbedBf16")
	put(tip, "src/rt/embed_head.cpp", "v2 NativeEmbedBf16")
	put(mirror, "tests/k39_integration/embed_parity.cu", "new")
	put(tip, "tests/k39_integration/embed_parity.cu", "new")
	if got := branchMovedUnder(tip, mirror, base, []string{"src/rt/embed_head.cpp", "tests/k39_integration/embed_parity.cu"}, nil); len(got) != 0 {
		t.Fatalf("the mirror's own files on the branch were taken for another's: %v", got)
	}
	// a part conversation pushed a newer version than this mirror holds: that is another's change
	put(tip, "src/rt/embed_head.cpp", "v3 by a part")
	if got := branchMovedUnder(tip, mirror, base, []string{"src/rt/embed_head.cpp"}, nil); len(got) != 1 {
		t.Fatalf("a version the mirror does not hold was not taken for another's: %v", got)
	}
}
