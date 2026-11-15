package kernel

// port_recheck.go — at the start of a run, the units that passed are held to what the
// reference requires as measured now.
//
// A unit's pass was judged against the definitions measured from the reference when it ran.
// When the kernel measures them differently later (a fix to how definitions are read, a
// change to what counts as a counterpart), a passed unit is not verified again unless
// something reopens it — and the kernel's own improvement never reaches the code already on
// the branch. 11/15 08:59 the run restarted on 7541e701 (a kernel declared with
// __launch_bounds__ is measured by its own name): unit ued2beb8a35 now owns 162 definitions
// (158 when it passed), ua1c077067b 250 (248), u739c93b0c3 129 (125), u7663fc74b8 98 (97);
// all four had passed, and none was looked at again.

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

const portRecheckFmt = "port_incomplete: measured again when the run restarted, %[1]d definition(s) the reference requires of this unit have no counterpart on the branch:\n%[2]s"

// portRecheck reopens, in plan, every passed unit (not the integration unit) whose port map
// on the branch, extracted to tree, leaves a definition reqFn says it must port without a
// counterpart. It returns the units reopened.
func portRecheck(plan *DevPlan, tree string, reqFn func(DevPlanNode) UnitRequirements) []string {
	var reopened []string
	for _, l := range plan.Leaves() {
		if l.Integration || !plan.UnitPass(l) {
			continue
		}
		req := reqFn(l)
		if len(req.Symbols) == 0 {
			continue
		}
		miss := portMissing(tree, l.ID, req.Symbols)
		if len(miss) == 0 {
			continue
		}
		shown := miss
		if len(shown) > 30 {
			shown = append(append([]string{}, shown[:30]...), fmt.Sprintf("  … %d more", len(miss)-30))
		}
		plan.MarkReopen(l.ID, DevFailure{Kind: "port_incomplete", Text: fmt.Sprintf(portRecheckFmt, len(miss), strings.Join(shown, "\n"))})
		reopened = append(reopened, l.ID)
	}
	return reopened
}

// branchTree extracts branch of the local clone dir into a new temporary directory.
func branchTree(dir, branch string) (string, error) {
	tmp, err := os.MkdirTemp("", "einsteinos_recheck_")
	if err != nil {
		return "", err
	}
	cmd := exec.Command("bash", "-c", fmt.Sprintf("git -C %q archive %q | tar -x -C %q", dir, branch, tmp))
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(tmp)
		return "", fmt.Errorf("%v: %s", err, tailText(string(out), 300))
	}
	return tmp, nil
}

// devPortRecheck runs portRecheck on the dev branch at the start of a run and saves the plan
// when it reopened anything.
func devPortRecheck(r *devRun) {
	dir, err := devRepoLocalDir(r.devRepo)
	if err != nil {
		return
	}
	tree, err := branchTree(dir, r.branch)
	if err != nil {
		log.Printf("dev_requirements: %s: the branch could not be read to hold passed units to the reference again (%v)", r.repo, err)
		return
	}
	defer os.RemoveAll(tree)
	if ids := portRecheck(r.plan, tree, r.requirements); len(ids) > 0 {
		log.Printf("dev_requirements: %s %d passed unit(s) no longer cover what the reference requires — reopened: %s", r.repo, len(ids), strings.Join(ids, " "))
		devPlanSave(r.repo, r.devRepo, r.branch, r.plan, fmt.Sprintf("plan: %s reopened, measured against the reference again", strings.Join(ids, " ")))
	}
}
