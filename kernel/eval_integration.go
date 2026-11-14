package kernel

// eval_integration.go — the integration unit's evaluation, run by the kernel inside its
// verification: the reference built and benchmarked here at the commit, the searched
// repositories built and benchmarked here, the port's counterparts run in the unit's
// workspace, the record stored and compared with the integration before (eval_gate.go).
//
// Verification sees the masked requirements; the reference itself is run unmasked by the
// kernel (it is never shown to a conversation), so dev_run registers the evaluation per unit.

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

var (
	integrationEvalMu    sync.Mutex
	integrationEvalHooks = map[string]func(ws string, shownBenches []EvalBench) (kind, text string){}

	// the reference's and the searched repositories' runs do not depend on the port: they
	// are measured once per reference commit and reused by every verification of it
	evalBaseMu    sync.Mutex
	evalBaseCache = map[string]evalBase{}
	// evalBaseInFlight: the measurements running now, closed when stored
	evalBaseInFlight = map[string]chan struct{}{}
)

type evalBase struct {
	reference EvalRun
	external  []EvalRun
	clues     []EvalClue
	docs      map[string]string // "<repo> <file>" → text, the documents the clues are in
	discover  EvalDiscoverTrace
}

// RegisterIntegrationEval makes fn the evaluation of unitID's verification.
func RegisterIntegrationEval(unitID string, fn func(ws string, shownBenches []EvalBench) (string, string)) {
	integrationEvalMu.Lock()
	integrationEvalHooks[unitID] = fn
	integrationEvalMu.Unlock()
}

// UnregisterIntegrationEval removes unitID's evaluation.
func UnregisterIntegrationEval(unitID string) {
	integrationEvalMu.Lock()
	delete(integrationEvalHooks, unitID)
	integrationEvalMu.Unlock()
}

func integrationEval(unitID string) func(string, []EvalBench) (string, string) {
	integrationEvalMu.Lock()
	defer integrationEvalMu.Unlock()
	return integrationEvalHooks[unitID]
}

// evalBaseFor measures the reference at sha and the searched repositories, once per sha.
//
// Verifications that ask at the same time wait for the one measuring: 11/10 between 16:46 and
// 22:45 the integration unit's verifications, running side by side, each found the cache
// empty and ran the search again — six searches of the same commit.
func evalBaseFor(repo, sha string, benches []EvalBench) evalBase {
	key := repo + "@" + sha
	for {
		evalBaseMu.Lock()
		b, ok := evalBaseCache[key]
		wait, measuring := evalBaseInFlight[key]
		if !ok && !measuring {
			evalBaseInFlight[key] = make(chan struct{})
		}
		evalBaseMu.Unlock()
		if ok {
			return b
		}
		if !measuring {
			break
		}
		<-wait
	}
	var b evalBase
	defer func() {
		evalBaseMu.Lock()
		close(evalBaseInFlight[key])
		delete(evalBaseInFlight, key)
		evalBaseMu.Unlock()
	}()
	b.reference = evalRunRepo(repo, refBareDir(repo), sha, benches)
	if len(benches) == 0 && b.reference.Err == "" {
		b.reference.Err = evalNoBenchReason(refBareDir(repo), sha)
	}
	b.clues = evalClues(repo, refBareDir(repo), sha)
	evalTaskMu.Lock()
	task := evalTask
	evalTaskMu.Unlock()
	taken, trace := evalDiscover(evalTaskWithConditions(task, measureEvalConditions(repo, sha)), []string{repo}, evalAcceptRepo)
	b.discover = trace
	for _, r := range taken {
		name := r.Owner + "/" + r.Name
		bare := evalBareDir(name)
		head, err := refGitFn(bare, "rev-parse", "HEAD")
		if err != nil {
			continue
		}
		head = strings.TrimSpace(head)
		eb, _ := evalBenchmarks(bare, head)
		b.external = append(b.external, evalRunRepo(name, bare, head, eb))
		b.clues = append(b.clues, evalClues(name, bare, head)...)
	}
	b.docs = evalClueDocs(b.clues)
	evalBaseMu.Lock()
	evalBaseCache[key] = b
	evalBaseMu.Unlock()
	return b
}

// evalIntegration is the evaluation dev_run registers for an integration unit.
func evalIntegration(pid int64, repo, sha, devRepo, unitID string, benches []EvalBench, mask func(string) string) func(string, []EvalBench) (string, string) {
	return func(ws string, shown []EvalBench) (string, string) {
		base := evalBaseFor(repo, sha, benches)
		rec := EvalRecord{Repo: repo, RefSHA: sha, Reference: base.reference, External: base.external,
			Clues: base.clues, Dev: evalRunPort(ws, shown), When: time.Now().UTC(),
			Conditions: measureEvalConditions(repo, sha), Discover: base.discover}
		var prev *EvalRecord
		if dir, err := evalStateDir(devRepo); err == nil {
			prev = prevEvalRecord(dir, sha)
		} else {
			log.Printf("eval: %s: the state branch could not be read (%v) — judged against the reference only", repo, err)
		}
		if prev != nil {
			rec.PrevSHA = prev.RefSHA
		}
		rec.E2E = evalRunE2E(ws, unitID)
		if k, t := e2eGate(rec.E2E); k != "" {
			rec.Gate, rec.GateText = k, t
		} else if k, t := e2eModelGate(rec.E2E, evalModel()); k != "" {
			rec.Gate, rec.GateText = k, t
		} else if k, t := e2ePortGate(rec.E2E, e2ePortKernels(ws, unitID)); k != "" {
			rec.Gate, rec.GateText = k, t
		} else if k, t := e2eUnchangedSince(repo, sha, ws, rec.E2E, prev); k != "" {
			rec.Gate, rec.GateText = k, t
		} else if k, t := e2eTokensGate(rec.E2E); k != "" {
			rec.Gate, rec.GateText = k, t
		} else if k, t := evalCorrectGate(rec.E2E, evalModel()); k != "" {
			rec.Gate, rec.GateText = k, t
		} else {
			external, trace := evalComparisons(pid, repo, rec.Conditions, rec.E2E, rec.Clues, base.docs)
			rec.Compare = trace
			var own []EvalComparison
			rec.Gate, rec.GateText, own = evalGateCompared(rec, prev, mask)
			rec.Compared = append(own, external...)
			if rec.Gate == "" {
				rec.Gate, rec.GateText = evalExternalGate(external)
			}
			if rec.Gate == "" && len(rec.Compared) == 0 {
				rec.Gate, rec.GateText = GateNothingCompared, evalNothingComparedText(rec, prev)
			}
		}
		log.Printf("eval: %s %s: reference build=%d %d metric(s); port %d metric(s); end-to-end on %s exit=%d %d metric(s); %d searched repo(s); %d clue(s); previous %q; gate=%q",
			repo, shortSHA(sha), rec.Reference.BuildExit, len(rec.Reference.Metrics()), len(rec.Dev.Metrics()),
			rec.Conditions.ModelName, rec.E2E.Exit, len(rec.E2E.Metrics), len(rec.External), len(rec.Clues), shortSHA(rec.PrevSHA), rec.Gate)
		if err := storeEvalRecord(devRepo, rec); err != nil {
			log.Printf("eval: %s %s: record NOT stored: %v", repo, shortSHA(sha), err)
		}
		return rec.Gate, rec.GateText
	}
}

// evalClueDocs reads the documents the clues are in, once each.
func evalClueDocs(clues []EvalClue) map[string]string {
	docs := map[string]string{}
	for _, c := range clues {
		k := c.Repo + " " + c.File
		if _, ok := docs[k]; ok {
			continue
		}
		bare := evalBareDir(c.Repo)
		if !fileExists(bare) {
			bare = refBareDir(c.Repo)
		}
		if text, err := refGitFn(bare, "show", c.SHA+":"+c.File); err == nil {
			docs[k] = text
		}
	}
	return docs
}

// e2eUnchangedSince: e2eUnchangedGate against the previous record, the reference's changes
// read from its bare clone.
func e2eUnchangedSince(repo, sha, ws string, cur EvalBenchRun, prev *EvalRecord) (string, string) {
	if prev == nil {
		return "", ""
	}
	out, err := refGitFn(refBareDir(repo), "diff", "--name-only", prev.RefSHA, sha)
	if err != nil {
		log.Printf("eval: %s: the reference's changes since %s could not be read (%v)", repo, shortSHA(prev.RefSHA), err)
		return "", ""
	}
	return e2eUnchangedGate(cur, prev.E2E, prev.RefSHA, strings.Fields(out), portMapRefFiles(ws))
}

// gateNothingComparedFmt: why each side gave nothing to compare with
const (
	gateNothingComparedFmt = GateNothingCompared + ": the port's end-to-end run was compared with nothing outside the pipeline — the reference here: %[1]s; the previous integration: %[2]s; searched repositories: %[3]s; published numbers: %[4]s"
	evalNoPrevFirst        = "none, this is the first integrated commit"
	evalNoPrevRatesFmt     = "%[1]s has no rate the port's run also printed"
	evalRefNothingFmt      = "built exit %[1]d, %[2]d benchmark(s) run, %[3]d number(s)%[4]s"
	evalExtNothingFmt      = "%[1]s %[2]s"
	evalPublishedFmt       = "%[1]d read, the planner named %[2]d comparable%[3]s"
)

// evalNothingComparedText: integration_nothing_compared's text, from the record. 11/09 and
// 11/10 the integration passed with nothing compared: the reference shipped no runnable
// benchmark, the one searched repository (triton-lang/triton) failed its checkout, and the
// planner found none of 68 published numbers comparable — an empty verdict passed.
func evalNothingComparedText(rec EvalRecord, prev *EvalRecord) string {
	why := func(s string) string {
		if s == "" {
			return ""
		}
		return " (" + s + ")"
	}
	ref := fmt.Sprintf(evalRefNothingFmt, rec.Reference.BuildExit, len(rec.Reference.Benches), len(rec.Reference.Metrics()), why(rec.Reference.Err))
	p := evalNoPrevFirst
	if prev != nil {
		p = fmt.Sprintf(evalNoPrevRatesFmt, shortSHA(prev.RefSHA))
	}
	var ext []string
	for _, e := range rec.External {
		w := e.Err
		if w == "" {
			w = fmt.Sprintf("%d number(s)", len(e.Metrics()))
		}
		ext = append(ext, fmt.Sprintf(evalExtNothingFmt, e.Repo, tailText(w, 200)))
	}
	if len(ext) == 0 {
		ext = append(ext, fmt.Sprintf("none taken of %d named", len(rec.Discover.Named)))
	}
	pub := fmt.Sprintf(evalPublishedFmt, rec.Compare.Published, rec.Compare.Named, why(rec.Compare.NotAsked))
	return fmt.Sprintf(gateNothingComparedFmt, ref, p, strings.Join(ext, "; "), pub)
}

// evalTaskConditionsFmt: the task and, inserted as they are, the conditions the kernel
// measured on this machine (the model, the GPUs, the memory)
const evalTaskConditionsFmt = "%[1]s\n\n%[2]s"

// evalTaskWithConditions: the search for repositories to compare with is given the measured
// conditions beside the task. 11/10 it was given the task alone and named llvm/llvm-project,
// ROCm/HIP and triton-lang/triton — compilers, none running the model on this machine
// (Qwen3.6-35B-A3B) — and of the 68 published numbers the planner then read, none was
// measured on that model, so none was comparable.
func evalTaskWithConditions(task string, cond EvalConditions) string {
	cj, err := json.Marshal(cond)
	if err != nil {
		return task
	}
	return fmt.Sprintf(evalTaskConditionsFmt, task, cj)
}
