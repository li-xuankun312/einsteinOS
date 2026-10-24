package kernel

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// ProcStatus — matches state_schema.status in proc_timeline.json
// ---------------------------------------------------------------------------

type ProcStatus int

const (
	PROC_BOOT      ProcStatus = iota // tick 0: push first pipeline
	PROC_RUNNING                     // ticks 1..N-1: read+route+push
	PROC_FINISHING                   // milestone benchmark in progress
	PROC_FINISHED                    // cursor exhausted, all merged
)

func (s ProcStatus) String() string {
	switch s {
	case PROC_BOOT:
		return "BOOT"
	case PROC_RUNNING:
		return "RUNNING"
	case PROC_FINISHING:
		return "FINISHING"
	case PROC_FINISHED:
		return "FINISHED"
	default:
		return "?"
	}
}

// ---------------------------------------------------------------------------
// ProcPhase — finer-grained phase within RUNNING, for observability
// ---------------------------------------------------------------------------

type ProcPhase int

const (
	PHASE_BOOT           ProcPhase = iota
	PHASE_WAIT                     // noop tick: children running
	PHASE_DEV_START                // first dev unit launched
	PHASE_DEV_PROGRESS             // dev units in flight
	PHASE_DEV_DONE                 // all units pass → open PR, advance cursor
	PHASE_PARALLEL                 // next round started while prior PR pending
	PHASE_STEADY_STATE             // repeating pattern
	PHASE_MILESTONE_BENCH          // benchmark pushed at module boundary
	PHASE_BENCHMARK_DONE           // benchmark recorded, continue
	PHASE_EXHAUSTED                // all commits processed
)

func (p ProcPhase) String() string {
	names := []string{
		"boot", "wait", "dev_start", "dev_progress", "dev_done",
		"parallel", "steady_state", "milestone_bench", "benchmark_done", "exhausted",
	}
	if int(p) < len(names) {
		return names[p]
	}
	return "?"
}

// ---------------------------------------------------------------------------
// PRQueueEntry — one open PR being tracked
// ---------------------------------------------------------------------------

type PRQueueEntry struct {
	Pid       int64
	Branch    string
	Repo      string
	CursorAt  int       // which commit index this PR covers
	OpenedAt  time.Time
	Merged    bool
	MergedAt  time.Time
}

// ---------------------------------------------------------------------------
// BenchmarkResult — recorded at module boundaries
// ---------------------------------------------------------------------------

type BenchmarkResult struct {
	Repo         string
	CursorAt     int
	TokPerSec    float64
	LatencyP99ms float64
	RecordedAt   time.Time
}

// ---------------------------------------------------------------------------
// RepoProgress — per-ref-repo cursor + state inside the Proc loop
// ---------------------------------------------------------------------------

type RepoProgress struct {
	Owner       string
	Name        string
	TotalCommits int
	Cursor      int  // next commit index to process (0-based)
	TheoryDone  bool // current cursor's theory finished?
	DevDone     bool // current cursor's dev units all pass?
	DevRunning  bool // dev units currently in flight?
	TheoryBytes int  // how many bytes of theory accumulated
}

// ---------------------------------------------------------------------------
// ProcConfig — tuning knobs for the loop
// ---------------------------------------------------------------------------

type ProcConfig struct {
	TickInterval     time.Duration // how often Proc() is called (default 5min)
	RefRepo          string        // "owner/name" of reference repo
	DevRepo          string        // "owner/name" of development repo
	BranchPattern    string        // e.g. "einsteinOS/dev-{pid}"
	MaxRetry         int           // per-step retry cap (default 5)
	MilestoneMod     int           // push benchmark every N commits (0=auto)
	SkipDocThreshold int           // skip commits with fewer than N source files (default 2)
}

func DefaultProcConfig() ProcConfig {
	return ProcConfig{
		TickInterval:     5 * time.Minute,
		MaxRetry:         5,
		MilestoneMod:     0, // auto-detect module boundaries
		SkipDocThreshold: 2,
	}
}

// ---------------------------------------------------------------------------
// ProcState — all mutable state for one Proc loop instance
// ---------------------------------------------------------------------------

type ProcState struct {
	mu sync.Mutex

	Pid    int64      // the PR process driving this loop
	Config ProcConfig

	Status ProcStatus
	Phase  ProcPhase
	Tick   int

	// per-repo progress (supports multiple ref repos)
	Repos map[string]*RepoProgress // key: "owner/name"

	// PR queue: all PRs opened by this loop
	PRQueue []PRQueueEntry

	// benchmark history
	Benchmarks []BenchmarkResult

	// timing
	StartTime  time.Time
	LastTick   time.Time

	// stop channel
	stopCh chan struct{}
	done   chan struct{}
}

// ---------------------------------------------------------------------------
// Interface: ProcController — external control surface
// ---------------------------------------------------------------------------

// ProcController is the public interface to inspect and control a running Proc loop.
type ProcController interface {
	// Start launches the tick loop in a goroutine. Non-blocking.
	Start()

	// Stop signals the loop to halt after the current tick finishes.
	Stop()

	// Wait blocks until the loop finishes (FINISHED or stopped).
	Wait()

	// Snapshot returns a read-only copy of current state.
	Snapshot() ProcSnapshot

	// ForceTick runs one tick immediately, bypassing the timer. For testing.
	ForceTick() ProcTickResult
}

// ProcSnapshot — immutable snapshot of state, safe to read concurrently.
type ProcSnapshot struct {
	Pid        int64
	Status     ProcStatus
	Phase      ProcPhase
	Tick       int
	Repos      map[string]RepoProgress // value copy
	PRQueue    []PRQueueEntry
	Benchmarks []BenchmarkResult
	Elapsed    time.Duration
}

// ProcTickResult — what happened in one tick, for logging / testing.
type ProcTickResult struct {
	Tick    int
	Phase   ProcPhase
	Action  string // human-readable action taken
	Spawned []string
	Elapsed time.Duration
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

func NewProc(pid int64, cfg ProcConfig) *ProcState {
	return &ProcState{
		Pid:       pid,
		Config:    cfg,
		Status:    PROC_BOOT,
		Phase:     PHASE_BOOT,
		Tick:      0,
		Repos:     make(map[string]*RepoProgress),
		StartTime: time.Now(),
		stopCh:    make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// ---------------------------------------------------------------------------
// ProcController implementation
// ---------------------------------------------------------------------------

func (ps *ProcState) Start() {
	go ps.loop()
}

func (ps *ProcState) Stop() {
	select {
	case <-ps.stopCh:
	default:
		close(ps.stopCh)
	}
}

func (ps *ProcState) Wait() {
	<-ps.done
}

func (ps *ProcState) Snapshot() ProcSnapshot {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	repos := make(map[string]RepoProgress, len(ps.Repos))
	for k, v := range ps.Repos {
		repos[k] = *v
	}
	prq := make([]PRQueueEntry, len(ps.PRQueue))
	copy(prq, ps.PRQueue)
	bm := make([]BenchmarkResult, len(ps.Benchmarks))
	copy(bm, ps.Benchmarks)

	return ProcSnapshot{
		Pid:        ps.Pid,
		Status:     ps.Status,
		Phase:      ps.Phase,
		Tick:       ps.Tick,
		Repos:      repos,
		PRQueue:    prq,
		Benchmarks: bm,
		Elapsed:    time.Since(ps.StartTime),
	}
}

func (ps *ProcState) ForceTick() ProcTickResult {
	return ps.tick()
}

// ---------------------------------------------------------------------------
// Main loop
// ---------------------------------------------------------------------------

func (ps *ProcState) loop() {
	defer close(ps.done)

	log.Printf("proc: pid=%d loop started, interval=%v", ps.Pid, ps.Config.TickInterval)

	// tick 0 immediately
	result := ps.tick()
	log.Printf("proc: tick %d phase=%s action=%q", result.Tick, result.Phase, result.Action)

	ticker := time.NewTicker(ps.Config.TickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ps.stopCh:
			log.Printf("proc: pid=%d stopped at tick %d", ps.Pid, ps.currentTick())
			return
		case <-ticker.C:
			if ps.isFinished() {
				log.Printf("proc: pid=%d FINISHED at tick %d", ps.Pid, ps.currentTick())
				return
			}
			result := ps.tick()
			log.Printf("proc: tick %d phase=%s action=%q spawned=%v",
				result.Tick, result.Phase, result.Action, result.Spawned)
		}
	}
}

func (ps *ProcState) currentTick() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.Tick
}

func (ps *ProcState) isFinished() bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.Status == PROC_FINISHED
}

// ---------------------------------------------------------------------------
// tick — the Proc() function. One call = one tick.
//
// "主对话是 Proc(): 每个 tick 读 registry，推 step，报 status。
//  不评判、不交互、不等待。"
// ---------------------------------------------------------------------------

func (ps *ProcState) tick() ProcTickResult {
	start := time.Now()

	ps.mu.Lock()
	ps.Tick++
	tickNum := ps.Tick
	ps.LastTick = start
	ps.mu.Unlock()

	// 1. read state
	snap := ps.readState()

	// 2. decide + act
	action, spawned := ps.route(snap)

	// 3. update phase
	ps.mu.Lock()
	phase := ps.Phase
	ps.mu.Unlock()

	return ProcTickResult{
		Tick:    tickNum,
		Phase:   phase,
		Action:  action,
		Spawned: spawned,
		Elapsed: time.Since(start),
	}
}

// ---------------------------------------------------------------------------
// readState — read conv_registry, cursor, pr_queue without blocking
// ---------------------------------------------------------------------------

type tickState struct {
	convs       []ConvRecord
	repos       map[string]*RepoProgress
	prQueue     []PRQueueEntry
	benchmarks  []BenchmarkResult
	status      ProcStatus
	tick        int
}

func (ps *ProcState) readState() tickState {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	// copy repos
	repos := make(map[string]*RepoProgress, len(ps.Repos))
	for k, v := range ps.Repos {
		cp := *v
		repos[k] = &cp
	}

	return tickState{
		convs:      readConvRegistry(),
		repos:      repos,
		prQueue:    append([]PRQueueEntry{}, ps.PRQueue...),
		benchmarks: append([]BenchmarkResult{}, ps.Benchmarks...),
		status:     ps.Status,
		tick:       ps.Tick,
	}
}

// readConvRegistry: snapshot the global conv registry
func readConvRegistry() []ConvRecord {
	convRegistryMu.Lock()
	defer convRegistryMu.Unlock()
	out := make([]ConvRecord, len(convRegistry))
	copy(out, convRegistry)
	return out
}

// ---------------------------------------------------------------------------
// route — the decision engine. Reads state, returns action string + spawned list.
//
// Design principle: "cursor驱动进度。不是 eval 子对话判断 sufficient/continue，
// 是 cursor 机械前进。"
// ---------------------------------------------------------------------------

func (ps *ProcState) route(snap tickState) (string, []string) {
	switch snap.status {
	case PROC_BOOT:
		return ps.routeBoot(snap)
	case PROC_RUNNING:
		return ps.routeRunning(snap)
	case PROC_FINISHING:
		return ps.routeFinishing(snap)
	case PROC_FINISHED:
		return "FINISHED", nil
	}
	return "noop", nil
}

// --- BOOT: push first pipeline -----------------------------------------------

func (ps *ProcState) routeBoot(snap tickState) (string, []string) {
	pr := GetPR(ps.Pid)
	if pr == nil {
		return "boot: no PR process", nil
	}

	var spawned []string

	// initialize repo progress from RefRepos
	ps.mu.Lock()
	for _, ref := range pr.RefRepos {
		key := ref.Owner + "/" + ref.Name
		if _, ok := ps.Repos[key]; !ok {
			commits, err := RefListCommits(ref.Owner, ref.Name)
			total := 0
			if err == nil {
				total = len(commits)
			}
			ps.Repos[key] = &RepoProgress{
				Owner:        ref.Owner,
				Name:         ref.Name,
				TotalCommits: total,
				Cursor:       0,
			}
			log.Printf("proc: boot: repo %s → %d commits", key, total)
		}
	}
	ps.Status = PROC_RUNNING
	ps.Phase = PHASE_WAIT
	ps.mu.Unlock()

	// push initial steps: HW_PROBE + REF_BIND(start=0) + DEV_FROM_THEOREMS
	// (PRFork already pushed these, but if we're bootstrapping fresh, ensure they exist)
	step := PRCurrentStep(ps.Pid)
	if step == nil {
		// no steps yet — push the initial pipeline
		PRPushStep(ps.Pid, &Step{Kind: STEP_HW_PROBE})
		spawned = append(spawned, "hw_probe")
		for _, ref := range pr.RefRepos {
			PRPushStep(ps.Pid, &Step{
				Kind:    STEP_REF_BIND,
				Owner:   ref.Owner,
				Repo:    ref.Name,
				Command: "start=0",
			})
			PRPushStep(ps.Pid, &Step{
				Kind:  STEP_DEV_FROM_THEOREMS,
				Owner: ref.Owner,
				Repo:  ref.Name,
			})
			spawned = append(spawned, fmt.Sprintf("ref_bind(%s,0)", ref.Name))
		}
	} else {
		spawned = append(spawned, "steps_already_queued")
	}

	return "boot: initialized repos, pushed first pipeline", spawned
}

// --- RUNNING: the main steady-state loop -------------------------------------

func (ps *ProcState) routeRunning(snap tickState) (string, []string) {
	// check: is the current step still executing?
	step := PRCurrentStep(ps.Pid)
	if step == nil {
		// no more steps — check if all repos exhausted
		return ps.checkExhausted(snap)
	}

	// step exists but hasn't started → run it
	if step.Started.IsZero() {
		return ps.runNextStep(snap)
	}

	// step is in progress (started but not finished) → noop, wait
	if step.Finished.IsZero() {
		ps.mu.Lock()
		ps.Phase = PHASE_WAIT
		ps.mu.Unlock()
		return "wait: step in progress", nil
	}

	// step finished → run the next one
	return ps.runNextStep(snap)
}

// runNextStep: execute the next pending step, update phase accordingly.
func (ps *ProcState) runNextStep(snap tickState) (string, []string) {
	step := PRCurrentStep(ps.Pid)
	if step == nil {
		return ps.checkExhausted(snap)
	}

	// update phase based on step kind
	ps.mu.Lock()
	switch step.Kind {
	case STEP_HW_PROBE:
		ps.Phase = PHASE_BOOT
	case STEP_REF_BIND:
		ps.Phase = PHASE_WAIT
	case STEP_DEV_FROM_THEOREMS:
		ps.Phase = PHASE_DEV_START
	case STEP_VLLM_UP, STEP_VLLM_BENCH:
		ps.Phase = PHASE_MILESTONE_BENCH
	case STEP_OPEN_PR:
		ps.Phase = PHASE_DEV_DONE
	default:
		ps.Phase = PHASE_DEV_PROGRESS
	}
	ps.mu.Unlock()

	// execute
	result, err := PRRunNext(ps.Pid)

	var spawned []string
	action := fmt.Sprintf("ran %s", step_name(step.Kind))
	if err != nil {
		action += fmt.Sprintf(" (error: %v)", err)
	}

	// post-step routing: after certain steps complete, push follow-ups
	ps.postStep(step, result, err, &spawned)

	// check if the step queue now has more work
	next := PRCurrentStep(ps.Pid)
	if next == nil {
		// pipeline segment done — maybe advance cursor and push next round
		ps.maybeAdvanceCursor(step, &action, &spawned)
	}

	return action, spawned
}

// postStep: after a step finishes, update repo progress and push next steps.
func (ps *ProcState) postStep(step *Step, result string, err error, spawned *[]string) {
	repo := step.Owner + "/" + step.Repo

	ps.mu.Lock()
	defer ps.mu.Unlock()

	rp, ok := ps.Repos[repo]
	if !ok {
		return
	}

	switch step.Kind {
	case STEP_REF_BIND:
		// theory finished for current cursor
		rp.TheoryDone = true
		rp.TheoryBytes = len(theorem_get_all(repo))
		log.Printf("proc: %s cursor=%d theory done (%d bytes)", repo, rp.Cursor, rp.TheoryBytes)

	case STEP_DEV_FROM_THEOREMS:
		rp.DevDone = (err == nil)
		rp.DevRunning = false
		if err == nil {
			log.Printf("proc: %s cursor=%d dev done", repo, rp.Cursor)
		} else {
			log.Printf("proc: %s cursor=%d dev failed: %v", repo, rp.Cursor, err)
		}

	case STEP_VLLM_BENCH:
		// parse benchmark result
		bm := parseBenchmarkResult(result)
		bm.Repo = repo
		bm.CursorAt = rp.Cursor
		bm.RecordedAt = time.Now()
		ps.Benchmarks = append(ps.Benchmarks, bm)
		log.Printf("proc: %s benchmark tok/s=%.1f p99=%.1fms",
			repo, bm.TokPerSec, bm.LatencyP99ms)

	case STEP_OPEN_PR:
		// record PR in queue
		branch := fmt.Sprintf("einsteinOS/dev-%d", ps.Pid)
		ps.PRQueue = append(ps.PRQueue, PRQueueEntry{
			Pid:      ps.Pid,
			Branch:   branch,
			Repo:     repo,
			CursorAt: rp.Cursor,
			OpenedAt: time.Now(),
		})
		ps.Phase = PHASE_DEV_DONE
		log.Printf("proc: %s PR opened at cursor=%d", repo, rp.Cursor)
	}
}

// maybeAdvanceCursor: after the pipeline segment for current cursor is done,
// advance cursor and push next round.
//
// "cursor = 1, push STEP_REF_BIND(start=1) + STEP_DEV_THMS"
func (ps *ProcState) maybeAdvanceCursor(lastStep *Step, action *string, spawned *[]string) {
	repo := lastStep.Owner + "/" + lastStep.Repo

	ps.mu.Lock()
	rp, ok := ps.Repos[repo]
	if !ok {
		ps.mu.Unlock()
		return
	}

	// only advance after dev is done (or after benchmark for milestone rounds)
	if !rp.DevDone {
		ps.mu.Unlock()
		return
	}

	// advance cursor
	rp.Cursor++
	rp.TheoryDone = false
	rp.DevDone = false

	cursor := rp.Cursor
	total := rp.TotalCommits
	owner := rp.Owner
	name := rp.Name
	ps.Phase = PHASE_PARALLEL
	ps.mu.Unlock()

	if cursor >= total {
		*action += fmt.Sprintf("; %s exhausted (%d/%d)", repo, cursor, total)
		return
	}

	// push next round
	log.Printf("proc: %s cursor %d → %d, pushing next pipeline", repo, cursor-1, cursor)
	PRPushStep(ps.Pid, &Step{
		Kind:    STEP_REF_BIND,
		Owner:   owner,
		Repo:    name,
		Command: fmt.Sprintf("start=%d", cursor),
	})
	PRPushStep(ps.Pid, &Step{
		Kind:  STEP_DEV_FROM_THEOREMS,
		Owner: owner,
		Repo:  name,
	})
	*spawned = append(*spawned, fmt.Sprintf("ref_bind(%s,%d)", name, cursor))

	// milestone benchmark: push at module boundaries
	if ps.isMilestone(cursor, total) {
		PRPushStep(ps.Pid, &Step{
			Kind:    STEP_VLLM_UP,
			Owner:   owner,
			Repo:    name,
			Command: ps.gpuList(),
		})
		PRPushStep(ps.Pid, &Step{
			Kind:  STEP_VLLM_BENCH,
			Owner: owner,
			Repo:  name,
		})
		*spawned = append(*spawned, "vllm_bench")

		ps.mu.Lock()
		ps.Status = PROC_FINISHING
		ps.Phase = PHASE_MILESTONE_BENCH
		ps.mu.Unlock()
	}

	// also push OPEN_PR for the just-completed round
	PRPushStep(ps.Pid, &Step{
		Kind:    STEP_OPEN_PR,
		Owner:   owner,
		Repo:    name,
		Command: ps.lastBenchString(),
	})
	*spawned = append(*spawned, "open_pr")

	*action += fmt.Sprintf("; cursor→%d, pushed next pipeline", cursor)
}

// --- FINISHING: benchmark in progress ----------------------------------------

func (ps *ProcState) routeFinishing(snap tickState) (string, []string) {
	step := PRCurrentStep(ps.Pid)
	if step == nil {
		// benchmark steps done → back to RUNNING
		ps.mu.Lock()
		ps.Status = PROC_RUNNING
		ps.Phase = PHASE_BENCHMARK_DONE
		ps.mu.Unlock()
		return "benchmark done, resuming", nil
	}

	// still have steps (benchmark or next pipeline) → run them
	return ps.runNextStep(snap)
}

// --- EXHAUSTED check ---------------------------------------------------------

func (ps *ProcState) checkExhausted(snap tickState) (string, []string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	allDone := true
	for _, rp := range ps.Repos {
		if rp.Cursor < rp.TotalCommits {
			allDone = false
			break
		}
	}

	if allDone {
		ps.Status = PROC_FINISHED
		ps.Phase = PHASE_EXHAUSTED
		return "FINISHED: all commits processed", nil
	}

	// not all done but no steps queued — shouldn't happen, but be safe
	ps.Phase = PHASE_WAIT
	return "wait: no steps queued", nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// isMilestone: should we push a benchmark at this cursor position?
// Default heuristic: every 50 commits, or at 10%, 25%, 50%, 75%, 100%.
func (ps *ProcState) isMilestone(cursor, total int) bool {
	if ps.Config.MilestoneMod > 0 {
		return cursor%ps.Config.MilestoneMod == 0
	}
	// auto: at 10%, 25%, 50%, 75%, last
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

func (ps *ProcState) gpuList() string {
	pr := GetPR(ps.Pid)
	if pr != nil && pr.GPUUsable != "" {
		return pr.GPUUsable
	}
	return "0"
}

func (ps *ProcState) lastBenchString() string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.Benchmarks) == 0 {
		return ""
	}
	b := ps.Benchmarks[len(ps.Benchmarks)-1]
	return fmt.Sprintf("tok_s=%.1f", b.TokPerSec)
}

func parseBenchmarkResult(text string) BenchmarkResult {
	var bm BenchmarkResult
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "tok_s=") || strings.HasPrefix(line, "tok/s=") {
			fmt.Sscanf(strings.TrimLeft(line, "tok_s=tok/s="), "%f", &bm.TokPerSec)
		}
		if strings.HasPrefix(line, "latency_p99_ms=") || strings.HasPrefix(line, "p99_ms=") {
			fmt.Sscanf(strings.TrimLeft(line, "latency_p99_ms=p99_ms="), "%f", &bm.LatencyP99ms)
		}
	}
	return bm
}

// ---------------------------------------------------------------------------
// ProcPs — human-readable status dump, like ps(1) for the Proc loop
// ---------------------------------------------------------------------------

func (ps *ProcState) ProcPs() string {
	snap := ps.Snapshot()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== Proc pid=%d status=%s phase=%s tick=%d elapsed=%v ===\n",
		snap.Pid, snap.Status, snap.Phase, snap.Tick, snap.Elapsed.Truncate(time.Second)))

	sb.WriteString("\n[REPOS]\n")
	for key, rp := range snap.Repos {
		sb.WriteString(fmt.Sprintf("  %-30s cursor=%d/%d theory=%v dev=%v (%d bytes)\n",
			key, rp.Cursor, rp.TotalCommits, rp.TheoryDone, rp.DevDone, rp.TheoryBytes))
	}

	if len(snap.PRQueue) > 0 {
		sb.WriteString("\n[PR QUEUE]\n")
		for _, pr := range snap.PRQueue {
			merged := "open"
			if pr.Merged {
				merged = fmt.Sprintf("merged %v", pr.MergedAt.Format("15:04"))
			}
			sb.WriteString(fmt.Sprintf("  %-30s cursor=%d %s\n", pr.Branch, pr.CursorAt, merged))
		}
	}

	if len(snap.Benchmarks) > 0 {
		sb.WriteString("\n[BENCHMARKS]\n")
		for _, bm := range snap.Benchmarks {
			sb.WriteString(fmt.Sprintf("  %-20s cursor=%d tok/s=%.1f p99=%.1fms\n",
				bm.Repo, bm.CursorAt, bm.TokPerSec, bm.LatencyP99ms))
		}
	}

	return sb.String()
}

// ---------------------------------------------------------------------------
// Global: running Proc instances
// ---------------------------------------------------------------------------

var (
	procInstances   = make(map[int64]*ProcState)
	procInstancesMu sync.Mutex
)

// ProcStart creates and starts a new Proc loop for an existing PR pid.
func ProcStart(pid int64, cfg ProcConfig) *ProcState {
	ps := NewProc(pid, cfg)

	procInstancesMu.Lock()
	procInstances[pid] = ps
	procInstancesMu.Unlock()

	ps.Start()
	return ps
}

// ProcGet returns the running Proc for a pid, or nil.
func ProcGet(pid int64) *ProcState {
	procInstancesMu.Lock()
	defer procInstancesMu.Unlock()
	return procInstances[pid]
}

// ProcStatus returns human-readable status of all running Proc loops.
func ProcStatusAll() string {
	procInstancesMu.Lock()
	defer procInstancesMu.Unlock()

	if len(procInstances) == 0 {
		return "no proc loops running"
	}

	var sb strings.Builder
	for pid, ps := range procInstances {
		sb.WriteString(fmt.Sprintf("--- pid %d ---\n", pid))
		sb.WriteString(ps.ProcPs())
		sb.WriteString("\n")
	}
	return sb.String()
}

// ProcStopAll stops all running Proc loops.
func ProcStopAll() {
	procInstancesMu.Lock()
	instances := make([]*ProcState, 0, len(procInstances))
	for _, ps := range procInstances {
		instances = append(instances, ps)
	}
	procInstancesMu.Unlock()

	for _, ps := range instances {
		ps.Stop()
	}
}
