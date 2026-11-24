package kernel

import (
	"strings"
	"testing"
)

// 11/23 20:43:10 and 11/24 00:56:46 build.sh was 0 bytes: it read itself and wrote nothing, and
// compile=pass went with the executables an earlier verification had left — such a build is
// not a compile; one that read a source or wrote a file is
func TestABuildThatBuiltNothingIsNotACompile(t *testing.T) {
	build := ".einsteinOS/verify/uint/build.sh"
	if why := builtNothing(buildMemo{Inputs: map[string]string{build: "e3b0"}}, build); !strings.Contains(why, "built nothing") {
		t.Fatalf("%q", why)
	}
	if builtNothing(buildMemo{Inputs: map[string]string{build: "x", "src/a.cu": "y"}}, build) != "" {
		t.Fatal("a build that read a source built nothing")
	}
	if builtNothing(buildMemo{Inputs: map[string]string{build: "x"}, Outputs: []string{"ws:build/a"}}, build) != "" {
		t.Fatal("a build that wrote a file built nothing")
	}
}

// 11/23 20:32:26 and 20:43:15 port_extra and port_ref_file_unknown failed on the missing:<group>
// rows kernel_unported asks of the integration unit: a row for a group of the planner's missing
// lines is not theirs to judge; one for another group, or of another unit, still is
func TestThePlannersMissingRowsAreNotPortExtra(t *testing.T) {
	integrationAnswersMu.Lock()
	integrationAnswers["uint"] = IntegrationModelPlan{Ops: []IntegrationOp{{Group: "g.weight", What: "x"}}}
	integrationAnswersMu.Unlock()
	defer func() { integrationAnswersMu.Lock(); delete(integrationAnswers, "uint"); integrationAnswersMu.Unlock() }()
	ws := t.TempDir()
	rows := "missing:g.weight\tk\tsrc/a.cu\tk\nmissing:h.weight\tk\tsrc/b.cu\tk\n"
	putTree(t, ws, map[string]string{".einsteinOS/port/uint.tsv": rows, ".einsteinOS/port/other.tsv": rows})
	syms := []RefSymbol{{File: "src/ref.cpp", Name: "f"}}
	extra, refFiles := strings.Join(portExtra(ws, "uint", syms), "\n"), strings.Join(portRefFiles(ws, "uint", syms), "\n")
	if strings.Contains(extra, "g.weight") || strings.Contains(refFiles, "g.weight") || !strings.Contains(extra, "h.weight") || !strings.Contains(refFiles, "h.weight") {
		t.Fatalf("extra:\n%s\nref files:\n%s", extra, refFiles)
	}
	if !strings.Contains(strings.Join(portExtra(ws, "other", syms), " "), "g.weight") {
		t.Fatal("another unit's missing: row is not judged")
	}
}
