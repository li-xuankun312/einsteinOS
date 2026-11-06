package kernel

// eval_store.go — every integrated commit's evaluation is kept, so each commit is compared
// with the integration before it.
//
// The records live on the kernel's state branch (einsteinOS/state, which no conversation
// clones): .einsteinOS/eval/<reference sha>.json, one per integrated reference commit, and
// .einsteinOS/eval/index.json, the reference commits in the order they were integrated. The
// previous record of a commit is the one before it in that order; the first has none.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// EvalRecord is one integrated commit's evaluation.
type EvalRecord struct {
	Repo      string     `json:"repo"`    // the reference
	RefSHA    string     `json:"ref_sha"` // the commit integrated
	PrevSHA   string     `json:"prev_sha,omitempty"`
	Reference EvalRun    `json:"reference"` // the reference itself, built and benchmarked here
	Dev       EvalRun    `json:"dev"`       // the port's counterparts of the same benchmarks
	External  []EvalRun  `json:"external"`  // the repositories eval_discover took
	Clues     []EvalClue `json:"clues"`
	Gate      string     `json:"gate"`
	GateText  string     `json:"gate_text"`
	When      time.Time  `json:"when"`
}

const (
	evalDir       = ".einsteinOS/eval"
	evalIndexFile = ".einsteinOS/eval/index.json"
	evalRecordFmt = ".einsteinOS/eval/%[1]s.json"
)

func evalRecordPath(sha string) string { return fmt.Sprintf(evalRecordFmt, sha) }

// readEvalIndex: the integrated reference commits, in order.
func readEvalIndex(dir string) []string {
	var idx []string
	if b, err := os.ReadFile(filepath.Join(dir, evalIndexFile)); err == nil {
		json.Unmarshal(b, &idx)
	}
	return idx
}

// prevEvalRecord: the record of the commit integrated before sha (nil for the first).
func prevEvalRecord(dir, sha string) *EvalRecord {
	idx := readEvalIndex(dir)
	prev := ""
	for _, s := range idx {
		if s == sha {
			break
		}
		prev = s
	}
	if prev == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(dir, evalRecordPath(prev)))
	if err != nil {
		return nil
	}
	var r EvalRecord
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

// writeEvalRecord writes rec and puts its commit in the index (once, at the end).
func writeEvalRecord(dir string, rec EvalRecord) error {
	if err := os.MkdirAll(filepath.Join(dir, evalDir), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, evalRecordPath(rec.RefSHA)), b, 0o644); err != nil {
		return err
	}
	idx := readEvalIndex(dir)
	for _, s := range idx {
		if s == rec.RefSHA {
			return nil
		}
	}
	ib, _ := json.MarshalIndent(append(idx, rec.RefSHA), "", " ")
	return os.WriteFile(filepath.Join(dir, evalIndexFile), ib, 0o644)
}

// evalStateDir checks out the state branch of the dev repository and returns its directory.
func evalStateDir(devRepo string) (string, error) {
	dir, err := devRepoLocalDir(devRepo)
	if err != nil {
		return "", err
	}
	if err := gitUseBranchFromRemote(dir, devStateBranch, devNewBranchBase(devRepo)); err != nil {
		return "", err
	}
	return dir, nil
}

// storeEvalRecord commits rec on the state branch and pushes it.
func storeEvalRecord(devRepo string, rec EvalRecord) error {
	dir, err := evalStateDir(devRepo)
	if err != nil {
		return err
	}
	if err := writeEvalRecord(dir, rec); err != nil {
		return err
	}
	GitCommit(dir, fmt.Sprintf("eval: %s %s gate=%q", rec.Repo, shortSHA(rec.RefSHA), rec.Gate))
	return devGitPush(dir, devRepo, devStateBranch, 20)
}
