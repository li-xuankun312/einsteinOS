package kernel

import (
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
