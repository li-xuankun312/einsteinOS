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
)

type evalBase struct {
	reference EvalRun
	external  []EvalRun
	clues     []EvalClue
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
func evalBaseFor(repo, sha string, benches []EvalBench) evalBase {
	key := repo + "@" + sha
	evalBaseMu.Lock()
	b, ok := evalBaseCache[key]
	evalBaseMu.Unlock()
	if ok {
		return b
	}
	b.reference = evalRunRepo(repo, refBareDir(repo), sha, benches)
	b.clues = evalClues(repo, refBareDir(repo), sha)
	evalTaskMu.Lock()
	task := evalTask
	evalTaskMu.Unlock()
	for _, r := range evalDiscover(task, []string{repo}, evalAcceptRepo) {
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
	evalBaseMu.Lock()
	evalBaseCache[key] = b
	evalBaseMu.Unlock()
	return b
}

// evalIntegration is the evaluation dev_run registers for an integration unit.
func evalIntegration(repo, sha, devRepo string, benches []EvalBench, mask func(string) string) func(string, []EvalBench) (string, string) {
	return func(ws string, shown []EvalBench) (string, string) {
		base := evalBaseFor(repo, sha, benches)
		rec := EvalRecord{Repo: repo, RefSHA: sha, Reference: base.reference, External: base.external,
			Clues: base.clues, Dev: evalRunPort(ws, shown), When: time.Now().UTC(),
			Conditions: measureEvalConditions(repo, sha)}
		var prev *EvalRecord
		if dir, err := evalStateDir(devRepo); err == nil {
			prev = prevEvalRecord(dir, sha)
		} else {
			log.Printf("eval: %s: the state branch could not be read (%v) — judged against the reference only", repo, err)
		}
		if prev != nil {
			rec.PrevSHA = prev.RefSHA
		}
		rec.Gate, rec.GateText = evalGate(rec, prev, mask)
		log.Printf("eval: %s %s: reference build=%d %d metric(s); port %d metric(s); %d searched repo(s); %d clue(s); previous %q; gate=%q",
			repo, shortSHA(sha), rec.Reference.BuildExit, len(rec.Reference.Metrics()), len(rec.Dev.Metrics()),
			len(rec.External), len(rec.Clues), shortSHA(rec.PrevSHA), rec.Gate)
		if err := storeEvalRecord(devRepo, rec); err != nil {
			log.Printf("eval: %s %s: record NOT stored: %v", repo, shortSHA(sha), err)
		}
		return rec.Gate, rec.GateText
	}
}
