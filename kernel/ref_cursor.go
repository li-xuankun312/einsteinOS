package kernel

// ref_cursor.go — after one pr_pipeline completes, the next reference commit.
//
// The smallest stored unit is one pr_pipeline: theory for one reference commit, then every
// dev unit verified on the real machine and reviewed. On 2026/11/03 that completed for
// commit 0 of Niko1221/Strata ("2437 remaining") and the kernel stopped: ref_bind_at could
// already start from any index ("start=N"), but nothing asked it to. Now ref_bind_at records
// which commit it bound, and when the dev step completes the kernel queues ref_bind for the
// next commit and a dev step for it — the cursor moves only after a complete pipeline.

import (
	"fmt"
	"log"
	"sync"
)

type boundRef struct {
	Idx   int    // index of the bound commit in the reference's history
	SHA   string // its sha
	Total int    // commits in the history
}

var (
	refBoundMu sync.Mutex
	refBound   = map[int64]map[string]boundRef{} // pid → owner/repo → the commit ref_bind bound
)

func recordRefBound(pid int64, repo string, b boundRef) {
	refBoundMu.Lock()
	defer refBoundMu.Unlock()
	if refBound[pid] == nil {
		refBound[pid] = map[string]boundRef{}
	}
	refBound[pid][repo] = b
}

func refBoundOf(pid int64, repo string) (boundRef, bool) {
	refBoundMu.Lock()
	defer refBoundMu.Unlock()
	b, ok := refBound[pid][repo]
	return b, ok
}

const refCursorLogFmt = "ref_cursor: %[1]s pipeline complete for commit %[2]d (%[3]s); next: commit %[4]d of %[5]d (%[6]d remaining after it)"

// advanceAfterPipeline queues the next commit's pipeline (ref_bind start=idx+1, then the
// dev step) and reports whether there was one.
func advanceAfterPipeline(pid int64, owner, repo string) bool {
	full := owner + "/" + repo
	b, ok := refBoundOf(pid, full)
	if !ok {
		log.Printf("ref_cursor: %s pipeline complete, but no bound commit is recorded — not advancing", full)
		return false
	}
	next := b.Idx + 1
	if next >= b.Total {
		log.Printf("ref_cursor: %s pipeline complete for the last commit (%d of %d)", full, b.Idx, b.Total)
		return false
	}
	log.Printf(refCursorLogFmt, full, b.Idx, short_sha(b.SHA), next, b.Total, b.Total-next-1)
	pr_push_step(pid, &Step{Kind: STEP_REF_BIND, Owner: owner, Repo: repo, Command: fmt.Sprintf("start=%d", next)})
	pr_push_step(pid, &Step{Kind: STEP_DEV_FROM_THEOREMS, Owner: owner, Repo: repo})
	return true
}
