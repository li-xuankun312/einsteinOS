package kernel

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Puppet — a disposable main conversation that runs one commit's PR pipeline.
//
// Three-layer state model:
//   L1: Go memory    — cursor, conv_registry, pr_queue (process-level, across all commits)
//   L2: main branch  — confirmed code + .einsteinOS/convs/ (persistent, across puppets)
//   L3: PR branch    — current commit's temp code (within one puppet, merge or discard)
//
// Lifecycle:
//   1. Go process creates puppet for commit N
//   2. Puppet runs: PRSetup → hw_probe → ref_bind → dev_from_theorems
//   3. Pipeline finishes → Go process checks tps
//   4. tps ok → merge PR branch to main (git merge + push)
//   5. Discard puppet → cursor++ → create new puppet for commit N+1
//
// The puppet's main conversation context is bounded to ONE commit.
// Cross-commit state is on main branch (git) + Go memory (conv_registry).
// ---------------------------------------------------------------------------

// PuppetState tracks one puppet's lifecycle.
type PuppetState struct {
	CommitIdx    int       // which commit index this puppet handles
	CommitSHA   string    // the ref repo commit SHA
	PRPid       int64     // the PR process pid
	Branch      string    // PR branch name (einsteinOS/dev-{pid})
	ConvID      string    // the puppet main conversation ID (if any)
	Status      string    // "running", "pipeline_done", "tps_check", "merged", "failed"
	StartTime   time.Time
	EndTime     time.Time
	CompilePass bool
	TpsResult   float64
}

// ProcEngine is the long-running Go process that manages puppets across commits.
// It replaces the old ProcState which tried to run everything in one conversation.
type ProcEngine struct {
	mu sync.Mutex

	Config ProcConfig
	Task   string // the original user task string

	// L1: Go memory state
	Cursor      int    // next commit index to process
	TotalCommits int
	RefOwner    string
	RefName     string
	DevRepo     string // "owner/name"

	// puppet history
	Puppets    []PuppetState
	Current    *PuppetState // the currently running puppet (nil if between puppets)

	// benchmark history
	Benchmarks []BenchmarkResult

	// timing
	StartTime time.Time
	Tick      int

	// control
	stopCh chan struct{}
	done   chan struct{}
	Status ProcStatus
}

func NewProcEngine(task string, cfg ProcConfig) *ProcEngine {
	return &ProcEngine{
		Config:    cfg,
		Task:      task,
		StartTime: time.Now(),
		Status:    PROC_BOOT,
		stopCh:    make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// ---------------------------------------------------------------------------
// Run — the main loop. Called as a goroutine.
// This is the equivalent of MinecraftServer.run() — it never stops until
// all commits are processed or Stop() is called.
// ---------------------------------------------------------------------------

func (pe *ProcEngine) Run() {
	defer close(pe.done)

	log.Printf("proc_engine: starting, task=%q", truncateStr(pe.Task, 80))

	// boot: discover refs, count commits
	if err := pe.boot(); err != nil {
		log.Printf("proc_engine: boot failed: %v", err)
		pe.mu.Lock()
		pe.Status = PROC_FINISHED
		pe.mu.Unlock()
		return
	}

	log.Printf("proc_engine: %s/%s %d commits, dev=%s, starting puppet loop",
		pe.RefOwner, pe.RefName, pe.TotalCommits, pe.DevRepo)

	// main loop: one iteration per commit
	for {
		select {
		case <-pe.stopCh:
			log.Printf("proc_engine: stopped at cursor=%d", pe.Cursor)
			return
		default:
		}

		pe.mu.Lock()
		cursor := pe.Cursor
		total := pe.TotalCommits
		pe.mu.Unlock()

		if cursor >= total {
			log.Printf("proc_engine: all %d commits processed", total)
			pe.mu.Lock()
			pe.Status = PROC_FINISHED
			pe.mu.Unlock()
			return
		}

		// run one commit
		result := pe.runCommit(cursor)

		pe.mu.Lock()
		if result == "merged" {
			pe.Cursor++
			log.Printf("proc_engine: commit %d merged, cursor → %d", cursor, pe.Cursor)
		} else if result == "stopped" {
			pe.mu.Unlock()
			return
		} else {
			// failed — retry same commit with new puppet
			log.Printf("proc_engine: commit %d failed (%s), retrying", cursor, result)
		}
		pe.mu.Unlock()
	}
}

func (pe *ProcEngine) Stop() {
	select {
	case <-pe.stopCh:
	default:
		close(pe.stopCh)
	}
}

func (pe *ProcEngine) Wait() {
	<-pe.done
}

// ---------------------------------------------------------------------------
// boot — discover refs, count commits, register dev repo
// ---------------------------------------------------------------------------

func (pe *ProcEngine) boot() error {
	refs := PRSetup(pe.Task)
	if len(refs) == 0 {
		return fmt.Errorf("no reference repos found")
	}

	pe.mu.Lock()
	pe.RefOwner = refs[0].Owner
	pe.RefName = refs[0].Name
	pe.mu.Unlock()

	commits, err := RefListCommits(pe.RefOwner, pe.RefName)
	if err != nil {
		return fmt.Errorf("list commits: %w", err)
	}

	devRepos := GetDevRepos()
	devRepo := ""
	if len(devRepos) > 0 {
		devRepo = devRepos[0]
	}

	pe.mu.Lock()
	pe.TotalCommits = len(commits)
	pe.DevRepo = devRepo
	pe.Status = PROC_RUNNING
	pe.mu.Unlock()

	return nil
}

// ---------------------------------------------------------------------------
// runCommit — create a puppet, run the PR pipeline for one commit, check
// tps, merge to main if ok. Returns "merged", "failed", or "stopped".
//
// The puppet main conversation lives for exactly this function call.
// When this returns, the puppet is discarded.
// ---------------------------------------------------------------------------

func (pe *ProcEngine) runCommit(cursor int) string {
	repo := pe.RefOwner + "/" + pe.RefName

	// get the commit SHA for this cursor position
	commits, err := RefListCommits(pe.RefOwner, pe.RefName)
	if err != nil || cursor >= len(commits) {
		return "failed: can't list commits"
	}
	sha := commits[cursor]

	puppet := &PuppetState{
		CommitIdx: cursor,
		CommitSHA: sha,
		Status:    "running",
		StartTime: time.Now(),
	}

	pe.mu.Lock()
	pe.Current = puppet
	pe.Puppets = append(pe.Puppets, *puppet)
	pe.mu.Unlock()

	log.Printf("proc_engine: commit %d/%d sha=%s — creating puppet",
		cursor, pe.TotalCommits, sha[:8])

	// 1. create PR process (this pushes hw_probe + ref_bind + dev_from_theorems steps)
	pid, err := PRFork(".", "dev", pe.Task, []RefRepo{{Owner: pe.RefOwner, Name: pe.RefName}})
	if err != nil {
		log.Printf("proc_engine: PRFork failed: %v", err)
		puppet.Status = "failed"
		return "failed: PRFork"
	}
	puppet.PRPid = pid
	puppet.Branch = fmt.Sprintf("einsteinOS/dev-%d", pid)

	// 2. run pipeline steps — Go process drives directly, no main conversation needed
	log.Printf("proc_engine: commit %d pid=%d running pipeline", cursor, pid)

	for stepNum := 0; stepNum < 30; stepNum++ {
		select {
		case <-pe.stopCh:
			puppet.Status = "stopped"
			return "stopped"
		default:
		}

		result := PRDriverStep(pid)
		if result == "" || strings.Contains(result, "no more steps") {
			log.Printf("proc_engine: commit %d pipeline done at step %d", cursor, stepNum+1)
			break
		}
		log.Printf("proc_engine: commit %d step %d: %s",
			cursor, stepNum+1, truncateStr(result, 120))
	}

	puppet.Status = "pipeline_done"

	// 3. check compile status from the last dev step
	pr := GetPR(pid)
	if pr == nil {
		puppet.Status = "failed"
		return "failed: PR not found"
	}

	// check if all dev units passed by looking at the step log
	stepLog := PRStepLog(pid)
	if !strings.Contains(stepLog, "compile=pass") {
		puppet.Status = "failed"
		log.Printf("proc_engine: commit %d compile not pass", cursor)
		return "failed: compile"
	}
	puppet.CompilePass = true

	// 4. milestone benchmark (if applicable)
	if pe.isMilestone(cursor) {
		puppet.Status = "tps_check"
		tps := pe.runBenchmark(pid, puppet.Branch)
		puppet.TpsResult = tps

		pe.mu.Lock()
		pe.Benchmarks = append(pe.Benchmarks, BenchmarkResult{
			Repo:       repo,
			CursorAt:   cursor,
			TokPerSec:  tps,
			RecordedAt: time.Now(),
		})
		pe.mu.Unlock()

		log.Printf("proc_engine: commit %d benchmark tok/s=%.1f", cursor, tps)
		// tps is recorded but NOT a gate — design principle
	}

	// 5. merge PR branch to main
	puppet.Status = "merging"
	if err := pe.mergePRToMain(pid, puppet.Branch, cursor, sha); err != nil {
		puppet.Status = "failed"
		log.Printf("proc_engine: commit %d merge failed: %v", cursor, err)
		return "failed: merge"
	}

	puppet.Status = "merged"
	puppet.EndTime = time.Now()

	// 6. cleanup: mark PR as merged in kernel
	PRMerge(pid)

	log.Printf("proc_engine: commit %d/%d MERGED in %v",
		cursor, pe.TotalCommits, puppet.EndTime.Sub(puppet.StartTime).Truncate(time.Second))

	pe.mu.Lock()
	pe.Current = nil
	pe.mu.Unlock()

	return "merged"
}

// ---------------------------------------------------------------------------
// mergePRToMain — git merge the PR branch to main and push.
// This is the L3→L2 state transition: temp code becomes confirmed code.
// ---------------------------------------------------------------------------

func (pe *ProcEngine) mergePRToMain(pid int64, branch string, cursor int, sha string) error {
	if pe.DevRepo == "" {
		return fmt.Errorf("no dev repo")
	}

	parts := strings.Split(pe.DevRepo, "/")
	repoName := parts[len(parts)-1]
	repoDir := fmt.Sprintf("/home/claude/%s", repoName)

	commitMsg := fmt.Sprintf("merge: commit %d (%s) — pipeline pass", cursor, sha[:8])

	script := fmt.Sprintf(`cd %s || exit 1
TOKEN=$(cat /tmp/.gh_token 2>/dev/null)
git fetch origin 2>/dev/null
git checkout main 2>/dev/null
git merge --no-ff -m "%s" origin/%s 2>&1
git push "https://${TOKEN}@github.com/%s.git" main 2>&1
echo "MERGE_OK"`,
		repoDir, commitMsg, branch, pe.DevRepo)

	child, err := ForkExec(script, repoDir)
	if err != nil {
		return fmt.Errorf("ForkExec: %w", err)
	}
	Wait(child)
	result := GetResult(child)

	if !strings.Contains(result, "MERGE_OK") {
		return fmt.Errorf("merge output: %s", truncateStr(result, 200))
	}

	log.Printf("proc_engine: merged %s to main: %s", branch, truncateStr(result, 100))
	return nil
}

// ---------------------------------------------------------------------------
// runBenchmark — run vllm benchmark on the current branch
// ---------------------------------------------------------------------------

func (pe *ProcEngine) runBenchmark(pid int64, branch string) float64 {
	// push benchmark steps
	PRPushStep(pid, &Step{
		Kind:    STEP_VLLM_UP,
		Owner:   pe.RefOwner,
		Repo:    pe.RefName,
		Command: pe.gpuList(),
	})
	PRPushStep(pid, &Step{
		Kind:  STEP_VLLM_BENCH,
		Owner: pe.RefOwner,
		Repo:  pe.RefName,
	})

	// run them
	for i := 0; i < 5; i++ {
		result := PRDriverStep(pid)
		if result == "" || strings.Contains(result, "no more steps") {
			break
		}
	}

	// parse result
	pr := GetPR(pid)
	if pr != nil && pr.BenchResult != "" {
		bm := parseBenchmarkResult(pr.BenchResult)
		return bm.TokPerSec
	}
	return 0
}

func (pe *ProcEngine) isMilestone(cursor int) bool {
	total := pe.TotalCommits
	if pe.Config.MilestoneMod > 0 {
		return cursor%pe.Config.MilestoneMod == 0
	}
	if total <= 10 {
		return cursor == total-1
	}
	for _, pct := range []int{10, 25, 50, 75, 100} {
		target := total * pct / 100
		if cursor == target {
			return true
		}
	}
	return cursor%50 == 0
}

func (pe *ProcEngine) gpuList() string {
	// look at the most recent puppet's PR
	pe.mu.Lock()
	defer pe.mu.Unlock()
	if pe.Current != nil {
		pr := GetPR(pe.Current.PRPid)
		if pr != nil && pr.GPUUsable != "" {
			return pr.GPUUsable
		}
	}
	return "0"
}

// ---------------------------------------------------------------------------
// Status / observability
// ---------------------------------------------------------------------------

func (pe *ProcEngine) Ps() string {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== ProcEngine status=%s cursor=%d/%d elapsed=%v ===\n",
		pe.Status, pe.Cursor, pe.TotalCommits,
		time.Since(pe.StartTime).Truncate(time.Second)))
	sb.WriteString(fmt.Sprintf("  ref: %s/%s  dev: %s\n", pe.RefOwner, pe.RefName, pe.DevRepo))

	if pe.Current != nil {
		c := pe.Current
		sb.WriteString(fmt.Sprintf("  current: commit %d sha=%s pid=%d status=%s elapsed=%v\n",
			c.CommitIdx, truncateStr(c.CommitSHA, 8), c.PRPid, c.Status,
			time.Since(c.StartTime).Truncate(time.Second)))
	}

	if len(pe.Puppets) > 0 {
		sb.WriteString(fmt.Sprintf("\n[PUPPET HISTORY] (%d total)\n", len(pe.Puppets)))
		start := 0
		if len(pe.Puppets) > 5 {
			start = len(pe.Puppets) - 5
		}
		for i := start; i < len(pe.Puppets); i++ {
			p := pe.Puppets[i]
			duration := ""
			if !p.EndTime.IsZero() {
				duration = p.EndTime.Sub(p.StartTime).Truncate(time.Second).String()
			}
			sb.WriteString(fmt.Sprintf("  commit %d: %s (pid=%d, %s)\n",
				p.CommitIdx, p.Status, p.PRPid, duration))
		}
	}

	if len(pe.Benchmarks) > 0 {
		sb.WriteString("\n[BENCHMARKS]\n")
		for _, bm := range pe.Benchmarks {
			sb.WriteString(fmt.Sprintf("  cursor=%d tok/s=%.1f\n", bm.CursorAt, bm.TokPerSec))
		}
	}

	return sb.String()
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------------------------------------------------------------------------
// Global engine instance
// ---------------------------------------------------------------------------

var (
	engineInstance *ProcEngine
	engineMu      sync.Mutex
)

func EngineStart(task string, cfg ProcConfig) *ProcEngine {
	pe := NewProcEngine(task, cfg)

	engineMu.Lock()
	engineInstance = pe
	engineMu.Unlock()

	go pe.Run()
	return pe
}

func EngineGet() *ProcEngine {
	engineMu.Lock()
	defer engineMu.Unlock()
	return engineInstance
}

func EngineStatus() string {
	pe := EngineGet()
	if pe == nil {
		return "no engine running"
	}
	return pe.Ps()
}

func EngineStop() string {
	pe := EngineGet()
	if pe == nil {
		return "no engine running"
	}
	pe.Stop()

	engineMu.Lock()
	engineInstance = nil
	engineMu.Unlock()

	return "engine stopped"
}
