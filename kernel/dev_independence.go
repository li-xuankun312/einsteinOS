package kernel

import "sort"

// Units of a dev run may run side by side (dev_schedule.go) only when nothing measured links
// them. What a unit touches, as far as the kernel can measure before it runs:
//   - the reference files it ports (its requirements' sources),
//   - the Lean modules it writes,
//   - the files its earlier verified commits put on the branch ("unit <id>:" commits),
//   - the reference files its sources #include: a unit porting one of them is linked to it.
// Two units are independent when these are disjoint. 11/18 units 11 and 12 (uebd9b81357,
// u02c65ed515) both wrote lean/Baloon/F16BitsRounding.lean and ported the same sources.
// A file neither had touched before and both write is caught at the push (branchMovedUnder).

type unitFootprint struct {
	sources, modules, files, includes map[string]bool
}

func setOf(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		if x != "" {
			m[x] = true
		}
	}
	return m
}

// footprint: what unit touches, measured from the plan, the reference and the branch.
func (r *devRun) footprint(unit DevPlanNode, branchFiles func(unitID string) []string) unitFootprint {
	req := r.rawRequirements(unit)
	fp := unitFootprint{sources: setOf(req.Sources), modules: setOf(unit.Modules), files: map[string]bool{}, includes: setOf(req.Includes)}
	if branchFiles != nil {
		fp.files = setOf(branchFiles(unit.ID))
	}
	return fp
}

// sharedWith: what a and b both touch, or nothing when they are independent.
func (a unitFootprint) sharedWith(b unitFootprint) []string {
	var out []string
	// a source of one the other's sources #include: 11/19 pd8e380f419 (the platform headers)
	// and p3199202ac2 (the .cpp that include them) ran side by side and wrote against each other
	for _, pair := range [][2]map[string]bool{{a.sources, b.sources}, {a.modules, b.modules}, {a.files, b.files},
		{a.sources, b.includes}, {a.includes, b.sources}} {
		for x := range pair[0] {
			if pair[1][x] {
				out = append(out, x)
			}
		}
	}
	sort.Strings(out)
	return out
}
