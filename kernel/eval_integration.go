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
	"os"
	"path/filepath"
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

// %[1]s repo, %[2]s commit, %[3]d how many groups, %[4]s which
const evalLeftoversKilledFmt = "eval: %[1]s %[2]s: before the end-to-end run, %[3]d process group(s) commands left running here were killed: %[4]s"

// %[1]s repo, %[2]s commit, %[3]d build exit, %[4]d metrics
const evalReferenceReusedFmt = "eval: %[1]s %[2]s: the reference was built and run here by an earlier run (build exit %[3]d, %[4]d metric(s)) — read from the state branch, not built again"

// evalBaseFor measures the reference at sha and the searched repositories, once per sha.
//
// Verifications that ask at the same time wait for the one measuring: 11/10 between 16:46 and
// 22:45 the integration unit's verifications, running side by side, each found the cache
// empty and ran the search again — six searches of the same commit.
func evalBaseFor(repo, sha string, benches []EvalBench, stored *EvalRun) evalBase {
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
			log.Printf("eval: %s %s: the reference and the search were measured before — reused", repo, shortSHA(sha))
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
	start := time.Now()
	if stored != nil {
		b.reference = *stored
		log.Printf(evalReferenceReusedFmt, repo, shortSHA(sha), b.reference.BuildExit, len(b.reference.Metrics()))
	} else {
		log.Printf("eval: %s %s: building the reference and running its %d benchmark(s) here", repo, shortSHA(sha), len(benches))
		b.reference = evalRunRepo(repo, refBareDir(repo), sha, benches)
		log.Printf("eval: %s %s: the reference built and run in %s (build exit %d, %d metric(s))", repo, shortSHA(sha), time.Since(start).Round(time.Second), b.reference.BuildExit, len(b.reference.Metrics()))
		if len(benches) == 0 && b.reference.Err == "" {
			b.reference.Err = evalNoBenchReason(refBareDir(repo), sha)
		}
	}
	b.clues = evalClues(repo, refBareDir(repo), sha)
	start = time.Now()
	log.Printf("eval: %s %s: searching for repositories to compare with", repo, shortSHA(sha))
	taken, trace := evalDiscover(evalSearchFacts(measureEvalConditions(repo, sha)), []string{repo}, evalAcceptRepo)
	log.Printf("eval: %s %s: the search ended in %s, %d repositor(ies) taken", repo, shortSHA(sha), time.Since(start).Round(time.Second), len(taken))
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
		// no e2e.sh: nothing the reference or the search would measure can be compared with
		// anything, so the unit is told before either runs (11/20 01:25–02:49 a reference
		// build of 21 minutes and a search of 56 for an integration that had none)
		if _, err := os.Stat(filepath.Join(ws, devE2EScript(unitID))); err != nil {
			br := EvalBenchRun{Bench: EvalBench{File: devE2EScript(unitID), Kind: "e2e", Name: "e2e.sh"}, Exit: -1, Tail: "missing"}
			k, t := e2eGate(br)
			log.Printf("eval: %s %s: %s does not exist — the reference is not built and nothing is searched; gate=%q", repo, shortSHA(sha), br.Bench.File, k)
			return k, t
		}
		var prev *EvalRecord
		var stored *EvalRun
		if dir, err := evalStateDir(devRepo); err == nil {
			prev = prevEvalRecord(dir, sha)
			// the reference at sha measured by an earlier run is on the state branch: it is
			// not built again (11/21 06:04:48-06:26:42 a build of 21m54s for 0 benchmarks,
			// after a restart, of a commit whose reference an earlier run had built)
			stored = storedReference(dir, repo, sha, benches)
		} else {
			log.Printf("eval: %s: the state branch could not be read (%v) — judged against the reference only", repo, err)
		}
		rec := EvalRecord{Repo: repo, RefSHA: sha, When: time.Now().UTC(), Conditions: measureEvalConditions(repo, sha)}
		if prev != nil {
			rec.PrevSHA = prev.RefSHA
		}
		// the port is measured on GPUs nothing else here holds: what a command run for a
		// conversation left running is killed first
		if killed := KillLeftoverProcessGroups(); len(killed) > 0 {
			log.Printf(evalLeftoversKilledFmt, repo, shortSHA(sha), len(killed), strings.Join(killed, "; "))
		}
		// the port's own run on the model comes first, and the reference is built and the
		// search run only for a run that is the port's: 11/21 the restarted integration was to
		// build the reference for 22 minutes and search for 39 before its e2e.sh — vllm and a
		// formula's rates — was looked at
		start := time.Now()
		log.Printf("eval: %s %s: running %s on the model at %s", repo, shortSHA(sha), devE2EScript(unitID), evalModel())
		rec.E2E = evalRunE2E(ws, unitID)
		log.Printf("eval: %s %s: %s exited %d in %s, %d metric(s)", repo, shortSHA(sha), devE2EScript(unitID), rec.E2E.Exit, time.Since(start).Round(time.Second), len(rec.E2E.Metrics))
		// the run's direction, as its processes measured it (eval_direction.go)
		var book *DirectionBook
		if dir, err := evalStateDir(devRepo); err == nil {
			book = loadDirections(dir, unitID)
		}
		key := directionKey(ws, rec.E2E)
		k, t := e2eGates(repo, sha, ws, unitID, rec.E2E, prev)
		if book != nil {
			d := book.noteDirection(key, k, time.Now())
			log.Printf(evalDirectionFmt, repo, shortSHA(sha), key, d.Verifications)
			if err := storeDirections(devRepo, book); err != nil {
				log.Printf("eval: %s %s: the directions were not stored: %v", repo, shortSHA(sha), err)
			}
		}
		if k != "" {
			log.Printf(evalE2EFirstFmt, repo, shortSHA(sha), devE2EScript(unitID), k)
			// what the conversation is told, here too: the run's record is not stored, and the
			// gate's name alone does not say which process or file it is about
			log.Printf(evalE2EGateTextFmt, repo, shortSHA(sha), truncStr(t, evalE2EGateTextMax))
			return k, t
		}
		base := evalBaseFor(repo, sha, benches, stored)
		rec.Reference, rec.External, rec.Clues, rec.Discover = base.reference, base.external, base.clues, base.discover
		rec.Dev = evalRunPort(ws, shown)
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
		log.Printf("eval: %s %s: reference build=%d %d metric(s); port %d metric(s); end-to-end on %s exit=%d %d metric(s); %d searched repo(s); %d clue(s); previous %q; gate=%q",
			repo, shortSHA(sha), rec.Reference.BuildExit, len(rec.Reference.Metrics()), len(rec.Dev.Metrics()),
			rec.Conditions.ModelName, rec.E2E.Exit, len(rec.E2E.Metrics), len(rec.External), len(rec.Clues), shortSHA(rec.PrevSHA), rec.Gate)
		if err := storeEvalRecord(devRepo, rec); err != nil {
			log.Printf("eval: %s %s: record NOT stored: %v", repo, shortSHA(sha), err)
		}
		return rec.Gate, rec.GateText
	}
}

// %[1]s repo, %[2]s commit, %[3]s the script, %[4]s the gate
const evalE2EFirstFmt = "eval: %[1]s %[2]s: %[3]s is not yet the port's run on the model (gate=%[4]q) — the reference is not built and nothing is searched"

// %[1]s repo, %[2]s commit, %[3]s the direction, %[4]d its verifications
const evalDirectionFmt = "eval: %[1]s %[2]s: the run's direction: %[3]s (verification %[4]d in it)"

// %[1]s repo, %[2]s commit, %[3]s the gate's text
const evalE2EGateTextFmt = "eval: %[1]s %[2]s: the gate's text, as sent to the conversation:\n%[3]s"

// evalE2EGateTextMax: how much of the gate's text the log keeps.
const evalE2EGateTextMax = 4000

// e2eGates: the first gate the end-to-end run itself fails, in order — it ran, changed nothing
// of the machine, loaded the model it was given, is the port (its kernels, its loading
// process, every shard), has the kernel's measured rate, followed the reference, printed its
// tokens, and generated what the model's own implementation does.
func e2eGates(repo, sha, ws, unitID string, br EvalBenchRun, prev *EvalRecord) (string, string) {
	for _, g := range []func() (string, string){
		func() (string, string) { return e2eGate(br) },
		func() (string, string) { return e2eMachineGate(br) },
		func() (string, string) { return e2eModelGate(br, evalModel()) },
		func() (string, string) { return e2ePortGate(br, e2ePortKernels(ws, unitID)) },
		func() (string, string) { return e2eLoaderGate(br, e2ePortKernels(ws, unitID)) },
		func() (string, string) { return e2eShardsGate(br, evalModel()) },
		func() (string, string) { return e2eModelTakenGate(br, evalModel()) },
		func() (string, string) { return e2eRateGate(br) },
		func() (string, string) { return e2eUnchangedSince(repo, sha, ws, br, prev) },
		func() (string, string) { return e2eTokensGate(br) },
		func() (string, string) { return evalCorrectGate(br, evalModel()) },
	} {
		if k, t := g(); k != "" {
			return k, t
		}
	}
	return "", ""
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

// evalSearchFacts: what the search for repositories to compare with is given — the
// conditions the kernel measured on this machine (the model, the GPUs, the memory, the
// reference), inserted as they are, and not the user's task. 11/10 it had the task alone and
// named llvm/llvm-project, ROCm/HIP and triton-lang/triton, compilers, none running the model
// here (534d7731 put the conditions beside the task). 11/20 01:51:53 conversation 1db7c0db,
// given the task beside them, did what the task asks of the pipeline: it collected Strata's
// 1384 new commits, pushed three commits to the dev repository's main (01:54:49–01:57:31) and
// answered with the reference and the dev repository, both excluded.
func evalSearchFacts(cond EvalConditions) string {
	cj, _ := json.Marshal(cond)
	return string(cj)
}
