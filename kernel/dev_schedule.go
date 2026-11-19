package kernel

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// The units of a dev run used to run one after another, each waiting for the one before even
// when nothing linked them: 11/19 the port units of separate directories took 20–30 minutes
// each, one at a time. A unit now starts as soon as nothing measured links it to a unit that
// runs (dev_independence.go). How many run at once is not fixed here: every conversation's
// requests wait in claudeweb's request window (10 per 10 minutes, api_window.go), so a unit
// whose conversation has been quiet for longer than the window takes none of it. The real
// machine still verifies one unit at a time (onTheRealMachine). A unit marked to run alone
// (its delta met another unit's commit, branchMovedUnder) and the integration unit, which
// runs the whole port end to end, run with no other unit beside them; the integration unit
// starts only when no other unit is left.

// unitResult: what one attempt at a unit came back with.
type unitResult struct {
	unit            DevPlanNode
	stdout          string
	passed, requeue bool
}

// nextUnitIndices: the units still to run, in the order nextUnitIndex takes them — units not
// reopened first, then reopened ones, then the integration unit.
func nextUnitIndices(plan *DevPlan, leaves []DevPlanNode) []int {
	var fresh, reopened, integration []int
	for i, l := range leaves {
		switch {
		case plan.UnitPass(l):
		case l.Integration:
			integration = append(integration, i)
		case plan.Reopened(l.ID):
			reopened = append(reopened, i)
		default:
			fresh = append(fresh, i)
		}
	}
	return append(append(fresh, reopened...), integration...)
}

// schedule runs the plan's units, each attempt in a goroutine of its own, as many at once as
// are independent. It returns the last text a unit's conversation ended on, the error that
// stopped the run, and whether infrastructure failed so the step is to be pushed again. The
// caller holds nothing; the run's lock (r.mu) is held here except while waiting on a unit.
func (r *devRun) schedule(theory string, staleRerun map[string]bool, attempt func(DevPlanNode, int, int) (string, bool, bool)) (lastStdout string, stopErr error, requeued bool) {
	plan := r.plan
	running := map[string]unitFootprint{}
	alone := false // a unit that runs alone is running
	results := make(chan unitResult)
	branchFiles := func(unitID string) []string {
		dir, err := devRepoLocalDir(r.devRepo)
		if err != nil {
			return nil
		}
		return devUnitPriorFiles(dir, r.branch, unitID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	turn := 0
	for {
		// start every unit that may run now
		for stopErr == nil && !requeued && !alone && turn < 400 {
			if d := ProxyQuotaSpent(); d > 0 && len(running) > 0 {
				break // the proxy answers no one until then; the units running hold what they have
			} else if d > 0 {
				log.Printf("dev_schedule: %s the proxy's quota for this account is spent for %v — no unit is started until it is back", r.repo, d.Round(time.Second))
				r.whileWaiting(func() { quotaSleep(d + time.Second) })
				continue
			}
			leaves := plan.Leaves()
			started, prepared := false, false
			for _, idx := range nextUnitIndices(plan, leaves) {
				unit := leaves[idx]
				if _, ok := running[unit.ID]; ok {
					continue
				}
				if r.accountRefusedBeside(len(running)) {
					break
				}
				soloUnit := unit.Integration || r.alone[unit.ID]
				if unit.Integration && (len(running) > 0 || len(nextUnitIndices(plan, leaves)) > 1) {
					continue // the integration unit is the last, and alone
				}
				if soloUnit && len(running) > 0 {
					continue
				}
				fp := r.footprint(unit, branchFiles)
				linked := ""
				for id, other := range running {
					if s := fp.sharedWith(other); len(s) > 0 {
						linked = fmt.Sprintf("%s (%s)", id, strings.Join(s[:min(len(s), 3)], " "))
						break
					}
				}
				if linked != "" {
					continue
				}
				turn++
				if r.prepareUnit(unit, theory) {
					prepared = true // the plan changed (a split, a deferral, a reopen): look again
					break
				}
				running[unit.ID] = fp
				if soloUnit {
					delete(r.alone, unit.ID)
					alone = true
				}
				if len(running) > 1 {
					log.Printf("dev_schedule: %s unit %s starts beside %d other unit(s) — nothing measured links them", r.repo, unit.ID, len(running)-1)
				}
				started = true
				go func(unit DevPlanNode, idx, total int) {
					stdout, passed, requeue := attempt(unit, idx, total)
					results <- unitResult{unit: unit, stdout: stdout, passed: passed, requeue: requeue}
				}(unit, idx, len(leaves))
				if soloUnit {
					break
				}
			}
			if !prepared && !started {
				break
			}
			if !prepared {
				break // every unit that may run has started
			}
		}
		if len(running) == 0 {
			if stopErr != nil || requeued || turn >= 400 {
				return lastStdout, stopErr, requeued
			}
			leaves := plan.Leaves()
			if nextUnitIndex(plan, leaves) >= 0 {
				return lastStdout, stopErr, requeued // nothing could start: not expected; stop rather than spin
			}
			if dir, err := devRepoLocalDir(r.devRepo); err == nil {
				// once per unit: an integration run that passes without a commit of its own leaves
				// the branch as it was, and would be judged stale again without end
				if after := integrationStale(dir, r.branch, plan, leaves); after != "" && !staleRerun[after] {
					staleRerun[after] = true
					plan.withdrawPass(plan.IntegrationUnit.ID)
					log.Printf("dev_from_theorems: %s unit %s committed after the integration unit %s's last verified commit — the integration runs again", r.repo, after, plan.IntegrationUnit.ID)
					devPlanSave(r.repo, r.devRepo, r.branch, plan, fmt.Sprintf("plan: %s runs again after unit %s", plan.IntegrationUnit.ID, after))
					continue
				}
			}
			return lastStdout, nil, false
		}
		var res unitResult
		r.whileWaiting(func() { res = <-results })
		delete(running, res.unit.ID)
		if res.unit.Integration || len(running) == 0 {
			alone = false
		}
		if res.stdout != "" {
			lastStdout = res.stdout
		}
		if err, again := r.afterUnit(res); err != nil && stopErr == nil {
			stopErr = err
		} else if again {
			requeued = true
		}
		if (stopErr != nil || requeued) && len(running) > 0 {
			log.Printf("dev_schedule: %s the run stops after the %d unit(s) still running end", r.repo, len(running))
		}
	}
}

// prepareUnit: what the run decides about a unit before it starts — a stored review failure
// that is another unit's, a split, a deferral. True when it changed the plan instead.
func (r *devRun) prepareUnit(unit DevPlanNode, theory string) bool {
	plan, repo, devRepo, branch := r.plan, r.repo, r.devRepo, r.branch
	// a stored review failure whose files belong to other units is theirs, not this
	// unit's: those units are reopened and this unit's attempts start again. 11/08 unit
	// pf7b804cf95 came back from the plan with three such failures already counted, so
	// the run asked to split it before it could run once and stopped on a port unit
	// that has no modules to split.
	if blames := resumeStoredReviewFailure(plan, unit, branch, r.reviewBlame); len(blames) > 0 {
		log.Printf("dev_from_theorems: %s unit %s: its stored review failure is about files of %s — those are reopened and unit %s starts again",
			repo, unit.ID, strings.Join(blames, " "), unit.ID)
		devPlanSave(repo, devRepo, branch, plan, fmt.Sprintf("plan: %s reopened, unit %s starts again", strings.Join(blames, " "), unit.ID))
		return true
	}

	// whether to split is the kernel's call, from numbers
	need, why := dev_node_needs_split(plan, unit)
	if need && !plan.VerifySeen[unit.ID] && plan.FixAttempts[unit.ID] < 2*DevMaxFixAttempts {
		log.Printf(devSplitUnseenFmt, repo, unit.ID, why)
		need = false
	}
	if need {
		if kids := dev_split_node(r.pid, plan, unit.ID, repo, devRepo, branch, theory, why); kids != nil {
			return true // the leaves changed; look again
		}
		if plan.FixAttempts[unit.ID] >= DevMaxFixAttempts {
			lf := deferredFailure(plan.LastFailure[unit.ID])
			plan.MarkDeferred(unit.ID, lf)
			log.Printf("dev_from_theorems: %s unit %s used its %d fix attempts and has nothing to split — the run goes on and its files are left to the integration unit; last review: %s; %s",
				repo, unit.ID, plan.FixAttempts[unit.ID], truncStr(lf.Text, 300), lf.Attribution)
			devPlanSave(repo, devRepo, branch, plan, fmt.Sprintf("plan: unit %s deferred to the integration unit", unit.ID))
			return true
		}
		log.Printf("dev_from_theorems: %s unit %s is over budget (%s) but cannot be split; running it as is", repo, unit.ID, why)
	}
	return false
}

// afterUnit: what one attempt's result means for the run — an error that stops it, or that
// infrastructure failed and the step is to be pushed again.
func (r *devRun) afterUnit(res unitResult) (error, bool) {
	plan, repo, devRepo, branch, pid := r.plan, r.repo, r.devRepo, r.branch, r.pid
	unit, passed, requeue := res.unit, res.passed, res.requeue
	// A conversation that writes nothing is not a unit that is too big: retrying it, or
	// splitting the unit, starts the same kind of conversation again. Two in a row means
	// the cause is outside the unit — stop, say so, keep everything (the plan and the
	// passed units are on the branch), and leave the decision to a person.
	// no GPU toolchain on this machine: no retry or split can build a GPU unit here
	if lf := plan.LastFailure[unit.ID]; !passed && !requeue && gateOutsideUnit(lf.Kind) {
		stopFmt := devStopMachineFmt
		if !gateIsMachine(lf.Kind) {
			stopFmt = devStopNothingComparedFmt
		}
		msg := fmt.Sprintf(stopFmt, unit.ID, nodeDesc(unit), lf.Text, lf.BaseRef)
		log.Printf("dev_from_theorems: %s %s", repo, msg)
		PRFeedbackAdd(pid, "- dev_from_theorems: "+msg)
		return fmt.Errorf("dev_from_theorems: %s", msg), false
	}
	if !passed && !requeue && plan.Silent(unit.ID) >= devMaxSilentAttempts {
		msg := fmt.Sprintf("unit %s (%s): the last %d conversations wrote no file. a conversation that does not start is not "+
			"cured by retrying or by splitting the unit, so the run stops here. the plan and the %d unit(s) already passed are "+
			"kept on branch %s; fix the cause and start again to continue. what the last one said:\n%s",
			unit.ID, nodeDesc(unit), plan.Silent(unit.ID), passedLeaves(plan), branch,
			truncStr(plan.LastFailure[unit.ID].Text, 1500))
		log.Printf("dev_from_theorems: %s %s", repo, truncStr(strings.ReplaceAll(msg, "\n", " | "), 700))
		PRFeedbackAdd(pid, "- dev_from_theorems: "+msg)
		return fmt.Errorf("dev_from_theorems: %s", msg), false
	}
	if requeue {
		plan.InfraFails++
		devPlanSave(repo, devRepo, branch, plan, "plan: infrastructure retry")
		if plan.InfraFails > 5 {
			return fmt.Errorf("dev_from_theorems: %s unit %s: git/push failed %d times in a row", repo, unit.ID, plan.InfraFails), false
		}
		return nil, true
	}
	plan.InfraFails = 0
	return nil, false
}

// quotaSleep waits for the proxy's quota to come back (a hook for tests).
var quotaSleep = sleepWithKernelHeartbeat

func sleepWithKernelHeartbeat(d time.Duration) { time.Sleep(d) }

// accountRefusedBeside: the proxy refuses the account itself (kernel/proxy_quota.go) and units
// are running — no unit is started beside them until a request of the account is answered.
// With none running one starts: its request is what finds out whether the account is back.
func (r *devRun) accountRefusedBeside(running int) bool {
	since, why := ProxyAccountUnusable()
	if since.IsZero() || running == 0 {
		return false
	}
	if !r.unusableSaid.Equal(since) {
		r.unusableSaid = since
		log.Printf("dev_schedule: %s the proxy has refused this account since %s (%s) — no unit is started beside the %d running until a request of it is answered", r.repo, since.Format("15:04:05"), why, running)
	}
	return true
}
