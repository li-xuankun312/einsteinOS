package kernel

// eval_integration.go — the integration unit's evaluation, run by the kernel inside its
// verification: the reference built and benchmarked here at the commit, the searched
// repositories built and benchmarked here, the port's counterparts run in the unit's
// workspace, the record stored and compared with the integration before (eval_gate.go).
//
// Verification sees the masked requirements; the reference itself is run unmasked by the
// kernel (it is never shown to a conversation), so dev_run registers the evaluation per unit.

import (
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
	b.clues = evalClues(repo, refBareDir(repo), sha)
	evalTaskMu.Lock()
	task := evalTask
	evalTaskMu.Unlock()
	taken, trace := evalDiscover(task, []string{repo}, evalAcceptRepo)
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
		} else if k, t := e2ePortGate(rec.E2E, e2ePortKernels(ws, unitID)); k != "" {
			rec.Gate, rec.GateText = k, t
		} else if k, t := e2eUnchangedSince(repo, sha, ws, rec.E2E, prev); k != "" {
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
