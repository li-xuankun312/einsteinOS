package kernel

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A unit does not write the reference's files into this repository at the paths the
// conversation is shown them (include/ref/… is include/strata/… masked). 11/19 08:10:15
// pb0569948de wrote include/ref/prefill/gemm.hpp, verified and pushed it at 08:15:16;
// p4fde6588a6 wrote include/ref/program/logits_selection.hpp and p71c0367d2e
// include/ref/plan/plan.hpp. 2c49a7d8 stopped such files from standing in the port map's
// reference column; written anywhere else they were not measured at all.
func TestAUnitDoesNotWriteTheReferencesFilesAtTheirShownPaths(t *testing.T) {
	shown := maskRequirements(UnitRequirements{Symbols: []RefSymbol{
		{File: "include/strata/prefill/gemm.hpp", Kind: "func", Name: "gemm_f16"}}},
		newRefMask(refMaskIdent, "Niko1221/Strata"))
	changed := []string{".einsteinOS/verify/pb0569948de/test.sh", "include/ref/prefill/gemm.hpp", "include/baloon/prefill_gemm.hpp"}
	got := refPathsWritten(changed, shown)
	if !reflect.DeepEqual(got, []string{"include/ref/prefill/gemm.hpp"}) {
		t.Fatalf("the reference's file written at its shown path: %v", got)
	}
	kind, text := unitGates(GateInput{Req: shown, Changed: changed, UnitID: "pb0569948de", KernelsMeasured: true,
		IncludersMeasured: true, RefPathsWritten: got})
	if kind != "ref_path_written" || !strings.Contains(text, "include/ref/prefill/gemm.hpp") {
		t.Fatalf("gate %q: %s", kind, text)
	}
	if got := refPathsWritten([]string{"include/baloon/prefill_gemm.hpp"}, shown); len(got) != 0 {
		t.Fatalf("the unit's own file taken for the reference's: %v", got)
	}
}

// At the start of a run a passed unit whose files on the branch include one at the path of a
// reference file as the conversations are shown it is reopened: 11/19 11:56:31 and 12:25:26 dev
// conversations of p4fde6588a6 and p71c0367d2e read include/ref/core/expert_cache.hpp — the
// shown path of include/strata/core/expert_cache.hpp, put on the branch by u6561d2f27c when it
// passed — and took it that reference headers belong under include/ref/. ref_path_written
// stops a unit writing such a file; the ones already on the branch nothing looked at.
func TestAPassedUnitsFileAtAReferencePathReopensIt(t *testing.T) {
	plan := &DevPlan{PortPass: map[string]bool{"u6561d2f27c": true, "u89be81de0b": true},
		PortUnits: []DevPlanNode{{ID: "u6561d2f27c"}, {ID: "u89be81de0b"}, {ID: "p71c0367d2e"}}}
	mask := newRefMask(refMaskIdent, "Niko1221/Strata")
	raw := map[string]UnitRequirements{
		"u6561d2f27c": {Symbols: []RefSymbol{{File: "include/strata/core/expert_cache.hpp", Kind: "class", Name: "ExpertCache"}}},
		"u89be81de0b": {Symbols: []RefSymbol{{File: "include/strata/kernels/ngram.hpp", Kind: "func", Name: "ngram_hash"}}},
		"p71c0367d2e": {Symbols: []RefSymbol{{File: "include/strata/plan/plan.hpp", Kind: "struct", Name: "Costs"}}},
	}
	shown := func(n DevPlanNode) UnitRequirements { return maskRequirements(raw[n.ID], mask) }
	prior := map[string][]string{
		"u6561d2f27c": {".einsteinOS/port/u6561d2f27c.tsv", "include/baloon/expert_cache.hpp", "include/ref/core/expert_cache.hpp"},
		"u89be81de0b": {".einsteinOS/port/u89be81de0b.tsv", "include/baloon/ngram.hpp"},
	}
	got := refPathRecheck(plan, func(id string) []string { return prior[id] }, shown)
	if strings.Join(got, " ") != "u6561d2f27c" {
		t.Fatalf("reopened %v, want u6561d2f27c", got)
	}
	f := plan.Reopen["u6561d2f27c"]
	if f.Kind != "ref_path_written" || !strings.Contains(f.Text, "include/ref/core/expert_cache.hpp") || plan.UnitPass(plan.PortUnits[0]) {
		t.Fatalf("reopen: %+v", f)
	}
}

// A reopened unit that leaves the file in place does not pass: the file at its reference
// file's path is its own whether this attempt wrote it or an earlier one put it on the branch.
func TestAFileAlreadyAtTheUnitsReferencePathIsItsToRemove(t *testing.T) {
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, "include/ref/prefill"), 0o755)
	os.WriteFile(filepath.Join(ws, "include/ref/prefill/gemm.hpp"), []byte("// pushed 11/19 08:15:16\n"), 0o644)
	req := maskRequirements(UnitRequirements{Symbols: []RefSymbol{{File: "include/strata/prefill/gemm.hpp", Kind: "func", Name: "gemm_f16"}}},
		newRefMask(refMaskIdent, "Niko1221/Strata"))
	changed := []string{"include/baloon/prefill_gemm.hpp"} // this attempt did not touch it
	if got := refPathsWritten(withRefPathsPresent(ws, changed, req), req); !reflect.DeepEqual(got, []string{"include/ref/prefill/gemm.hpp"}) {
		t.Fatalf("the file left at the reference path: %v", got)
	}
	os.Remove(filepath.Join(ws, "include/ref/prefill/gemm.hpp"))
	if got := refPathsWritten(withRefPathsPresent(ws, changed, req), req); len(got) != 0 {
		t.Fatalf("removed, still found: %v", got)
	}
}

// A port that sits where the reference keeps its own file (src/kernels/cuda/rope.cu, a path the
// mask leaves as it is) is not the reference's file under its name: only paths the mask
// changed (include/ref/… for include/strata/…) are.
func TestAPortAtTheReferencesOwnUnmaskedPathIsNotARefPath(t *testing.T) {
	req := maskRequirements(UnitRequirements{Symbols: []RefSymbol{
		{File: "src/kernels/cuda/rope.cu", Kind: "kernel", Name: "rope_kernel"},
		{File: "include/strata/kernels/rope.hpp", Kind: "func", Name: "build_rope_table"}}},
		newRefMask(refMaskIdent, "Niko1221/Strata"))
	got := refPathsWritten([]string{"src/kernels/cuda/rope.cu", "include/ref/kernels/rope.hpp"}, req)
	if !reflect.DeepEqual(got, []string{"include/ref/kernels/rope.hpp"}) {
		t.Fatalf("%v", got)
	}
}
