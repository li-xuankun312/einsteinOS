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
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
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
const devNothingToBuildFmt = "dev_from_theorems: %[1]s unit %[2]d/%[3]d id=%[4]s has nothing of its own to build (definitions of its sources: %[5]d, all ported by other units; modules with no reference file: %[6]v; no GPU source or ISA path of its own) — closed without a conversation"

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
	// the GPU and ISA requirements follow the files whose definitions the unit ports: 11/05
	// u0868e10073 owned none of its 90 and still had to write and run GPU code for its
	// sources' .cu files, which other units port
	if len(req.Symbols) != len(r.rawRequirements(unit).Symbols) {
		ownedFile := map[string]bool{}
		for _, sym := range own {
			ownedFile[sym.File] = true
		}
		var gpu []string
		for _, f := range req.GPUSources {
			if ownedFile[f] {
				gpu = append(gpu, f)
			}
		}
		req.GPUSources = gpu
		flagFiles := map[string][]string{}
		isa := map[string]bool{}
		for f, fl := range req.FlagFiles {
			if ownedFile[f] {
				flagFiles[f] = fl
				for _, x := range fl {
					isa[x] = true
				}
			}
		}
		req.FlagFiles, req.ISAFlags = flagFiles, nil
		for x := range isa {
			req.ISAFlags = append(req.ISAFlags, x)
		}
		sort.Strings(req.ISAFlags)
	}
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
		tests, excluded, cuda := refIntegrationTestsWithExcluded(r.repo, sha)
		req = UnitRequirements{Tests: tests, RequireGPU: cuda, Repo: r.repo, SHA: sha}
		// the tests must be the reference's tests: every definition of the files the reference
		// builds them from is the unit's to port (11/06 uintegration01529aaa82b9 passed 28 tests
		// named like the reference's, each a wrapper running other units' invariant checks)
		req.Sources, req.Symbols = integrationTestSymbols(refBareDir(r.repo), sha, tests)
		// and the reference's own benchmarks: their counterparts are built and run by the port,
		// so the files the reference builds them from are the unit's to port too
		if b, err := evalBenchmarks(refBareDir(r.repo), sha); err == nil {
			req.Benches = b
			req.Sources, req.Symbols = withBenchSources(refBareDir(r.repo), sha, req.Sources, req.Symbols, b)
		}
		log.Printf("dev_requirements: %s integration unit %s: %d benchmark(s) of the reference to run here", r.repo, unit.ID, len(req.Benches))
		log.Printf("dev_requirements: %s integration unit %s: %d registered test(s) of the reference required, %d not (they need what the reference does not contain); GPU required=%v", r.repo, unit.ID, len(tests), len(excluded), cuda)
		for name, why := range excluded {
			log.Printf("dev_requirements:   %s not required: %s", name, why)
		}
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
	devMaskSweep(repo, devRepo, branch)

	plan := dev_get_plan(pid, repo, devRefSHA(repo, step.SHA), theory, branch)
	if err := devLeaseCheck(plan.Holder, thisHolder(time.Now()), time.Now(), os.Getenv(devTakeLeaseEnv) != "", holderRunning); err != nil {
		log.Printf("dev_from_theorems: %s %s: %v", repo, branch, err)
		PRFeedbackAdd(pid, "- dev_from_theorems: "+err.Error())
		return "", err
	}

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
	// the measurements below grep and read the reference commit; a blobless clone that still
	// lacks some of its blobs would fetch them one at a time (11/06: 35 minutes between the
	// plan and the first measured unit, nothing pushed in between)
	prefetchCommitBlobs(refBareDir(repo), plan.RefSHA)
	measureStart := time.Now()
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
		log.Printf("dev_requirements: %s %d unit(s) measured against the reference in %s", repo, len(plan.Leaves()), time.Since(measureStart).Round(time.Second))
		if n := plan.AddPortUnits(all, covered); n > 0 {
			log.Printf("dev_plan: %s reference has %d code files; theory units cover %d; %d port unit(s) added for the rest", repo, len(all), len(covered), n)
			devPlanSave(repo, devRepo, branch, plan, fmt.Sprintf("plan: %d port units", n))
		}
	}

	var lastStdout string
	staleRerun := map[string]bool{} // units after which the integration was run again (integrationStale)
	// every loop turn is one attempt at one unit (or one split). The ceiling is
	// a backstop against a bug, not a limit anyone should reach.
	for turn := 0; turn < 400; turn++ {
		leaves := plan.Leaves()
		idx := nextUnitIndex(plan, leaves)
		if idx < 0 {
			if dir, err := devRepoLocalDir(devRepo); err == nil {
				// once per unit: an integration run that passes without a commit of its own leaves
				// the branch as it was, and would be judged stale again without end
				if after := integrationStale(dir, branch, plan, leaves); after != "" && !staleRerun[after] {
					staleRerun[after] = true
					plan.withdrawPass(plan.IntegrationUnit.ID)
					log.Printf("dev_from_theorems: %s unit %s committed after the integration unit %s's last verified commit — the integration runs again", repo, after, plan.IntegrationUnit.ID)
					devPlanSave(repo, devRepo, branch, plan, fmt.Sprintf("plan: %s runs again after unit %s", plan.IntegrationUnit.ID, after))
					continue
				}
			}
			break
		}
		unit := leaves[idx]

		// a stored review failure whose files belong to other units is theirs, not this
		// unit's: those units are reopened and this unit's attempts start again. 11/08 unit
		// pf7b804cf95 came back from the plan with three such failures already counted, so
		// the run asked to split it before it could run once and stopped on a port unit
		// that has no modules to split.
		if blames := resumeStoredReviewFailure(plan, unit, branch, run.reviewBlame); len(blames) > 0 {
			log.Printf("dev_from_theorems: %s unit %s: its stored review failure is about files of %s — those are reopened and unit %s starts again",
				repo, unit.ID, strings.Join(blames, " "), unit.ID)
			devPlanSave(repo, devRepo, branch, plan, fmt.Sprintf("plan: %s reopened, unit %s starts again", strings.Join(blames, " "), unit.ID))
			continue
		}

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
				lf := deferredFailure(plan.LastFailure[unit.ID])
				plan.MarkDeferred(unit.ID, lf)
				log.Printf("dev_from_theorems: %s unit %s used its %d fix attempts and has nothing to split — the run goes on and its files are left to the integration unit; last review: %s; %s",
					repo, unit.ID, plan.FixAttempts[unit.ID], truncStr(lf.Text, 300), lf.Attribution)
				devPlanSave(repo, devRepo, branch, plan, fmt.Sprintf("plan: unit %s deferred to the integration unit", unit.ID))
				continue
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
		if lf := plan.LastFailure[unit.ID]; !passed && !requeue && gateIsMachine(lf.Kind) {
			msg := fmt.Sprintf("unit %s (%s): %s. this is a fact about the machine, not the unit; the code is kept on %s; fix the machine to continue",
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

	// every unit passed verification on the real machine and review on the real branch,
	// except the deferred ones, whose files the integration unit's end-to-end run answers for
	if ids := plan.DeferredUnits(); len(ids) > 0 {
		for _, id := range ids {
			log.Printf("dev_from_theorems: %s unit %s was deferred; the last thing its review said: %s; %s", repo, id, truncStr(plan.Deferred[id].Text, 300), plan.Deferred[id].Attribution)
		}
		PRFeedbackAdd(pid, fmt.Sprintf("- dev_from_theorems: %d unit(s) deferred to the integration unit: %s", len(ids), strings.Join(ids, " ")))
	}
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
	// a unit another unit's review faulted starts from that verdict, as a fix attempt, on
	// the branch as it stands (its own code is already there)
	if f, ok := plan.TakeReopen(unit.ID); ok {
		fail = f
		plan.LastFailure[unit.ID] = f
		plan.FixAttempts[unit.ID] = 1
		log.Printf("dev_from_theorems: %s unit %s runs again on a later review's verdict: %s", r.repo, unit.ID, truncStr(f.Text, 300))
		devPlanSave(r.repo, r.devRepo, r.branch, plan, fmt.Sprintf("plan: unit %s reopened", unit.ID))
	}
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

	if unit.Integration {
		raw := r.rawRequirements(unit)
		RegisterIntegrationEval(unit.ID, evalIntegration(r.pid, r.repo, raw.SHA, r.devRepo, unit.ID, raw.Benches, newRefMask(refMaskIdent, r.repo).Replace))
		defer UnregisterIntegrationEval(unit.ID)
	}
	cond := devUnitCondition()
	var child int64
	var pre *preVerifyResult // the last verification run at a done line (dev_preverify.go)
	var convID string
	var status map[string]string
	for try := 1; try <= 5; try++ {
		var ferr error
		waitRoleEnded(role, r.repo, unit.ID)
		RegisterPreVerify(role, func() (bool, string) {
			if changed, removed, err := treeDelta(mirror, base); err == nil && pre.sameRejected(deltaDigest(mirror, changed, removed)) {
				log.Printf("dev_preverify: %s unit %s (%s): the files are those of the last rejected verification (gate=%q) — its result is sent back, not measured again",
					r.repo, unit.ID, role, pre.out.Result.Gate)
				return false, preVerifyReport(pre.out)
			}
			o := devVerifyOnly(cloneDir, mirror, r.branch, baseRef, unit.ID, r.gpuUsable(), base, r.shown(unit))
			passed := o.Err == nil && o.Verified && o.Result.Passed()
			changed, removed, _ := treeDelta(mirror, base)
			pre = &preVerifyResult{ok: passed, digest: deltaDigest(mirror, changed, removed), out: o}
			end := preverifyMayEnd(passed, o.Result.Gate)
			log.Printf("dev_preverify: %s unit %s (%s): compile=%s smoke=%s gate=%q — %s", r.repo, unit.ID, role,
				o.Result.Compile, o.Result.Smoke, o.Result.Gate, map[bool]string{true: "the conversation may end", false: "sent back to the conversation"}[end])
			if end {
				return true, ""
			}
			return false, preVerifyReport(o)
		})
		child, ferr = ForkWithTermination(prompt, ".", role, cond)
		if ferr != nil {
			UnregisterPreVerify(role)
			log.Printf("dev_from_theorems: %s unit %s fork failed (try %d/5): %v", r.repo, unit.ID, try, ferr)
			continue
		}
		// the fork returns at once and the conversation runs until Wait returns: the check
		// stays registered for all of it (11/05 07:14:08 dev_fix_1 of u12d8dd5102 ended with no
		// check at all — it had been unregistered the moment the fork returned)
		Wait(child)
		waitAbandoned(child, r.repo, unit.ID)
		UnregisterPreVerify(role)
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
			if changed, removed, _ := treeDelta(mirror, base); try == 1 && pre != nil && pre.ok && pre.digest == deltaDigest(mirror, changed, removed) {
				// the tree the conversation ended on is the one the kernel just verified
				log.Printf("dev_verify: unit %s: the tree is the one verified at the done line — that verification is used", unit.ID)
				out = pre.out
				out.msg = fmt.Sprintf("unit %s: %s [compile=%s smoke=%s gate=%s kernels=%s, verified by the kernel]", unit.ID, truncStr(nodeDesc(unit), 120), out.Result.Compile, out.Result.Smoke, gateOrNone(out.Result.Gate), out.Result.Test.LaunchCount())
				out = devRetryPush(cloneDir, mirror, r.devRepo, r.branch, baseRef, unit.ID, out)
			} else {
				out = devVerifyAndPush(cloneDir, mirror, r.devRepo, r.branch, baseRef, unit.ID, nodeDesc(unit), r.gpuUsable(), base, r.shown(unit))
			}
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
	return r.reviewUnit(unit, idx, total, convID, st, stdout)
}

// reviewUnit runs the review of a verified unit and records its verdict.
func (r *devRun) reviewUnit(unit DevPlanNode, idx, total int, convID string, st map[string]string, stdout string) (string, bool, bool) {
	plan := r.plan
	plan.TakeReviewPending(unit.ID)
	st = reviewStatus(st, unit.ID, r.shown(unit))
	du := devUnitFromNode(unit, r.sections)
	if convID != "" {
		st["dev_conv"] = convID
	}
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
	// the files the verdict names decide whose failure it is (review_owner.go)
	if blames := r.reviewBlame(unit, verdict); len(plan.ReopenableUnits(blames)) > 0 {
		blames = plan.ReopenableUnits(blames)
		copies := ""
		if dir, err := devRepoLocalDir(r.devRepo); err == nil {
			copies = reviewCopiesText(r.branch, definitionCopies(dir, r.branch, verdict))
		}
		for _, id := range blames {
			plan.MarkReopen(id, DevFailure{Kind: "review_fail", Text: fmt.Sprintf(reopenReasonFmt, reason, unit.ID), BaseRef: r.branch, Copies: copies})
		}
		log.Printf("dev_from_theorems: %s unit %s: the review faulted files of %s — %s reopened, unit %s is not held to answer for it",
			r.repo, unit.ID, strings.Join(blames, " "), strings.Join(blames, " "), unit.ID)
		plan.MarkUnitPass(unit)
		if convID != "" {
			plan.SetConv(unit.ID, convID)
		}
		devPlanSave(r.repo, r.devRepo, r.branch, plan, fmt.Sprintf("plan: unit %s PASS, %s reopened by its review", unit.ID, strings.Join(blames, " ")))
		return stdout, true, false
	}
	kind := "review_fail"
	if strings.Contains(verdict, "proof_failed:") {
		// a proof problem is not a code problem: re-entering re-runs the preflight,
		// which repairs the proof or blocks with its name. Not counted against the unit.
		log.Printf("dev_from_theorems: %s unit %s proof gate FAIL — re-running preflight", r.repo, unit.ID)
		return stdout, false, true
	}
	plan.ClearSilent(unit.ID)
	failure := DevFailure{Kind: kind, Text: reason, BaseRef: r.branch}
	if dir, err := devRepoLocalDir(r.devRepo); err == nil {
		failure.Copies = reviewCopiesText(r.branch, definitionCopies(dir, r.branch, verdict))
	}
	n := plan.Fail(unit.ID, failure)
	log.Printf("dev_from_theorems: %s unit %d/%d id=%s review FAIL, attempt %d", r.repo, idx+1, total, unit.ID, n)
	devPlanSave(r.repo, r.devRepo, r.branch, plan, fmt.Sprintf("plan: %s review_fail attempt %d", unit.ID, n))
	return stdout, false, false
}

// unitPrompt: the shell (Bagu) with this unit inserted. Attempt 1 and every fix attempt are
// the same shell; a fix attempt differs only by what is inserted — the failure the kernel
// measured or the review's words go first in the content, as the user's review did.
func (r *devRun) unitPrompt(unit DevPlanNode, idx, total, attempt int, fail DevFailure, baseRef string) string {
	plan := r.plan
	var c strings.Builder
	if attempt > 1 {
		if fail.Text != "" {
			c.WriteString(truncStr(fail.Text, 3500) + "\n")
		}
		if fail.Copies != "" {
			c.WriteString(fail.Copies + "\n")
		}
		c.WriteString("\n")
	}
	c.WriteString(hardwareSection(r.hwInfo))
	if r.plan != nil {
		// the measured requirements name reference files: masked like everything else shown
		c.WriteString(newRefMask(refMaskIdent, r.repo).Replace(r.requirements(unit).Block("(reference)", r.plan.RefSHA)))
		c.WriteString(r.shown(unit).PortChecklist("(reference)", r.plan.RefSHA, unit.ID))
		c.WriteString(r.shown(unit).IntegrationBlock("(reference)", r.plan.RefSHA, unit.ID))
	}
	passed := 0
	for _, l := range plan.Leaves() {
		if l.ID != unit.ID && plan.UnitPass(l) {
			files := nodeDesc(l)
			if len(files) > 120 {
				files = files[:120] + "..."
			}
			addr := l.Conv
			if addr == "" {
				addr = "-"
			}
			c.WriteString(fmt.Sprintf(devPriorUnitFmt, l.ID, shortID(addr), files))
			passed++
		}
	}
	c.WriteString(fmt.Sprintf(devTheoryFmt, idx+1, total, unit.ID, len(unit.Modules), plan.UnitTheoryBytes(unit), strings.Join(unit.Modules, ", ")))
	if unit.Scope != "" {
		c.WriteString(fmt.Sprintf(devScopeFmt, unit.Scope))
	}
	if tbl := Proofs.Table(SpecTheorems(devUnitFromNode(unit, r.sections).Theory)); tbl != "" {
		c.WriteString("[PROOF STATUS]\n" + tbl)
	}
	c.WriteString(fmt.Sprintf("[TASK]\n%s\n\n", r.task))
	c.WriteString(newDevRepoFacts(r.devRepo, baseRef).Block())
	build, test := devVerifyScripts(unit.ID)
	c.WriteString(fmt.Sprintf(dev_unit_instruction, idx+1, total, unit.ID, r.gpuAll, r.gpuUsable(), r.gpuUsable(), build, test, einsteinHelp, DoneInstruction()+DevDoneInToolRound, toolchainPATH()))
	if fb := pr_feedback(r.pid); fb != "" {
		c.WriteString(fmt.Sprintf("\n[PRIOR FEEDBACK]\n%s\n", fb))
	}
	bytes := 0
	for _, m := range unit.Modules {
		if r.plan != nil {
			bytes += r.plan.Modules[m].Bytes
		}
	}
	c.WriteString("\n" + BudgetBlock(devWorkLoad(len(unit.Modules), bytes), RoundBudgetOf(devUnitCondition())))

	what := fmt.Sprintf(devWhatFmt, passed, idx+1, total, unit.ID, attempt)
	if attempt > 1 {
		what += fmt.Sprintf(devWhatFailFmt, fail.Kind)
	}
	commits := len(r.requirements(unit).Symbols)
	if commits == 0 {
		commits = len(unit.Modules)
	}
	return maskShownPrompt(r.repo, Bagu(fmt.Sprintf(devWhereFmt, baseRef), what, c.String(), fmt.Sprint(commits), r.devRepo))
}

// inserted into the shell for a dev unit: %[1]s the branch to build on
const devWhereFmt = "分支 %[1]s 上最近的提交"

// %[1]d units passed, unit %[2]d of %[3]d, id %[4]s, attempt %[5]d
const devWhatFmt = "有 %[1]d 个 unit 已经通过，现在是 unit %[2]d/%[3]d（%[4]s）第 %[5]d 次"

// %[1]s the kind of the last failure
const devWhatFailFmt = "，上一次 %[1]s"

// a passed unit: %[1]s id, %[2]s its conversation, %[3]s its modules
const devPriorUnitFmt = "[PRIOR UNIT] %[1]s conv=%[2]s modules=[%[3]s]\n"

// the unit's theory: %[1]d/%[2]d unit, %[3]s id, %[4]d modules, %[5]d bytes, %[6]s module names
const devTheoryFmt = "[THEORY — unit %[1]d/%[2]d, id=%[3]s, %[4]d module(s), %[5]d bytes] .einsteinOS/theory.md, its sections headed '## FILE: <path>' or '## MODULE: <name>': %[6]s\n"

// %[1]s the part of a split module that is this unit's
const devScopeFmt = "[SCOPE] %[1]s\n"

// the kernel's contracts with a dev unit, as facts: %[1]d/%[2]d/%[3]s unit, %[4]s all GPUs,
// %[5]s/%[6]s usable, %[7]s/%[8]s the scripts it runs, %[9]s the commands, %[10]s the done
// line, %[11]s where the compilers are.
const dev_unit_instruction = `[UNIT %[1]d/%[2]d id=%[3]s]
GPUs: %[4]s; usable: %[5]s (CUDA_VISIBLE_DEVICES=%[6]s)
build: %[7]s
test: %[8]s
the kernel runs both on the real machine, with EINSTEINOS_GPUS and CUDA_VISIBLE_DEVICES set to the usable GPUs; their exit codes are compile= and smoke=.
the compilers are in %[11]s on this machine.
LAYER 2 placeholders: prjname, adj, Obj, typenameType, objtype, argname
[CONVERSATION COMMANDS]
%[9]s
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

// reopenReasonFmt: a reopened unit's failure — what the reviewer said (%[1]s) and which unit
// it was reviewing (%[2]s).
const reopenReasonFmt = "%[1]s (said by the review of unit %[2]s, whose files these are not)"

// reviewBlame: the other units whose files the verdict named. A unit is blamed only for files
// it owns and only while this unit owns none of the named ones, so a verdict about its own
// code stays its own.
func (r *devRun) reviewBlame(unit DevPlanNode, verdict string) []string {
	dir, err := devRepoLocalDir(r.devRepo)
	if err != nil {
		return nil
	}
	sources := map[string][]string{}
	inPlan := map[string]bool{}
	for _, l := range r.plan.Leaves() {
		inPlan[l.ID] = true
		if req := r.rawRequirements(l); len(req.Sources) > 0 {
			sources[l.ID] = req.Sources
		}
	}
	blames := blameOnBranch(dir, r.branch, unit.ID, verdict, sources, inPlan)
	if dropped := outsidePlan(blameOnBranch(dir, r.branch, unit.ID, verdict, sources, nil), inPlan); len(dropped) > 0 {
		log.Printf(reviewOutsidePlanFmt, r.repo, unit.ID, strings.Join(dropped, " "), map[bool]string{true: "the verdict is unit " + unit.ID + "'s", false: "units of this plan: " + strings.Join(blames, " ")}[len(blames) == 0])
	}
	return blames
}

// reviewOutsidePlanFmt: said whenever a verdict names code that port maps of earlier plans give
// to units that will not run — 11/10 14:36 it went unsaid, and the bug with it.
const reviewOutsidePlanFmt = "dev_from_theorems: %[1]s unit %[2]s: the review faulted files the port maps give to %[3]s, units of earlier plans that do not run — %[4]s"

// outsidePlan: the ids not in inPlan.
func outsidePlan(ids []string, inPlan map[string]bool) []string {
	var out []string
	for _, id := range ids {
		if !inPlan[id] {
			out = append(out, id)
		}
	}
	return out
}

// blameOnBranch: the units other than unitID that own what verdict faults, measured on the
// dev branch in the clone at dir — by the files it names, else by the definitions it names.
// Nobody, when unitID owns any of it. Only units of this plan (inPlan; nil: every port map)
// are owners: the dev branch keeps the port maps of earlier commits' plans, whose units never
// run again — 11/10 14:36 unit p81030368e8's review faulted src/ilu_kv_q8.cu, owned only by
// u02c65ed515, u649794726a, ub6cd72ef49 and uebd9b81357 of earlier plans; those four were
// "reopened", none ran, and p81030368e8 passed with the bug in place.
func blameOnBranch(dir, branch, unitID, verdict string, sources map[string][]string, inPlan map[string]bool) []string {
	owners := planOwners(reviewOwners(verdict, portMapOwners(dir, branch), sources), inPlan)
	if len(owners) == 0 {
		owners = planOwners(reviewSymbolOwners(verdict, portMapEntries(dir, branch), func(f string) string {
			b, _ := exec.Command("git", "-C", dir, "show", branch+":"+f).Output()
			return string(b)
		}), inPlan)
	}
	for _, units := range owners {
		for _, u := range units {
			if u == unitID {
				return nil
			}
		}
	}
	return reviewBlames(unitID, owners)
}

// waitAbandoned: a conversation Wait gave up on is still the unit's conversation — the next
// one is not started beside it. 11/10 18:31 and 21:15 the integration unit's conversation
// was given up on while it ran, and fresh ones (b7563fcf, e8e9ed00) were started; from 22:39
// three conversations of the unit changed its files at once.
func waitAbandoned(child int64, repo, unitID string) {
	m := getMeta(child)
	if m == nil || m.Done == nil {
		return
	}
	metaMu.Lock()
	abandoned := m.Abandoned
	metaMu.Unlock()
	if !abandoned {
		return
	}
	for {
		select {
		case <-m.Done:
			log.Printf("dev_from_theorems: %s unit %s: the conversation given up on (pid %d) has ended", repo, unitID, child)
			return
		case <-time.After(waitAbandonedReport):
			log.Printf("dev_from_theorems: %s unit %s: waiting for the conversation given up on (pid %d) to end before another is started", repo, unitID, child)
		}
	}
}

// waitRoleEnded: the check of role is registered only when no conversation of role runs. The
// check is kept per role; 11/10 the verification that decided the integration unit at 23:33
// was run from 9a378854's done line through the check e8e9ed00's start had registered, on a
// tree three conversations had written.
func waitRoleEnded(role, repo, unitID string) {
	for {
		pid, alive := liveRole(role)
		if !alive {
			return
		}
		m := getMeta(pid)
		if m == nil || m.Done == nil {
			return
		}
		log.Printf("dev_from_theorems: %s unit %s: a conversation of role %s (pid %d) still runs — its check stays until it ends", repo, unitID, role, pid)
		select {
		case <-m.Done:
		case <-time.After(waitAbandonedReport):
		}
	}
}

// waitAbandonedReport: how often waitAbandoned says it is still waiting.
var waitAbandonedReport = 10 * time.Minute

// integrationStale: the unit of leaves whose commit on branch is newer than the integration
// unit's last commit, or "" (also when the integration unit has no commit). Read from the
// branch, it holds whatever code recorded the passes: 11/11 the integration unit's last
// commit was 3b95c93 (11/10 23:34); units uf4e6ff5513, uf9f69c2c43 and ua9684a588f committed
// after it, under a kernel that did not withdraw the integration's pass, and the pipeline
// completed at 05:48 with the integration judged on the tree before their fixes.
func integrationStale(dir, branch string, plan *DevPlan, leaves []DevPlanNode) string {
	if plan.IntegrationUnit == nil {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "log", "--first-parent", "--format=%s", branch).Output()
	if err != nil {
		return ""
	}
	ids := map[string]bool{}
	for _, l := range leaves {
		if !l.Integration {
			ids[l.ID] = true
		}
	}
	var newer string
	for _, subj := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(subj, "unit ") {
			continue
		}
		id := strings.TrimSuffix(strings.Fields(subj)[1], ":")
		if id == plan.IntegrationUnit.ID {
			return newer
		}
		if newer == "" && ids[id] {
			newer = id
		}
	}
	return ""
}

// planOwners: owners with every unit outside inPlan dropped, and files left with no owner
// dropped (nil inPlan keeps every unit).
func planOwners(owners map[string][]string, inPlan map[string]bool) map[string][]string {
	if inPlan == nil {
		return owners
	}
	out := map[string][]string{}
	for f, units := range owners {
		for _, u := range units {
			if inPlan[u] {
				out[f] = append(out[f], u)
			}
		}
	}
	return out
}

// resumeStoredReviewFailure: a unit that comes back from the plan with a review failure
// stored against it, whose files blame measures as other units', has those units reopened
// with that verdict and its own attempts and failure cleared, so it starts again instead of
// being asked to split. It returns the units reopened (none: the plan is left as it was).
// dev_from_theorems runs it before the split question; it is a function so that what the
// loop does is what a test runs — 11/08 the test of this step (6dfd2f13) wrote the same
// steps out itself and passed on the code before the fix.
func resumeStoredReviewFailure(plan *DevPlan, unit DevPlanNode, branch string, blame func(DevPlanNode, string) []string) []string {
	lf := plan.LastFailure[unit.ID]
	if lf.Kind != "review_fail" || plan.FixAttempts[unit.ID] == 0 {
		return nil
	}
	blames := plan.ReopenableUnits(blame(unit, lf.Text))
	if len(blames) == 0 {
		return nil
	}
	for _, id := range blames {
		plan.MarkReopen(id, DevFailure{Kind: "review_fail", Text: fmt.Sprintf(reopenReasonFmt, lf.Text, unit.ID), BaseRef: branch})
	}
	delete(plan.FixAttempts, unit.ID)
	delete(plan.LastFailure, unit.ID)
	return blames
}

// nextUnitIndex: the unit to run now — the first that has not passed and was not reopened,
// and only when none of those is left, the first reopened one. A unit another unit's review
// faulted is extra work queued behind what was already queued, not a step back: 11/08 the
// units pf7b804cf95's review faulted (u41e2bf114f, u4decb4e657) sit before it in the plan.
//
// The integration unit is not one of those: it runs the port on the model end to end, so it
// waits for every reopened unit. 11/10 16:01 it started while units
// uf4e6ff5513, uf9f69c2c43, ua9684a588f and u0063babba8 were reopened, and judged the code
// before their fixes.
func nextUnitIndex(plan *DevPlan, leaves []DevPlanNode) int {
	reopened, integration := -1, -1
	for i, l := range leaves {
		if plan.UnitPass(l) {
			continue
		}
		if l.Integration {
			if integration < 0 {
				integration = i
			}
			continue
		}
		if !plan.Reopened(l.ID) {
			return i
		}
		if reopened < 0 {
			reopened = i
		}
	}
	if reopened >= 0 {
		return reopened
	}
	return integration
}
