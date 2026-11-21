package kernel

// eval_clues.go — numbers a repository only states (README, docs) are kept as clues next to
// the measurements and never judged on: without code that reproduces them on this machine
// they cannot be compared with anything run here.

import (
	"path"
	"regexp"
	"strings"
)

// EvalClue is a number a repository's documentation states.
type EvalClue struct {
	Repo   string `json:"repo"`
	SHA    string `json:"sha"`
	File   string `json:"file"`
	Metric Metric `json:"metric"`
}

// evalDocRe: documentation and published results (11/08 Strata ships its measured matrix as
// bench/results/<date>/matrix.md, with no code that produced it).
var evalDocRe = regexp.MustCompile(`(?i)(^|/)(readme[^/]*|(docs?|bench\w*|benchmarks?|results?)/.*)\.(md|rst|txt)$`)

// evalClues reads the stated numbers of the repository's documentation at sha.
func evalClues(repo, bare, sha string) []EvalClue {
	ls, err := refGitFn(bare, "ls-tree", "-r", "--name-only", sha)
	if err != nil {
		return nil
	}
	var out []EvalClue
	for _, f := range strings.Split(ls, "\n") {
		f = strings.TrimSpace(f)
		if !evalDocRe.MatchString(f) || strings.Count(f, "/") > 4 || path.Base(f) == "" {
			continue
		}
		text, err := refGitFn(bare, "show", sha+":"+f)
		if err != nil {
			continue
		}
		for _, m := range metricsIn(text) {
			out = append(out, EvalClue{Repo: repo, SHA: sha, File: f, Metric: m})
		}
	}
	return out
}
