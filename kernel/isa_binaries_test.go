package kernel

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The unit's own objects are always disassembled, however many other binaries its build
// produced before them; the others fill the cap.
func TestTheUnitsOwnObjectsAreAlwaysDisassembled(t *testing.T) {
	dir := t.TempDir()
	var cmds []string
	for i := 0; i < isaObjdumpMax+6; i++ {
		o := filepath.Join(dir, fmt.Sprintf("lib%02d.o", i))
		os.WriteFile(o, []byte("x"), 0o644)
		cmds = append(cmds, fmt.Sprintf("/usr/bin/g++ -O2 -c src/other/f%02d.cpp -o %s", i, o))
	}
	own := filepath.Join(dir, "expert_q2.o")
	os.WriteFile(own, []byte("x"), 0o644)
	cmds = append(cmds, "/usr/bin/g++ -O2 -mavx512f -c src/cpu/expert_q2.cpp -o "+own)

	bins := isaBinaries(ProcTrace{Cmdlines: cmds}, ProcTrace{}, []string{"src/cpu/expert_q2.cpp", "tests/k63_ube4/test_ube4.cu"})
	if len(bins) != isaObjdumpMax || bins[0] != own {
		t.Fatalf("%d binaries, first %q", len(bins), bins[0])
	}
	if old := isaBinaries(ProcTrace{Cmdlines: cmds}, ProcTrace{}, nil); len(old) != isaObjdumpMax || old[0] == own {
		t.Fatalf("without the changed files the cap is filled in build order: %d", len(old))
	}
}
