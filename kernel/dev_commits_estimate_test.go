package kernel

import (
	"os"
	"path/filepath"
	"testing"
)

// 11/18 uf7cdc0dcca: 196 definitions, 162 of them mapped on the branch; the conversation was
// told 196 and padded 34 lines of work out with comment-only commits. The estimate is what is
// still missing, measured as the port gate measures it.
func TestTheShellsEstimateIsWhatTheUnitStillLacks(t *testing.T) {
	ws := t.TempDir()
	put := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(ws, p)), 0o755)
		os.WriteFile(filepath.Join(ws, p), []byte(s), 0o644)
	}
	syms := []RefSymbol{
		{File: "src/core/mtp.cpp", Kind: "type", Name: "Bump"},
		{File: "src/core/mtp.cpp", Kind: "alias", Name: "Clock"},
		{File: "src/core/weights.cpp", Kind: "const", Name: "CHUNK"},
	}
	put("src/core/mtp.cpp", "struct Bump { int x; };\nusing Clock = int;\n")
	put("src/core/weights.cpp", "constexpr int CHUNK = 8;\n")
	if n := devCommitsEstimate(ws, "uf7cdc0dcca", syms, 4); n != 3 {
		t.Fatalf("first attempt, no port map: %d", n)
	}
	put(PortMapPath("uf7cdc0dcca"), "src/core/mtp.cpp\tBump\tsrc/core/mtp.cpp\tBump\n")
	if n := devCommitsEstimate(ws, "uf7cdc0dcca", syms, 4); n != 2 {
		t.Fatalf("one of three on the branch: %d", n)
	}
	put(PortMapPath("uf7cdc0dcca"), "src/core/mtp.cpp\tBump\tsrc/core/mtp.cpp\tBump\nsrc/core/mtp.cpp\tClock\tsrc/core/mtp.cpp\tClock\nsrc/core/weights.cpp\tCHUNK\tsrc/core/weights.cpp\tCHUNK\n")
	if n := devCommitsEstimate(ws, "uf7cdc0dcca", syms, 4); n != 4 {
		t.Fatalf("all mapped (failed on something else): the modules, got %d", n)
	}
	if n := devCommitsEstimate(ws, "u0", nil, 0); n != 1 {
		t.Fatalf("nothing measured: %d", n)
	}
}
