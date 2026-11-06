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
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "clone", "--bare", "--filter=blob:none",
			fmt.Sprintf("https://github.com/%s/%s.git", owner, name), dir)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			os.RemoveAll(dir)
			return fmt.Errorf("%v: %s", err, tailText(string(out), 200))
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
// as the baseline, not as a candidate). accept measures what a clone must hold.
func evalDiscover(task string, exclude []string, accept func(repo string) (bool, string)) []RefRepo {
	skip := map[string]bool{}
	for _, e := range exclude {
		skip[strings.ToLower(e)] = true
	}
	var good []RefRepo
	prompt := fmt.Sprintf(refDiscoverPromptFmt, truncDiff(task, 3000), MAX_REFS)
	for tried := 1; tried <= 3 && len(good) < MAX_REFS; tried++ {
		raw, err := evalAskFn(prompt)
		if err != nil {
			log.Printf("eval_discover: ask: %v", err)
			return good
		}
		cands := parseRefRepos(raw)
		log.Printf("eval_discover: attempt %d: %d candidate(s) named", tried, len(cands))
		var failed []string
		for _, r := range cands {
			key := r.Owner + "/" + r.Name
			if skip[strings.ToLower(key)] || is_dev_repo(r.Owner, r.Name) {
				continue
			}
			skip[strings.ToLower(key)] = true
			if err := evalCloneFn(r.Owner, r.Name, evalBareDir(key)); err != nil {
				log.Printf("eval_discover: %s not taken: does not clone (%v)", key, err)
				failed = append(failed, key)
				continue
			}
			if ok, why := accept(key); !ok {
				log.Printf("eval_discover: %s not taken: %s", key, why)
				continue
			} else {
				log.Printf("eval_discover: %s taken: %s", key, why)
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
	return good
}
