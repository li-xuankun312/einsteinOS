package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// At the start of a run a passed unit whose verify scripts on the branch still read its port
// map (written before 1ccc5551 made that port_count_in_script) is reopened, and a unit still
// to do whose next attempt starts from the branch is told before it starts. 11/19 16:44:02,
// 16:47:04 and 16:51:11 p42de699ebc, p71c0367d2e and p17652417b0 were each sent back for
// port_count_in_script after a 3–4 minute verification, their scripts carrying the port check
// they had before; 16:48:05 p71c0367d2e's conversation read p42de699ebc's test.sh to write its own.
func TestScriptsOnTheBranchThatReadAPortMapAreFoundAtTheStart(t *testing.T) {
	tree := t.TempDir()
	write := func(rel, text string) {
		p := filepath.Join(tree, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(text), 0o644)
	}
	portBlock := "python3 - .einsteinOS/port/%s.tsv <<'PY'\nimport sys\nn = sum(1 for l in open(sys.argv[1]) if l.strip())\nsys.exit(0 if n == 9 else 1)\nPY\n"
	write(".einsteinOS/verify/u89be81de0b/test.sh", "#!/bin/bash\n"+strings.ReplaceAll(portBlock, "%s", "u89be81de0b"))
	write(".einsteinOS/verify/ub25e8eba7f/test.sh", "#!/bin/bash\n./build/test_generate\n")
	write(".einsteinOS/verify/p71c0367d2e/test.sh", "#!/bin/bash\n"+strings.ReplaceAll(portBlock, "%s", "p71c0367d2e"))
	write(".einsteinOS/verify/p42de699ebc/test.sh", "#!/bin/bash\n"+strings.ReplaceAll(portBlock, "%s", "p42de699ebc"))

	plan := &DevPlan{PortPass: map[string]bool{"u89be81de0b": true, "ub25e8eba7f": true},
		PortUnits: []DevPlanNode{{ID: "u89be81de0b", Sources: []string{"include/strata/kernels/ngram.hpp"}}, {ID: "ub25e8eba7f", Sources: []string{"src/program/generate.cpp"}},
			{ID: "p71c0367d2e", Sources: []string{"include/strata/plan/plan.hpp"}}, {ID: "p42de699ebc", Sources: []string{"src/prefill/gemm.cu"}}},
		LastFailure: map[string]DevFailure{
			"p71c0367d2e": {Kind: "existing_code_fails", Text: "smoke_fail: …", BaseRef: "einsteinOS/dev-c880662efd"},
			"p42de699ebc": {Kind: "compile_fail", Text: "nvcc: …", BaseRef: "einsteinOS/wip-p42de699ebc"},
		}}
	shown := func(n DevPlanNode) UnitRequirements {
		return UnitRequirements{Symbols: []RefSymbol{{File: "include/ref/x.hpp", Kind: "func", Name: "f_" + n.ID}}}
	}
	reopened, told := portScriptRecheck(plan, tree, "einsteinOS/dev-c880662efd", shown)
	if strings.Join(reopened, " ") != "u89be81de0b" {
		t.Fatalf("reopened %v, want u89be81de0b", reopened)
	}
	if f := plan.Reopen["u89be81de0b"]; f.Kind != "port_count_in_script" || !strings.Contains(f.Text, ".einsteinOS/verify/u89be81de0b/test.sh") {
		t.Fatalf("reopen: %+v", f)
	}
	if strings.Join(told, " ") != "p71c0367d2e" {
		t.Fatalf("told %v, want p71c0367d2e (p42de699ebc continues from its own wip branch, not the branch measured)", told)
	}
	f := plan.LastFailure["p71c0367d2e"]
	if f.Kind != "existing_code_fails" || !strings.Contains(f.Text, "smoke_fail") || !strings.Contains(f.Text, "port_count_in_script") {
		t.Fatalf("the unit's own failure is to be kept and the measurement added: %+v", f)
	}
	if _, again := portScriptRecheck(plan, tree, "einsteinOS/dev-c880662efd", shown); len(again) != 0 {
		t.Fatalf("told twice: %v", again)
	}
}
