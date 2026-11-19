package kernel

import (
	"reflect"
	"testing"
)

// 11/18 units 11 and 12 both wrote lean/Baloon/F16BitsRounding.lean and ported the same
// sources: they are not independent. Port units of different directories are.
func TestUnitsAreIndependentWhenNothingMeasuredLinksThem(t *testing.T) {
	u11 := DevPlanNode{ID: "uebd9b81357", Modules: []string{"F16BitsRounding.lean"}}
	u12 := DevPlanNode{ID: "u02c65ed515", Modules: []string{"F16BitsRounding.lean"}}
	p1 := DevPlanNode{ID: "p3798e66f3c"}
	p2 := DevPlanNode{ID: "p71c0367d2e"}
	p3 := DevPlanNode{ID: "pd8e380f419"}
	r := &devRun{repo: "o/r", raw: map[string]UnitRequirements{
		u11.ID: {Sources: []string{"include/strata/artifact/dequant.hpp", "src/kernels/cuda/qsa.cu"}},
		u12.ID: {Sources: []string{"include/strata/artifact/dequant.hpp", "src/kernels/cuda/qsa.cu"}},
		p1.ID:  {Sources: []string{"include/strata/ngram/ple_reader.hpp"}},
		p2.ID:  {Sources: []string{"include/strata/plan/plan.hpp"}},
		p3.ID:  {Sources: []string{"include/strata/platform/memory.hpp"}},
	}}
	onBranch := map[string][]string{
		p2.ID: {"include/baloon/plan.hpp"},
		p3.ID: {"include/baloon/plan.hpp", "include/baloon/memory.hpp"}, // wrote a file p2 owns
	}
	files := func(id string) []string { return onBranch[id] }
	fp := func(u DevPlanNode) unitFootprint { return r.footprint(u, files) }

	if s := fp(u11).sharedWith(fp(u12)); len(s) == 0 {
		t.Fatalf("units 11 and 12 taken for independent")
	}
	if s := fp(p1).sharedWith(fp(p2)); len(s) != 0 {
		t.Fatalf("port units of different directories share %v", s)
	}
	if s := fp(p2).sharedWith(fp(p3)); !reflect.DeepEqual(s, []string{"include/baloon/plan.hpp"}) {
		t.Fatalf("a file both have on the branch: %v", s)
	}
}
