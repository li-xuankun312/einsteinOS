package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// one or zero: every reference definition needs a line, and the line's name must be
// defined in the file it names
func TestEveryReferenceDefinitionNeedsAVerifiedCounterpart(t *testing.T) {
	ws := t.TempDir()
	put := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(ws, p)), 0o755)
		os.WriteFile(filepath.Join(ws, p), []byte(s), 0o644)
	}
	syms := []RefSymbol{
		{File: "src/spec/controller.cpp", Kind: "func", Name: "Controller::choose"},
		{File: "src/spec/controller.cpp", Kind: "func", Name: "Controller::observe"},
		{File: "include/strata/kernels/ngram.hpp", Kind: "type", Name: "PleTable"},
	}
	put("src/spec/controller.cpp", "namespace baloon {\nint Controller::choose(int k) { return k; }\n}\n")
	put("include/baloon/ple.hpp", "struct PleTable { int x; };\n")
	if m := portMissing(ws, "u1", syms); len(m) != 3 {
		t.Fatalf("no port map: all three missing, got %v", m)
	}
	put(PortMapPath("u1"), "src/spec/controller.cpp\tController::choose\tsrc/spec/controller.cpp\tController::choose\n"+
		"src/spec/controller.cpp\tController::observe\tsrc/spec/controller.cpp\tobserve\n"+
		"include/strata/kernels/ngram.hpp\tPleTable\tinclude/baloon/ple.hpp\tPleTable\n")
	m := portMissing(ws, "u1", syms)
	if len(m) != 1 || !strings.Contains(m[0], "observe") || !strings.Contains(m[0], "not defined") {
		t.Fatalf("observe is mapped to a file that does not define it: %v", m)
	}
	put("src/spec/controller.cpp", "int Controller::choose(int k) { return k; }\nvoid Controller::observe(int) {}\n")
	if m := portMissing(ws, "u1", syms); len(m) != 0 {
		t.Fatalf("complete: %v", m)
	}

	kind, text := unitGates(GateInput{Req: UnitRequirements{Symbols: syms}, PortMissing: []string{"  x"}})
	if kind != "port_incomplete" || !strings.Contains(text, "1 of the 3") {
		t.Fatalf("%s %s", kind, text)
	}
	cl := UnitRequirements{Symbols: syms}.PortChecklist("Niko1221/Strata", "01529aaa82b9ffff", "u1")
	if !strings.Contains(cl, ".einsteinOS/port/u1.tsv") || !strings.Contains(cl, "src/spec/controller.cpp\tController::observe\tfunc") || !strings.Contains(cl, "3 definitions") {
		t.Fatalf("%s", cl)
	}
}
