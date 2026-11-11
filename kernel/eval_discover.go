package kernel

// eval_discover.go — the repository search that ref_discover keeps sealed, run for evaluation.
//
// 8a9627bd (10-19) made ref_discover return as soon as the task names a reference: the
// child that searches GitHub for more repositories never runs once the task says
// "Niko1221/Strata". The integration unit needs exactly that search — repositories outside
// the pipeline whose code measures the same work — so this is a copy of the sealed path:
// same prompt, same parse, same clone check, but its repositories are clones of their own
// (/tmp/eval_<owner>_<name>.git), never registered as references, never theorized or ported.

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const evalBareDirFmt = "/tmp/eval_%[1]s_%[2]s.git"

// evalCloneTimeout: how long a searched repository's clone may take.
var evalCloneTimeout = 120 * time.Second

// runKillingGroup runs name args in a process group of its own and, at timeout, kills the
// whole group and stops waiting for its output. 11/10 the clone's 120 s context killed git
// only; git-remote-https held the output pipe, CombinedOutput waited on it, and the llvm
// clone was reported "not taken" 37 minutes after it started (19:15 → 19:52).
func runKillingGroup(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	setProcGroup(cmd)
	cmd.Cancel = func() error { killProcGroup(cmd); return nil }
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		err = fmt.Errorf("killed after %v", timeout)
	}
	return string(out), err
}

func evalBareDir(repo string) string {
	owner, name, _ := strings.Cut(repo, "/")
	return fmt.Sprintf(evalBareDirFmt, owner, name)
}

var (
	// evalAskFn asks a child the search prompt and returns its reply (a variable for tests)
	evalAskFn = func(prompt string) (string, error) {
		pid, err := Fork(prompt, ".")
		if err != nil {
			return "", err
		}
		Wait(pid)
		return GetResult(pid), nil
	}
	// evalCloneFn makes the blobless bare clone of owner/name at dir
	evalCloneFn = func(owner, name, dir string) error {
		if exec.Command("git", "--git-dir", dir, "rev-parse", "HEAD").Run() == nil {
			return nil
		}
		os.RemoveAll(dir)
		out, err := runKillingGroup(evalCloneTimeout, "git", "clone", "--bare", "--filter=blob:none",
			fmt.Sprintf("https://github.com/%s/%s.git", owner, name), dir)
		if err != nil {
			os.RemoveAll(dir)
			return fmt.Errorf("%v: %s", err, tailText(out, 200))
		}
		return nil
	}
	evalTaskMu sync.Mutex
	evalTask   string // the task ref_discover was given (PRSetup)
)

// SetEvalTask records the task the evaluation search runs on.
func SetEvalTask(task string) {
	evalTaskMu.Lock()
	evalTask = task
	evalTaskMu.Unlock()
}

// evalDiscover runs the sealed search: up to MAX_REFS candidates the child names that clone,
// excluding the dev repositories and the ones in exclude (the reference itself is evaluated
// as the baseline, not as a candidate). accept measures what a clone must hold
// (evalAcceptRepo: a benchmark the kernel can run).
func evalDiscover(task string, exclude []string, accept func(repo string) (bool, string)) ([]RefRepo, EvalDiscoverTrace) {
	var tr EvalDiscoverTrace
	skip := map[string]bool{}
	for _, e := range exclude {
		skip[strings.ToLower(e)] = true
	}
	var good []RefRepo
	prompt := fmt.Sprintf(refDiscoverPromptFmt, truncDiff(task, 3000), MAX_REFS)
	for tried := 1; tried <= 3 && len(good) < MAX_REFS; tried++ {
		raw, err := evalAskFn(prompt)
		tr.Asked++
		if err != nil {
			log.Printf("eval_discover: ask: %v", err)
			tr.AskError = err.Error()
			return good, tr
		}
		cands := parseRefRepos(raw)
		log.Printf("eval_discover: attempt %d: %d candidate(s) named", tried, len(cands))
		var failed []string
		for _, r := range cands {
			key := r.Owner + "/" + r.Name
			tr.Named = append(tr.Named, key)
			if skip[strings.ToLower(key)] || is_dev_repo(r.Owner, r.Name) {
				tr.NotTaken = append(tr.NotTaken, key+": the reference, a dev repository or named before")
				continue
			}
			skip[strings.ToLower(key)] = true
			if why, ok := evalNotTakenBefore(key); ok {
				tr.NotTaken = append(tr.NotTaken, key+": "+why+" (measured before in this run)")
				failed = append(failed, key)
				continue
			}
			if why := evalTooLarge(r.Owner, r.Name); why != "" {
				log.Printf("eval_discover: %s not taken: %s", key, why)
				tr.NotTaken = append(tr.NotTaken, key+": "+why)
				evalRecordNotTaken(key, why)
				continue
			}
			if err := evalCloneFn(r.Owner, r.Name, evalBareDir(key)); err != nil {
				log.Printf("eval_discover: %s not taken: does not clone (%v)", key, err)
				tr.NotTaken = append(tr.NotTaken, fmt.Sprintf("%s: does not clone (%v)", key, err))
				evalRecordNotTaken(key, fmt.Sprintf("does not clone (%v)", err))
				failed = append(failed, key)
				continue
			}
			if ok, why := accept(key); !ok {
				log.Printf("eval_discover: %s not taken: %s", key, why)
				tr.NotTaken = append(tr.NotTaken, key+": "+why)
				evalRecordNotTaken(key, why)
				continue
			} else {
				log.Printf("eval_discover: %s taken: %s", key, why)
				tr.Taken = append(tr.Taken, key+": "+why)
			}
			good = append(good, r)
			if len(good) == MAX_REFS {
				break
			}
		}
		if len(failed) == 0 && len(cands) > 0 {
			break
		}
		prompt = fmt.Sprintf(refDiscoverPromptFmt, truncDiff(task, 3000), MAX_REFS-len(good))
	}
	return good, tr
}

// evalNotTaken: the repositories the search measured and did not take in this run, and why.
// 11/10 llvm/llvm-project was named, cloned and not taken at 16:48, 19:52 and 22:28, each time
// after the same clone; a repository not taken is not measured again.
var (
	evalNotTakenMu sync.Mutex
	evalNotTaken   = map[string]string{}
)

func evalRecordNotTaken(repo, why string) {
	evalNotTakenMu.Lock()
	evalNotTaken[strings.ToLower(repo)] = why
	evalNotTakenMu.Unlock()
}

func evalNotTakenBefore(repo string) (string, bool) {
	evalNotTakenMu.Lock()
	defer evalNotTakenMu.Unlock()
	why, ok := evalNotTaken[strings.ToLower(repo)]
	return why, ok
}

// EvalDiscoverTrace: what the search did, kept in the evaluation record — 11/09 the first
// record on the machine said "external": null, and whether the search had been asked, named
// anything or rejected everything could be read only from a log nobody had kept.
type EvalDiscoverTrace struct {
	Asked    int      `json:"asked"`               // times the search conversation was asked
	AskError string   `json:"ask_error,omitempty"` // why asking it failed
	Named    []string `json:"named"`               // every owner/name it replied
	Taken    []string `json:"taken"`               // "<repo>: <why>"
	NotTaken []string `json:"not_taken"`           // "<repo>: <why>"
}
