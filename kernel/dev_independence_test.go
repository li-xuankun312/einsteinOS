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

// A unit whose reference sources #include another unit's are linked: one ports the header,
// the other the implementation written against it. 11/19 06:50 pd8e380f419 (include/strata/
// platform/direct_file.hpp, memory.hpp) rewrote include/baloon/direct_file.hpp while
// p3199202ac2 (src/platform/direct_file.cpp, memory.cpp) wrote its .cpp against the old one;
// both were started side by side at 08:06:25 and 08:07:06 again.
func TestAUnitWhoseSourcesIncludeAnothersIsLinkedToIt(t *testing.T) {
	tree := []string{"include/strata/platform/direct_file.hpp", "include/strata/platform/memory.hpp",
		"src/platform/direct_file.cpp", "src/platform/memory.cpp", "include/strata/plan/plan.hpp"}
	// the reference's own lines at 01529aaa82b9
	direct := "// direct I/O\n#include \"strata/platform/direct_file.hpp\"\n\n#include <chrono>\n#include <cstdlib>\n#ifdef _WIN32\n#include <windows.h>\n#endif\n"
	memory := "#include \"strata/platform/memory.hpp\"\n#include <sys/mman.h>\n"
	if got := refIncludedFiles("src/platform/direct_file.cpp", direct, tree); !reflect.DeepEqual(got, []string{"include/strata/platform/direct_file.hpp"}) {
		t.Fatalf("direct_file.cpp includes %v", got)
	}

	hdr := DevPlanNode{ID: "pd8e380f419"}
	impl := DevPlanNode{ID: "p3199202ac2"}
	other := DevPlanNode{ID: "p71c0367d2e"}
	r := &devRun{repo: "o/r", raw: map[string]UnitRequirements{
		hdr.ID: {Sources: []string{"include/strata/platform/direct_file.hpp", "include/strata/platform/memory.hpp"}},
		impl.ID: {Sources: []string{"src/platform/direct_file.cpp", "src/platform/memory.cpp"},
			Includes: append(refIncludedFiles("src/platform/direct_file.cpp", direct, tree),
				refIncludedFiles("src/platform/memory.cpp", memory, tree)...)},
		other.ID: {Sources: []string{"include/strata/plan/plan.hpp"}},
	}}
	fp := func(u DevPlanNode) unitFootprint { return r.footprint(u, nil) }
	if s := fp(impl).sharedWith(fp(hdr)); len(s) == 0 {
		t.Fatalf("the implementation and the header it includes taken for independent")
	}
	if s := fp(hdr).sharedWith(fp(impl)); len(s) == 0 {
		t.Fatalf("the link is not the same both ways")
	}
	if s := fp(other).sharedWith(fp(impl)); len(s) != 0 {
		t.Fatalf("a header nothing of the other includes links them: %v", s)
	}
}
