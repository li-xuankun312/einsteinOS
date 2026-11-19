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

// branchTree extracts branch of the clone dir into a new temporary directory — the branch as
// the remote has it (refs/remotes/origin/<branch>, which the sync that made dir current has
// just fetched), not the local branch of that name: GitCloneOrReuse ends in `git reset --hard
// origin/HEAD`, and with the dev branch checked out that moves the local branch to main's
// commit. 11/17 02:36 and 11/16 08:42 that made every passed unit look as if its port map were
// not there, and each was reopened, while the branch on GitHub held them. A branch that was
// never pushed has no remote-tracking branch; the local one is read then.
func branchTree(dir, branch string) (string, error) {
	tmp, err := os.MkdirTemp("", "einsteinos_recheck_")
	if err != nil {
		return "", err
	}
	ref := branch
	if exec.Command("git", "-C", dir, "rev-parse", "--verify", "-q", "refs/remotes/origin/"+branch).Run() == nil {
		ref = "refs/remotes/origin/" + branch
	}
	cmd := exec.Command("bash", "-c", fmt.Sprintf("git -C %q archive %q | tar -x -C %q", dir, ref, tmp))
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
	if ids := r.recheckOn(tree); len(ids) > 0 {
		log.Printf("dev_requirements: %s %d passed unit(s) no longer cover what the reference requires — reopened: %s", r.repo, len(ids), strings.Join(ids, " "))
		devPlanSave(r.repo, r.devRepo, r.branch, r.plan, fmt.Sprintf("plan: %s reopened, measured against the reference again", strings.Join(ids, " ")))
	}
	if ids, told := portScriptRecheck(r.plan, tree, r.branch, r.shown); len(ids)+len(told) > 0 {
		log.Printf("dev_requirements: %s %d passed unit(s) whose scripts on the branch read their port map — reopened: %s; %d unit(s) to do told before they start: %s",
			r.repo, len(ids), strings.Join(ids, " "), len(told), strings.Join(told, " "))
		devPlanSave(r.repo, r.devRepo, r.branch, r.plan, fmt.Sprintf("plan: scripts reading a port map — reopened %s, told %s", strings.Join(ids, " "), strings.Join(told, " ")))
	}
	prior := func(id string) []string { return devUnitPriorFiles(dir, r.branch, id) }
	if ids := refPathRecheck(r.plan, prior, r.shown); len(ids) > 0 {
		log.Printf("dev_requirements: %s %d passed unit(s) left a file at a reference file's path on the branch — reopened: %s", r.repo, len(ids), strings.Join(ids, " "))
		devPlanSave(r.repo, r.devRepo, r.branch, r.plan, fmt.Sprintf("plan: %s reopened, a file at a reference file's path", strings.Join(ids, " ")))
	}
}

// recheckOn holds the passed units to what the reference requires as the branch writes it:
// the requirements the verification checks a unit against (r.shown, the reference masked),
// so that the same port map reads the same both times. 11/17 10:12:54 uf7cdc0dcca, passed at
// 05:22 with its 162 lines (include/ref/core/layer.hpp BlockBuffers …), was reopened for 58
// definitions of include/strata/core/layer.hpp having no line — the raw reference path,
// which no port map holds — and 12:05 u89be81de0b ran again for its 25 of
// include/strata/kernels/ngram.hpp, all 25 on the branch as include/ref/kernels/ngram.hpp.
func (r *devRun) recheckOn(tree string) []string {
	return portRecheck(r.plan, tree, r.shown)
}

// refPathRecheck reopens, in plan, every passed unit (not the integration unit) one of whose
// files on the branch (priorFiles: what its verified commits put there) is at the path of a
// reference file as the conversations are shown it (shownFn: the masked requirements of every
// unit). 11/19 11:56:31 and 12:25:26 dev conversations of p4fde6588a6 and p71c0367d2e read
// include/ref/core/expert_cache.hpp — include/strata/core/expert_cache.hpp shown, put on the
// branch by u6561d2f27c when it passed — and took it that reference headers belong under
// include/ref/. ref_path_written (e3da5f3e) stops a unit writing one; this finds the ones the
// branch already holds, and the unit that put each there ports it again without it.
func refPathRecheck(plan *DevPlan, priorFiles func(string) []string, shownFn func(DevPlanNode) UnitRequirements) []string {
	ref := map[string]bool{}
	for _, l := range plan.Leaves() {
		for _, f := range shownFn(l).MaskedPaths {
			ref[f] = true
		}
	}
	var reopened []string
	for _, l := range plan.Leaves() {
		if l.Integration || !plan.UnitPass(l) {
			continue
		}
		var hits []string
		for _, f := range priorFiles(l.ID) {
			if ref[f] {
				hits = append(hits, f)
			}
		}
		if len(hits) == 0 {
			continue
		}
		hits = sortedCopy(hits)
		own := sortedCopy(refFilesOf(shownFn(l).Symbols))
		plan.MarkReopen(l.ID, DevFailure{Kind: "ref_path_written",
			Text: fmt.Sprintf(gateRefPathWrittenFmt, len(hits), shortList(own, 12), strings.Join(hits, "\n"))})
		reopened = append(reopened, l.ID)
	}
	return reopened
}

// portScriptRecheck finds, on the branch's tree, the units whose verify scripts still read
// their port map (scriptsReadingPortMap) — scripts written before 1ccc5551 made that
// port_count_in_script. A passed unit is reopened with that failure; a unit still to do whose
// next attempt starts from this branch (its last failure's base, or none) has the measurement
// added to its last failure, so its conversation knows before it starts. 11/19 16:44:02,
// 16:47:04 and 16:51:11 p42de699ebc, p71c0367d2e and p17652417b0 were each sent back for it
// after a 3–4 minute verification, their scripts carrying the port check they had before, and
// 16:48:05 p71c0367d2e's conversation read p42de699ebc's test.sh to write its own.
func portScriptRecheck(plan *DevPlan, tree, branch string, shownFn func(DevPlanNode) UnitRequirements) (reopened, told []string) {
	for _, l := range plan.Leaves() {
		if l.Integration || plan.Reopened(l.ID) {
			continue // a reopened unit runs on the failure it was reopened for
		}
		req := shownFn(l)
		scripts := unitPortScripts(tree, l.ID, req)
		if len(scripts) == 0 {
			continue
		}
		text := fmt.Sprintf(gatePortScriptFmt, strings.Join(scripts, ", "), PortMapPath(l.ID), len(req.Symbols))
		if plan.UnitPass(l) {
			plan.MarkReopen(l.ID, DevFailure{Kind: "port_count_in_script", Text: text})
			reopened = append(reopened, l.ID)
			continue
		}
		f := plan.LastFailure[l.ID]
		if f.BaseRef != "" && f.BaseRef != branch {
			continue // it continues from its own branch, which this tree is not
		}
		if strings.Contains(f.Text, "port_count_in_script:") {
			continue
		}
		if f.Kind == "" {
			f.Kind = "port_count_in_script"
		}
		f.Text = strings.TrimSpace(f.Text + "\n" + text)
		if plan.LastFailure == nil {
			plan.LastFailure = map[string]DevFailure{}
		}
		plan.LastFailure[l.ID] = f
		told = append(told, l.ID)
	}
	return reopened, told
}
