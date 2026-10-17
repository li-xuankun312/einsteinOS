package compact

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

type SchedulerConfig struct {
	Dir             string
	IndexID         string
	CheckInterval   time.Duration
	MinIdleTime     time.Duration
	MaxConcurrent   int
	CompactorCfg    CompactorConfig
	PlannerCfg      PlannerConfig
	AutoRun         bool
}

func DefaultSchedulerConfig(dir string) SchedulerConfig {
	return SchedulerConfig{
		Dir:           dir,
		IndexID:       "hnsw",
		CheckInterval: 30 * time.Second,
		MinIdleTime:   5 * time.Second,
		MaxConcurrent: 1,
		CompactorCfg:  DefaultCompactorConfig(dir),
		PlannerCfg:    DefaultPlannerConfig(dir),
		AutoRun:       true,
	}
}

type CompactionJob struct {
	ID        int64
	Action    PlanAction
	State     JobState
	StartedAt time.Time
	EndedAt   time.Time
	Error     error
}

type JobState int

const (
	JobPending  JobState = 0
	JobRunning  JobState = 1
	JobDone     JobState = 2
	JobFailed   JobState = 3
	JobCanceled JobState = 4
)

func (s JobState) String() string {
	switch s {
	case JobPending:
		return "pending"
	case JobRunning:
		return "running"
	case JobDone:
		return "done"
	case JobFailed:
		return "failed"
	case JobCanceled:
		return "canceled"
	default:
		return "unknown"
	}
}

type Scheduler struct {
	cfg        SchedulerConfig
	compactor  *Compactor
	planner    *Planner
	mu         sync.Mutex
	jobs       []*CompactionJob
	nextJobID  atomic.Int64
	running    atomic.Int32
	stopCh     chan struct{}
	stopped    bool
	lastCheck  time.Time
	lastAction time.Time
	cycles     atomic.Int64
	totalJobs  atomic.Int64
	failedJobs atomic.Int64
}

func NewScheduler(cfg SchedulerConfig) *Scheduler {
	return &Scheduler{
		cfg:       cfg,
		compactor: NewCompactor(cfg.CompactorCfg),
		planner:   NewPlanner(cfg.PlannerCfg),
		stopCh:    make(chan struct{}),
	}
}

func (s *Scheduler) Start() {
	if s.cfg.AutoRun {
		go s.loop()
	}
}

func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	close(s.stopCh)
}

func (s *Scheduler) loop() {
	ticker := time.NewTicker(s.cfg.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.check()
		}
	}
}

func (s *Scheduler) check() {
	s.cycles.Add(1)
	s.lastCheck = time.Now()

	if int(s.running.Load()) >= s.cfg.MaxConcurrent {
		return
	}

	plan, err := s.planner.Plan()
	if err != nil {
		log.Printf("[scheduler] plan error: %v", err)
		return
	}

	if plan.IsEmpty() {
		return
	}

	for _, action := range plan.Actions {
		if int(s.running.Load()) >= s.cfg.MaxConcurrent {
			break
		}
		s.scheduleJob(action)
	}
}

func (s *Scheduler) scheduleJob(action PlanAction) {
	job := &CompactionJob{
		ID:     s.nextJobID.Add(1),
		Action: action,
		State:  JobPending,
	}

	s.mu.Lock()
	s.jobs = append(s.jobs, job)
	s.mu.Unlock()

	s.totalJobs.Add(1)

	go s.runJob(job)
}

func (s *Scheduler) runJob(job *CompactionJob) {
	s.running.Add(1)
	defer s.running.Add(-1)

	job.State = JobRunning
	job.StartedAt = time.Now()

	log.Printf("[scheduler] job %d: %s — %s", job.ID, job.Action.Type, job.Action.Reason)

	shouldAbort := func() bool {
		select {
		case <-s.stopCh:
			return true
		default:
			return false
		}
	}

	_, err := s.compactor.RunCycle(shouldAbort)
	job.EndedAt = time.Now()

	if err != nil {
		job.State = JobFailed
		job.Error = err
		s.failedJobs.Add(1)
		log.Printf("[scheduler] job %d failed: %v", job.ID, err)
	} else {
		job.State = JobDone
		s.lastAction = time.Now()
		log.Printf("[scheduler] job %d completed in %v", job.ID, job.EndedAt.Sub(job.StartedAt))
	}
}

func (s *Scheduler) TriggerCompaction() error {
	plan, err := s.planner.Plan()
	if err != nil {
		return err
	}
	if plan.IsEmpty() {
		return nil
	}
	for _, action := range plan.Actions {
		s.scheduleJob(action)
	}
	return nil
}

func (s *Scheduler) ForceCompaction(action Action) error {
	pa := PlanAction{
		Type:     action,
		Priority: 100,
		Reason:   "manual trigger",
	}
	s.scheduleJob(pa)
	return nil
}

func (s *Scheduler) Jobs() []*CompactionJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*CompactionJob, len(s.jobs))
	copy(out, s.jobs)
	return out
}

func (s *Scheduler) ActiveJobs() int {
	return int(s.running.Load())
}

func (s *Scheduler) PendingJobs() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, j := range s.jobs {
		if j.State == JobPending || j.State == JobRunning {
			count++
		}
	}
	return count
}

func (s *Scheduler) Stats() map[string]interface{} {
	return map[string]interface{}{
		"cycles":      s.cycles.Load(),
		"total_jobs":  s.totalJobs.Load(),
		"failed_jobs": s.failedJobs.Load(),
		"running":     s.running.Load(),
		"last_check":  s.lastCheck.Format(time.RFC3339),
		"last_action": s.lastAction.Format(time.RFC3339),
		"compactor":   s.compactor.Stats(),
	}
}

func (s *Scheduler) ClearHistory() {
	s.mu.Lock()
	defer s.mu.Unlock()
	var active []*CompactionJob
	for _, j := range s.jobs {
		if j.State == JobPending || j.State == JobRunning {
			active = append(active, j)
		}
	}
	s.jobs = active
}

func (s *Scheduler) Summary() string {
	stats := s.Stats()
	return fmt.Sprintf("Scheduler: %d cycles, %d jobs (%d failed), %d running",
		stats["cycles"], stats["total_jobs"], stats["failed_jobs"], stats["running"])
}
