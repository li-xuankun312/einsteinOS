package kernel

// dev_run.go — dev_from_theorems, rebuilt around three facts the Strata run
// (2026/11/01) exposed:
//
//  1. THE PLAN IS ONE OBJECT (dev_plan_store.go). Planned once, stored on the
//     branch, units identified by their module set, pass state on the modules.
//     Re-entering this function resumes; it never re-plans.
//  2. THE KERNEL JUDGES COMPILE/TEST, ON THE REAL MACHINE, AND PUSHES (dev_verify.go).
//     The child writes code and two scripts and stops. It does not push.
//  3. A FAILED UNIT IS FIXED AS THAT UNIT. The failure (real build output) and the
//     branch holding the failing code travel with the unit in the plan, so the
//     next attempt starts from the code, with the error, in the same loop —
//     not as a separate whole-project dev_fix that never knew which unit failed.
//
// Per unit, one attempt is: prime the local mirror from the branch tip → fork a
// work (or fix) conversation → wait for it and everything it started → the
// kernel verifies + pushes → a reviewer judges the real branch. A unit that keeps
// failing is SPLIT (dev_split_node), not retried forever.

import (
	"sort"
	"fmt"
	"log"
	"strings"
	"time"
)

// devMaxSilentAttempts: conversations in a row that wrote no file before the run stops.
// One may be a fluke; the second, started fresh with the first one's last words in front
// of it, is not.
const devMaxSilentAttempts = 2

func passedLeaves(p *DevPlan) int {
	n := 0
	for _, l := range p.Leaves() {
		if p.UnitPass(l) {
			n++
		}
	}
	return n
}

// devCompletionPR opens the PR that marks a finished dev branch; a variable so a
// test does not reach for the GitHub API.
var devCompletionPR = dev_open_completion_pr

// devBranchFor names the branch a dev run works on. It used to be einsteinOS/dev-<pid>, and
// a restarted process has a different pid (6, 7, … from the same small range), so the
// plan and the units that had already passed were on a branch the new process never
// looked at: a restart began again from unit 1. The branch is now named by what it
// develops (the reference repo at its commit); the pid is the fallback when the commit
// is not known.
func devBranchFor(repo, refSHA string, pid int64) string {
	if refSHA == "" {
		return fmt.Sprintf("einsteinOS/dev-%d", pid)
	}
	return "einsteinOS/dev-" + shortHash(repo + "@" + refSHA)[:10]
}

// devRefSHA identifies the reference commit the theory was made from. The dev
// step does not carry it, so take the last theory stored for the repo.
func devRefSHA(repo, stepSHA string) string {
	if stepSHA != "" {
		return stepSHA
	}
	theorem_mu.Lock()
	defer theorem_mu.Unlock()
	sha := ""
	for i := 0; i < nr_theorems; i++ {
		if theorem_table[i].repo == repo {
			sha = theorem_table[i].sha
		}
	}
	return sha
}

// devUnitFromNode turns a plan node into the (Files, Theory) pair the proof
// gate and the reviewer read.
func devUnitFromNode(n DevPlanNode, sections []devFileSection) devUnit {
	byName := map[string]string{}
	for _, s := range sections {
		byName[strings.TrimSpace(s.path)] = s.content
	}
	var th strings.Builder
	for _, m := range n.Modules {
		th.WriteString(byName[m])
		th.WriteString("\n\n")
	}
	return devUnit{Files: append([]string(nil), n.Modules...), Theory: strings.TrimSpace(th.String())}
}

func nodeDesc(n DevPlanNode) string {
	d := strings.Join(n.Modules, ", ")
	if n.Scope != "" {
		d += " [" + n.Scope + "]"
	}
	return d
}

// devRun carries what every unit of one dev_from_theorems call shares.
type devRun struct {
	pid                         int64
	repo, devRepo, branch, task string
	hwInfo, gpuAll              string
	plan                        *DevPlan
	sections                    []devFileSection
	reqs                        map[string]UnitRequirements // per unit, measured once (unit_requirements.go)
	raw                         map[string]UnitRequirements // before definition ownership
	owners                      map[string]string           // "file\tname" -> the unit that ports it
	ownersFor                   string                      // the leaves the owners were computed for
}

// devNothingToBuildFmt: a unit closed without a conversation (args: repo, n, total, id,
// definitions of its sources, modules no reference file was found for).
const devNothingToBuildFmt = "dev_from_theorems: %[1]s unit %[2]d/%[3]d id=%[4]s has nothing of its own to build (its sources' %[5]d definitions belong to other units; no GPU source; no ISA path; unmeasured modules %[6]v) — closed without a conversation"

// definitionOwners assigns every reference definition to exactly one unit, in plan order:
// the first unit whose sources hold it ports it, the others do not list it. Units split
// from one node divide its sources between them (file i to sibling i mod k). 11/05
// src/program/generate.cpp was a source of 4 units, and the three units split from
// u042aaf67ce each had to port the same 90 definitions.
func (r *devRun) definitionOwners() map[string]string {
	leaves := r.plan.Leaves()
	var ids []string
	for _, l := range leaves {
		ids = append(ids, l.ID)
	}
	key := strings.Join(ids, ",")
	if r.owners != nil && r.ownersFor == key {
		return r.owners
	}
	r.owners, r.ownersFor, r.reqs = map[string]string{}, key, nil
	siblings := map[string][]string{}
	for _, l := range leaves {
		if l.Parent != "" {
			siblings[l.Parent] = append(siblings[l.Parent], l.ID)
		}
	}
	for _, l := range leaves {
		if l.Integration {
			continue
		}
		raw := r.rawRequirements(l)
		mine := map[string]bool{}
		if sib := siblings[l.Parent]; l.Parent != "" && len(sib) > 1 {
			files := append([]string(nil), raw.Sources...)
			sort.Strings(files)
			at := 0
			for i, id := range sib {
				if id == l.ID {
					at = i
				}
			}
			for i, f := range files {
				if i%len(sib) == at {
					mine[f] = true
				}
			}
		}
		for _, sym := range raw.Symbols {
			k := sym.File + "\t" + sym.Name
			if _, taken := r.owners[k]; taken {
				continue
			}
			if len(mine) > 0 && !mine[sym.File] {
				continue
			}
			r.owners[k] = l.ID
		}
	}
	return r.owners
}

// requirements measures what the reference says the unit is made of, once per unit.
// shown is what a conversation is given of the unit's requirements: the reference masked.
func (r *devRun) shown(unit DevPlanNode) UnitRequirements {
	return maskRequirements(r.requirements(unit), newRefMask(refMaskIdent, r.repo))
}

// requirements: what the unit is measured against — its raw requirements, with only the
// definitions it owns (definitionOwners).
func (r *devRun) requirements(unit DevPlanNode) UnitRequirements {
	req := r.rawRequirements(unit)
	if unit.Integration || r.plan == nil {
		return req
	}
	owners := r.definitionOwners()
	if cached, ok := r.reqs[unit.ID]; ok {
		return cached
	}
	if r.reqs == nil {
		r.reqs = map[string]UnitRequirements{}
	}
	var own []RefSymbol
	for _, sym := range req.Symbols {
		if owners[sym.File+"\t"+sym.Name] == unit.ID {
			own = append(own, sym)
		}
	}
	if len(own) != len(req.Symbols) {
		log.Printf("dev_requirements: %s unit %s ports %d of its sources' %d definitions; the rest belong to other units", r.repo, unit.ID, len(own), len(req.Symbols))
	}
	req.Symbols = own
	r.reqs[unit.ID] = req
	return req
}

func (r *devRun) rawRequirements(unit DevPlanNode) UnitRequirements {
	if req, ok := r.raw[unit.ID]; ok {
		return req
	}
	if r.raw == nil {
		r.raw = map[string]UnitRequirements{}
	}
	sha := ""
	if r.plan != nil {
		sha = r.plan.RefSHA
	}
	var req UnitRequirements
	if unit.Integration {
		tests, cuda := refIntegrationTests(r.repo, sha)
		req = UnitRequirements{Tests: tests, RequireGPU: cuda, Repo: r.repo, SHA: sha}
		log.Printf("dev_requirements: %s integration unit %s: %d registered test(s) of the reference; GPU required=%v", r.repo, unit.ID, len(tests), cuda)
		r.raw[unit.ID] = req
		return req
	} else if len(unit.Sources) > 0 {
		req = requirementsForSources(r.repo, sha, unit.Sources)
	} else {
		req = measureUnitRequirements(r.repo, sha, devUnitFromNode(unit, r.sections).Theory)
	}
	logUnitRequirements(r.repo, unit.ID, req)
	r.raw[unit.ID] = req
	return req
}

func (r *devRun) gpuUsable() string {
	_, u := pr_gpu_args(r.pid)
	return u
}

// dev_from_theorems drives the plan to "every unit passed".
func dev_from_theorems(pid int64, step *Step) (string, error) {
	repo := step.Owner + "/" + step.Repo

	theory := theorem_get(repo, "")
	if theory == "" {
		return "", fmt.Errorf("dev_from_theorems: no theory for %s", repo)
	}

	pr := GetPR(pid)
	task, hwInfo := "", ""
	gpuAll, _ := pr_gpu_args(pid)
	if pr != nil {
		task = strip_credentials(pr.Title)
		hwInfo = pr.HWInfo
		writeGHToken(pr.Title)
		// the dev conv must NOT know the source repo: only the dev repo and the abstract theory
		var refs []string
		for _, ref := range pr.RefRepos {
			refs = append(refs, ref.Owner+"/"+ref.Name)
		}
		task = newRefMask(refMaskText, refs...).Replace(task)
	}

	devRepos := GetDevRepos()
	if len(devRepos) == 0 {
		return "", fmt.Errorf("dev_from_theorems: no dev repo registered — the kernel pushes the code itself and has nowhere to push")
	}
	devRepo := devRepos[0]
	refSHA := devRefSHA(repo, step.SHA)
	branch := devBranchFor(repo, refSHA, pid)
	// a run started before the branch was named by what it develops left its work on a
	// pid-named branch: continue from it instead of starting over (dev_adopt.go)
	adoptLegacyDevBranch(devRepo, repo, refSHA, branch)

	// proofs are verified by the KERNEL before any dev child runs
	patchedTheory, perr := dev_proof_preflight(pid, repo, branch, theory)
	if perr != nil {
		PRFeedbackAdd(pid, "- "+perr.Error())
		return "", perr
	}
	theory = patchedTheory
	if serr := dev_store_theory(theory, repo, branch); serr != nil {
		PRFeedbackAdd(pid, "- "+serr.Error())
		return "", serr
	}

	plan := dev_get_plan(pid, repo, devRefSHA(repo, step.SHA), theory, branch)

	run := &devRun{
		pid: pid, repo: repo, devRepo: devRepo, branch: branch, task: task,
		hwInfo: hwInfo, gpuAll: gpuAll,
		plan: plan, sections: dev_extract_file_sections(theory),
	}

	if n := plan.RejudgeIfGatesChanged(); n > 0 {
		log.Printf("dev_plan: %s %d unit(s) passed under another gate set — judged again under %s", repo, n, devGateSet)
		devPlanSave(repo, devRepo, branch, plan, "plan: rejudge under "+devGateSet)
	}
	// every code file of the reference commit has a unit: the ones no theory unit's measured
	// sources include get port units, one per directory
	if all := refPortSources(repo, plan.RefSHA); len(all) > 0 {
		covered := map[string]bool{}
		for _, l := range plan.Leaves() {
			if len(l.Sources) == 0 {
				for _, f := range run.requirements(l).Sources {
					covered[f] = true
				}
			}
		}
		if tests, _ := refIntegrationTests(repo, plan.RefSHA); len(tests) > 0 && plan.SetIntegrationUnit(IntegrationUnitID(plan.RefSHA)) {
			log.Printf("dev_plan: %s the reference registers %d tests: integration unit %s is the last unit", repo, len(tests), IntegrationUnitID(plan.RefSHA))
			devPlanSave(repo, devRepo, branch, plan, "plan: integration unit")
		}
		if n := plan.AddPortUnits(all, covered); n > 0 {
			log.Printf("dev_plan: %s reference has %d code files; theory units cover %d; %d port unit(s) added for the rest", repo, len(all), len(covered), n)
			devPlanSave(repo, devRepo, branch, plan, fmt.Sprintf("plan: %d port units", n))
		}
	}

	var lastStdout string
	// every loop turn is one attempt at one unit (or one split). The ceiling is
	// a backstop against a bug, not a limit anyone should reach.
	for turn := 0; turn < 400; turn++ {
		leaves := plan.Leaves()
		idx := -1
		for i, l := range leaves {
			if !plan.UnitPass(l) {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		unit := leaves[idx]

		// whether to split is the kernel's call, from numbers
		need, why := dev_node_needs_split(plan, unit)
		if need && !plan.VerifySeen[unit.ID] && plan.FixAttempts[unit.ID] < 2*DevMaxFixAttempts {
			log.Printf(devSplitUnseenFmt, repo, unit.ID, why)
			need = false
		}
		if need {
			if kids := dev_split_node(pid, plan, unit.ID, repo, devRepo, branch, theory, why); kids != nil {
				continue // the leaves changed; look again
			}
			if plan.FixAttempts[unit.ID] >= DevMaxFixAttempts {
				msg := fmt.Sprintf("unit %s (%s) failed %d attempts and cannot be split further; last failure: %s",
					unit.ID, nodeDesc(unit), plan.FixAttempts[unit.ID], truncStr(plan.LastFailure[unit.ID].Text, 400))
				PRFeedbackAdd(pid, "- dev_from_theorems: "+msg)
				return lastStdout, fmt.Errorf("dev_from_theorems: %s", msg)
			}
			log.Printf("dev_from_theorems: %s unit %s is over budget (%s) but cannot be split; running it as is", repo, unit.ID, why)
		}

		stdout, passed, requeue := run.runUnit(unit, idx, len(leaves))
		if stdout != "" {
			lastStdout = stdout
		}
		// A conversation that writes nothing is not a unit that is too big: retrying it, or
		// splitting the unit, starts the same kind of conversation again. Two in a row means
		// the cause is outside the unit — stop, say so, keep everything (the plan and the
		// passed units are on the branch), and leave the decision to a person.
		// no GPU toolchain on this machine: no retry or split can build a GPU unit here
		if lf := plan.LastFailure[unit.ID]; !passed && !requeue && lf.Kind == GateToolchainMissing {
			msg := fmt.Sprintf("unit %s (%s): %s. the code is kept on %s; run on a machine with the toolchain to continue",
				unit.ID, nodeDesc(unit), lf.Text, lf.BaseRef)
			log.Printf("dev_from_theorems: %s %s", repo, msg)
			PRFeedbackAdd(pid, "- dev_from_theorems: "+msg)
			return lastStdout, fmt.Errorf("dev_from_theorems: %s", msg)
		}
		if !passed && !requeue && plan.Silent(unit.ID) >= devMaxSilentAttempts {
			msg := fmt.Sprintf("unit %s (%s): the last %d conversations wrote no file. a conversation that does not start is not "+
				"cured by retrying or by splitting the unit, so the run stops here. the plan and the %d unit(s) already passed are "+
				"kept on branch %s; fix the cause and start again to continue. what the last one said:\n%s",
				unit.ID, nodeDesc(unit), plan.Silent(unit.ID), passedLeaves(plan), branch,
				truncStr(plan.LastFailure[unit.ID].Text, 1500))
			log.Printf("dev_from_theorems: %s %s", repo, truncStr(strings.ReplaceAll(msg, "\n", " | "), 700))
			PRFeedbackAdd(pid, "- dev_from_theorems: "+msg)
			return lastStdout, fmt.Errorf("dev_from_theorems: %s", msg)
		}
		if requeue {
			plan.InfraFails++
			devPlanSave(repo, devRepo, branch, plan, "plan: infrastructure retry")
			if plan.InfraFails > 5 {
				return lastStdout, fmt.Errorf("dev_from_theorems: %s unit %s: git/push failed %d times in a row", repo, unit.ID, plan.InfraFails)
			}
			pr_push_step(pid, &Step{Kind: STEP_DEV_FROM_THEOREMS, Owner: step.Owner, Repo: step.Repo})
			return lastStdout, nil
		}
		plan.InfraFails = 0
		_ = passed
	}

	// every unit passed verification on the real machine and review on the real branch
	leaves := plan.Leaves()
	var verdicts []string
	for i, l := range leaves {
		verdicts = append(verdicts, fmt.Sprintf("unit %d/%d: PASS — %s", i+1, len(leaves), nodeDesc(l)))
	}
	log.Printf("dev_from_theorems: %s all %d units pass. pipeline complete, branch %s ready for main conv.\n%s",
		repo, len(leaves), branch, plan.Summary())
	PRFeedbackAdd(pid, fmt.Sprintf("- dev_from_theorems: all %d units pass, branch %s ready", len(leaves), branch))
	devCompletionPR(pid, step.Owner, step.Repo, repo, branch, step.SHA, verdicts)
	log.Printf("dev_from_theorems: conv_registry:\n%s", ConvRegistryStatus())
	// one pr_pipeline is complete: the next reference commit (ref_cursor.go), whose dev
	// branch starts from this one
	recordCompletedBranch(devRepo, branch)
	advanceAfterPipeline(pid, step.Owner, step.Repo)
	return lastStdout, nil
}

// runUnit makes ONE attempt at a unit. Returns the child's last text, whether
// the unit passed, and whether infrastructure (git) failed so the whole call
// should be re-queued.
func (r *devRun) runUnit(unit DevPlanNode, idx, total int) (stdout string, passed, requeue bool) {
	plan := r.plan
	fail := plan.LastFailure[unit.ID]
	attempt := plan.FixAttempts[unit.ID] + 1
	baseRef := r.branch
	if fail.BaseRef != "" {
		baseRef = fail.BaseRef
	}

	// a unit with nothing measured to build: no definition of its own (every one belongs to
	// another unit, or its theory names no reference file), no GPU source, no ISA path. 11/05
	// u208dfb4222 ("sources []; definitions to port 0") would have passed on anything written.
	if req := r.requirements(unit); !unit.Integration && req.RefRead && len(req.Symbols) == 0 && !req.GPU() && len(req.ISAFlags) == 0 {
		log.Printf(devNothingToBuildFmt, r.repo, idx+1, total, unit.ID, len(r.rawRequirements(unit).Symbols), req.Unmeasured)
		plan.MarkUnitPass(unit)
		devPlanSave(r.repo, r.devRepo, r.branch, plan, fmt.Sprintf("plan: unit %s has nothing of its own to build", unit.ID))
		return "", true, false
	}
	if st, ok := plan.ReviewPending[unit.ID]; ok {
		log.Printf("dev_from_theorems: %s unit %d/%d id=%s was verified and pushed before the run stopped — resuming with its review",
			r.repo, idx+1, total, unit.ID)
		return r.reviewUnit(unit, idx, total, "", st, "")
	}

	cloneDir, err := devRepoLocalDir(r.devRepo)
	if err != nil {
		log.Printf("dev_run: %s no local clone: %v", r.repo, err)
		return "", false, true
	}
	if err := gitUseBranchFromRemote(cloneDir, baseRef, r.branch); err != nil {
		log.Printf("dev_run: %s checkout %s: %v", r.repo, baseRef, err)
		return "", false, true
	}
	mirror := devMirrorDir(r.devRepo)
	base, err := primeMirror(cloneDir, mirror)
	if err != nil {
		log.Printf("dev_run: %s %v", r.repo, err)
		return "", false, true
	}

	prompt := r.unitPrompt(unit, idx, total, attempt, fail, baseRef)
	role := fmt.Sprintf("dev_unit_%d", idx+1)
	if attempt > 1 {
		role = fmt.Sprintf("dev_fix_%d", attempt-1)
	}
	log.Printf("dev_from_theorems: %s unit %d/%d id=%s attempt %d forking %s (prompt %d bytes, base %s, modules: %s)",
		r.repo, idx+1, total, unit.ID, attempt, role, len(prompt), baseRef, nodeDesc(unit))

	cond := devUnitCondition()
	var child int64
	var convID string
	var status map[string]string
	for try := 1; try <= 5; try++ {
		var ferr error
		child, ferr = ForkWithTermination(prompt, ".", role, cond)
		if ferr != nil {
			log.Printf("dev_from_theorems: %s unit %s fork failed (try %d/5): %v", r.repo, unit.ID, try, ferr)
			continue
		}
		Wait(child)
		if m := getMeta(child); m != nil && m.ConvID != "" {
			convID = m.ConvID
			RegisterConv(r.pid, convID, r.repo, role, idx+1)
			ConvDone(convID, "done")
			plan.SetConv(unit.ID, convID)
		}
		stdout, status = devCollectLite(child, r.repo)
		// only "the child did nothing" is worth a fresh child; anything else is
		// judged by what is in the mirror, not by what the child says about it
		if status["compile"] == "no_response" || strings.Contains(stdout, ModelDowngradeMarker) {
			log.Printf("dev_from_theorems: %s unit %s no response (try %d/5), retrying fresh", r.repo, unit.ID, try)
			base, _ = primeMirror(cloneDir, mirror)
			continue
		}
		break
	}

	// the conversation may have started others; do not judge half a unit
	if convID != "" && VerifySeen(convID) {
		if plan.VerifySeen == nil {
			plan.VerifySeen = map[string]bool{}
		}
		plan.VerifySeen[unit.ID] = true
	}
	if convID != "" {
		if !WaitConvTree(convID, 90*time.Minute, 10*time.Second) {
			log.Printf("dev_from_theorems: %s unit %s: conversations below %s still running after 90 min", r.repo, unit.ID, shortID(convID))
		}
		if m := ConvManifest(convID); len(m.Children) > 0 {
			log.Printf("dev_from_theorems: %s unit %s started %d conversation(s): %v", r.repo, unit.ID, len(m.Flatten()), sortedStatuses(m))
		}
	}

	// gpu bookkeeping still reads the child's own p2p report (a measurement it made)
	if status != nil {
		dev_update_gpu(r.pid, status, r.gpuAll, r.gpuUsable())
	}

	// ---- the kernel's verdict, on the real machine -------------------------------------------------
	var out UnitVerifyOutcome
	for try := 1; try <= 3; try++ {
		if out.Verified {
			out = devRetryPush(cloneDir, mirror, r.devRepo, r.branch, baseRef, unit.ID, out)
		} else {
			out = devVerifyAndPush(cloneDir, mirror, r.devRepo, r.branch, baseRef, unit.ID, nodeDesc(unit), r.gpuUsable(), base, r.shown(unit))
		}
		if out.Err == nil {
			break
		}
		what := "verify/push error"
		if out.Verified {
			what = "push error after the verification (only the push is retried)"
		}
		log.Printf("dev_from_theorems: %s unit %s %s (try %d/3): %v", r.repo, unit.ID, what, try, out.Err)
		time.Sleep(20 * time.Second)
	}
	if out.Err != nil {
		// the code is still in the mirror; this is the network's failure, not the child's
		return stdout, false, true
	}
	st := out.Status
	st["unit_id"] = unit.ID
	for k, v := range r.requirements(unit).Status() {
		st[k] = v // the reviewer sees what the reference says the unit is, next to what was measured
	}
	if status != nil {
		if f := status["files_written"]; f != "" {
			st["files_written"] = f
		}
	}
	PRFeedbackAdd(r.pid, fmt.Sprintf("- dev[%s] unit %d/%d (%s): compile=%s smoke=%s [kernel, real machine]",
		r.repo, idx+1, total, unit.ID, st["compile"], st["smoke"]))

	if !out.Result.Passed() {
		kind := "compile_fail"
		text := st["compile_error"]
		switch {
		case out.Status["existing_code"] == "true":
			// it wrote nothing and the existing code failed: that failure is the evidence
			kind, text = "existing_code_fails", "the conversation wrote no file, and the code already on the branch for this unit fails on the real machine:\n"+st["compile_error"]+st["smoke_error"]+out.Result.GateText
		case len(out.Changed) == 0 && len(out.Removed) == 0:
			kind = "nothing_written"
			// the only evidence of WHY is what the conversation said: 2026/11/02 unit 2 spent
			// two attempts (27 rounds) writing nothing, and the log held none of it
			words := tailText(stdout, 1500)
			text = "the conversation wrote no file. what it said last:\n" + words
			log.Printf("dev_from_theorems: %s unit %s wrote nothing; its last words: %s", r.repo, unit.ID,
				truncStr(strings.Join(strings.Fields(tailText(stdout, 600)), " "), 600))
		case out.Result.Compile == "pass" && out.Result.Smoke != "pass":
			kind, text = "smoke_fail", st["smoke_error"]
		case out.Result.Gate != "":
			// the scripts passed; a measured requirement did not (unit_gates.go)
			kind, text = out.Result.Gate, out.Result.GateText
		case out.Result.Compile == "missing":
			kind = "missing"
		}
		next := out.Pushed
		if next == "" {
			next = baseRef
		}
		if kind == "nothing_written" {
			plan.NoteSilent(unit.ID)
		} else {
			plan.ClearSilent(unit.ID) // it wrote something: whatever is wrong now is the unit's, not the conversation's
		}
		n := plan.Fail(unit.ID, DevFailure{Kind: kind, Text: text, BaseRef: next})
		log.Printf("dev_from_theorems: %s unit %d/%d id=%s FAILED verification (%s), attempt %d; code on %q",
			r.repo, idx+1, total, unit.ID, kind, n, out.Pushed)
		devPlanSave(r.repo, r.devRepo, r.branch, plan, fmt.Sprintf("plan: %s %s attempt %d", unit.ID, kind, n))
		return stdout, false, false
	}

	// ---- verified on the real machine and pushed; now the reviewer reads the real branch -----------------
	plan.MarkReviewPending(unit.ID, st)
	devPlanSave(r.repo, r.devRepo, r.branch, plan, fmt.Sprintf("plan: unit %s verified, review pending", unit.ID))
	if convID != "" {
		dev_store_conv(convID, r.repo, r.branch, unit.ID, st)
	}
	return r.reviewUnit(unit, idx, total, convID, st, stdout)
}

// reviewUnit runs the review of a verified unit and records its verdict.
func (r *devRun) reviewUnit(unit DevPlanNode, idx, total int, convID string, st map[string]string, stdout string) (string, bool, bool) {
	plan := r.plan
	plan.TakeReviewPending(unit.ID)
	st = reviewStatus(st, unit.ID, r.shown(unit))
	du := devUnitFromNode(unit, r.sections)
	verdict := dev_review_unit(r.pid, r.repo, r.branch, idx+1, total, du, st)
	log.Printf("dev_from_theorems: %s unit %d/%d id=%s review → %s", r.repo, idx+1, total, unit.ID, verdict)
	if strings.HasPrefix(verdict, "PASS") {
		plan.MarkUnitPass(unit)
		if convID != "" {
			plan.SetConv(unit.ID, convID)
		}
		devPlanSave(r.repo, r.devRepo, r.branch, plan, fmt.Sprintf("plan: unit %s PASS", unit.ID))
		return stdout, true, false
	}

	PRFeedbackAdd(r.pid, fmt.Sprintf("- review[%s] unit %d/%d: %s", r.repo, idx+1, total, verdict))
	reason := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(verdict, "FAIL"), ":"))
	kind := "review_fail"
	if strings.Contains(verdict, "proof_failed:") {
		// a proof problem is not a code problem: re-entering re-runs the preflight,
		// which repairs the proof or blocks with its name. Not counted against the unit.
		log.Printf("dev_from_theorems: %s unit %s proof gate FAIL — re-running preflight", r.repo, unit.ID)
		return stdout, false, true
	}
	plan.ClearSilent(unit.ID)
	n := plan.Fail(unit.ID, DevFailure{Kind: kind, Text: reason, BaseRef: r.branch})
	log.Printf("dev_from_theorems: %s unit %d/%d id=%s review FAIL, attempt %d", r.repo, idx+1, total, unit.ID, n)
	devPlanSave(r.repo, r.devRepo, r.branch, plan, fmt.Sprintf("plan: %s review_fail attempt %d", unit.ID, n))
	return stdout, false, false
}

// unitPrompt builds the work conversation's prompt (attempt 1) or the fix
// conversation's prompt (attempt > 1, with the real failure).
func (r *devRun) unitPrompt(unit DevPlanNode, idx, total, attempt int, fail DevFailure, baseRef string) string {
	var p strings.Builder
	plan := r.plan

	p.WriteString(hardwareSection(r.hwInfo))
	if r.plan != nil {
		// the measured requirements name reference files: masked like everything else shown
		p.WriteString(newRefMask(refMaskIdent, r.repo).Replace(r.requirements(unit).Block("(reference)", r.plan.RefSHA)))
		p.WriteString(r.shown(unit).PortChecklist("(reference)", r.plan.RefSHA, unit.ID))
		p.WriteString(r.shown(unit).IntegrationBlock("(reference)", r.plan.RefSHA, unit.ID))
	}

	// prior units: addresses and logs, never text
	var prior []DevPlanNode
	for _, l := range plan.Leaves() {
		if l.ID != unit.ID && plan.UnitPass(l) {
			prior = append(prior, l)
		}
	}
	if len(prior) > 0 {
		p.WriteString(fmt.Sprintf("[PRIOR UNITS — %d passed; code is on branch %s]\n", len(prior), r.branch))
		for _, l := range prior {
			files := nodeDesc(l)
			if len(files) > 120 {
				files = files[:120] + "..."
			}
			addr := l.Conv
			if addr == "" {
				addr = "-"
			}
			p.WriteString(fmt.Sprintf("  unit %s: conv=%s modules=[%s]\n    work log: .einsteinOS/convs/%s.md\n", l.ID, shortID(addr), files, l.ID))
		}
		p.WriteString("\nthe code of every prior unit is on that branch. READ it before you write; do NOT rewrite it unless your theory REQUIRES it.\n")
		p.WriteString("work logs hold each unit's decisions; use grep or view with line ranges, never cat the whole file.\n\n")
	}

	// the theory: it is in the repo (the local mirror is primed with it)
	p.WriteString(fmt.Sprintf("[THEORY — unit %d/%d, id=%s, %d module(s), %d bytes]\n", idx+1, total, unit.ID, len(unit.Modules), plan.UnitTheoryBytes(unit)))
	p.WriteString(fmt.Sprintf("your modules: %s\n", strings.Join(unit.Modules, ", ")))
	p.WriteString("the full theory is at .einsteinOS/theory.md in your repo. find YOUR sections and read them (the theory is the specification):\n")
	p.WriteString("  grep -n '^## \\(FILE\\|MODULE\\):' .einsteinOS/theory.md — where each section starts (its header line ends with the module's name)\n")
	p.WriteString("  sed -n '<start>,<end>p' .einsteinOS/theory.md — read a section\n")
	if unit.Scope != "" {
		p.WriteString(fmt.Sprintf("\n[YOUR SCOPE INSIDE THE MODULE] %s\nthis module was too big for one session and was split. implement ONLY the scope above; other units cover the rest.\n", unit.Scope))
	}
	p.WriteString("\n")

	if tbl := Proofs.Table(SpecTheorems(devUnitFromNode(unit, r.sections).Theory)); tbl != "" {
		p.WriteString("[PROOF STATUS — checked by the kernel against Axle. do NOT re-verify these]\n")
		p.WriteString(tbl)
		p.WriteString("a VERIFIED theorem is a fact you may rely on. for anything UNPROVEN/UNVERIFIED, never claim in code or\n")
		p.WriteString("comments that it is verified; guard the property with a static_assert or runtime check plus a test.\n\n")
	}

	p.WriteString("[YOUR ROLE]\n")
	p.WriteString("you are an EXECUTOR: write the code for your unit, write the two verification scripts, and stop.\n")
	p.WriteString("you do NOT push (the kernel verifies on the real machine and pushes). a separate REVIEW conversation judges the pushed result.\n")
	p.WriteString("do NOT decide whether your work is 'good enough' — do your best and stop.\n\n")

	if attempt > 1 {
		p.WriteString(fmt.Sprintf("[THIS IS FIX ATTEMPT %d for this unit]\n", attempt-1))
		p.WriteString(fmt.Sprintf("the previous attempt failed: %s\n", fail.Kind))
		if fail.Text != "" {
			p.WriteString("the REAL output (from the kernel's run on the real machine, or the reviewer's reason):\n")
			p.WriteString("-----\n" + truncStr(fail.Text, 3500) + "\n-----\n")
		}
		p.WriteString(fmt.Sprintf("the code of the previous attempt is on branch %s. fix THAT code; do not start over.\n", baseRef))
		p.WriteString("fix the reason above first; do not spend tools on unrelated probing.\n\n")
	}
	if len(prior) > 0 || attempt > 1 {
		p.WriteString(fmt.Sprintf("[EXISTING CODE]\nbranch %s is the tree to build on. add your unit to it and keep the WHOLE project building.\n\n", baseRef))
	}

	p.WriteString(fmt.Sprintf("[TASK]\n%s\n\n", r.task))
	p.WriteString(newDevRepoFacts(r.devRepo, baseRef).Block())
	p.WriteString("\n")

	build, test := devVerifyScripts(unit.ID)
	p.WriteString(fmt.Sprintf(dev_unit_instruction, idx+1, total, unit.ID, r.gpuAll, r.gpuUsable(), r.gpuUsable(), build, test, einsteinHelp, DoneInstruction()+DevDoneInToolRound, toolchainPATH()))

	if fb := pr_feedback(r.pid); fb != "" {
		p.WriteString(fmt.Sprintf("\n\n[PRIOR FEEDBACK]\n%s", fb))
	}
	// last, after everything it needs: what it reads, what it writes, the budget
	bytes := 0
	for _, m := range unit.Modules {
		if r.plan != nil {
			bytes += r.plan.Modules[m].Bytes
		}
	}
	p.WriteString("\n\n" + BudgetBlock(devWorkLoad(len(unit.Modules), bytes), RoundBudgetOf(devUnitCondition())))
	return maskShownPrompt(r.repo, p.String())
}

const dev_unit_instruction = `
[INCREMENTAL BUILD — unit %[1]d of %[2]d, id=%[3]s]
you are building ONE unit of a multi-unit project. each unit is its own conversation.

[GPU CONFIGURATION]
all GPUs on this machine: %[4]s
usable GPUs: %[5]s
CUDA_VISIBLE_DEVICES=%[6]s

[WHAT YOU HAVE]
- the THEORY for your unit (LAYER 1 math + LAYER 2 form templates) in .einsteinOS/theory.md
- the code of the prior units, on the dev repo branch
- you do NOT have the original source code. this is intentional.

[CRITICAL — DO NOT]
- do NOT clone, fetch, or read ANY repository other than the dev repo
- do NOT search for or access the reference source code in any way
- do NOT use the reference project's namespace, directory structure, or file names
- the theory is ABSTRACT — it describes WHAT to build, not HOW the reference did it
- if the theory mentions paths from the reference, IGNORE those paths

[WHERE YOUR FILES GO]
work in the repo directory shown under [DEV REPO]. use the dev repo's existing layout
(include/<project>/, src/<project>/, tests/); if it has none yet, create one that fits YOUR project name.

[THE KERNEL VERIFIES YOU — WRITE THESE TWO SCRIPTS]
%[7]s
%[8]s
- build script: compiles the WHOLE project with the real toolchain (the compilers are in %[11]s on this machine).
  it must exit 0 only if everything builds.
- test script: runs the tests for YOUR unit. it must exit 0 only if every test passes; a failing check must make it exit non-zero.
  the tests must check the INVARIANTS of the theory (the LAYER 1 statements), not just that the code runs.
the kernel runs both on the real machine after you stop, with EINSTEINOS_GPUS and CUDA_VISIBLE_DEVICES set to the usable GPUs.
a unit with no test script, or with tests that cannot fail, will be rejected by the reviewer.
run them yourself first: the results you see for commands run on this machine are the real ones.

[IF YOUR UNIT IS TOO BIG]
if you find your part is too large to finish well in one session, you may hand a self-contained piece to another conversation
(see below) — give it a narrow scope (specific files), because it edits the same tree as you. do this EARLY, before you have
written most of it yourself. do not start conversations for small things.
when you have many small questions to decide, ask ONE judge conversation all of them at once.

[CONVERSATION COMMANDS — run them in bash]
%[9]s

[THEORY → CODE]
LAYER 2 form templates use placeholder identifiers:
  prjname, adj, Obj, typenameType, objtype, argname
fill them with YOUR project's names. you are creating NEW code, not copying.
the math in LAYER 1 defines WHAT the code must do. HOW you implement it
(data structures, control flow, error handling) is YOUR choice for YOUR hardware.

[WHEN YOU ARE DONE]
write a short summary (what you built, which files). do NOT git push. do NOT claim compile or test results — the kernel measures them.
%[10]s`

// devSettleSleep: how long to wait between looks at a conversation the remote
// may still be running. A variable so a test does not sleep.
var devSettleSleep = 30 * time.Second

// devCollectLite is what the dev flow needs from a finished conversation: its
// last text, any measurement it printed itself (the p2p report), and whether it
// did anything at all. It does NOT judge compile/test (the kernel does, from
// exit codes) and it never polls for a status block or sends "continue" —
// dev_collect_child did both, and a child that is told to stop without claiming
// a result would have sat in its 3-minute poll loop for up to 45 minutes and
// then been told to go and push, which is no longer its job.
func devCollectLite(childPid int64, repo string) (string, map[string]string) {
	stdout := GetResult(childPid)
	status := parse_last_status(stdout)
	if status == nil {
		status = map[string]string{}
	}
	delete(status, "compile") // the child's word on compile is not an input
	delete(status, "smoke")
	if files := dev_child_files(childPid); len(files) > 0 {
		status["files_written"] = strings.Join(files, ",")
	}

	// an SSE drop ends our wait while the remote may still be working
	if m := getMeta(childPid); m != nil && m.ConvID != "" {
		for i := 0; i < 40; i++ {
			busy, err := remoteBusy(m.ConvID)
			if err != nil || !busy {
				break
			}
			log.Printf("dev_collect: %s conversation %s still running remotely, waiting", repo, shortID(m.ConvID))
			time.Sleep(devSettleSleep)
		}
		stdout = GetResult(childPid)
	}

	apiErr := strings.Contains(stdout, "[API Error]") || strings.Contains(stdout, ModelDowngradeMarker) ||
		strings.Contains(stdout, "http request:") || strings.Contains(stdout, "TLS handshake timeout") ||
		strings.Contains(stdout, "net/http:")
	if apiErr || (status["files_written"] == "" && len(strings.TrimSpace(stdout)) < 100) {
		status["compile"] = "no_response"
	}
	return stdout, status
}

// devUnitCondition is how a dev unit conversation ends; its prompt states the same
// round budget (RoundBudgetOf).
func devUnitCondition() TerminationCondition {
	return WithDoneMarker(OrConditions(
		&ConsecutiveTextRounds{Threshold: 4},
		&MaxRounds{Limit: 30},
	))
}

// reviewStatus adds what the kernel measured about the unit's port to the status the
// reviewer reads: the reference files, the port map, and that every definition was found.
// 11/04 the reviewers of u89be81de0b and u0e60a9f272 judged the code against the theory only,
// never against the reference files it was meant to port.
func reviewStatus(st map[string]string, unitID string, req UnitRequirements) map[string]string {
	out := map[string]string{}
	for k, v := range st {
		out[k] = v
	}
	if len(req.Symbols) > 0 {
		out["port_map"] = PortMapPath(unitID)
		out["port"] = fmt.Sprintf("every one of the %d definitions has a counterpart defined where the map says (measured by the kernel)", len(req.Symbols))
	}
	return out
}
