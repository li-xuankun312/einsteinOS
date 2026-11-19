package kernel

import "sort"

// Units of a dev run may run side by side (dev_schedule.go) only when nothing measured links
// them. What a unit touches, as far as the kernel can measure before it runs:
//   - the reference files it ports (its requirements' sources),
//   - the Lean modules it writes,
//   - the files its earlier verified commits put on the branch ("unit <id>:" commits).
// Two units are independent when these are disjoint. 11/18 units 11 and 12 (uebd9b81357,
// u02c65ed515) both wrote lean/Baloon/F16BitsRounding.lean and ported the same sources.
// A file neither had touched before and both write is caught at the push (branchMovedUnder).

type unitFootprint struct {
	sources, modules, files map[string]bool
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
	fp := unitFootprint{sources: setOf(r.rawRequirements(unit).Sources), modules: setOf(unit.Modules), files: map[string]bool{}}
	if branchFiles != nil {
		fp.files = setOf(branchFiles(unit.ID))
	}
	return fp
}

// sharedWith: what a and b both touch, or nothing when they are independent.
func (a unitFootprint) sharedWith(b unitFootprint) []string {
	var out []string
	for _, pair := range [][2]map[string]bool{{a.sources, b.sources}, {a.modules, b.modules}, {a.files, b.files}} {
		for x := range pair[0] {
			if pair[1][x] {
				out = append(out, x)
			}
		}
	}
	sort.Strings(out)
	return out
}
