package kernel

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// ProcDriver — ToolDriver interface for the Proc loop.
//
// Three tools exposed to the main conversation:
//   proc_start  — boot the Proc loop for a PR pid
//   proc_status — read current state (the tick-loop's "status" command)
//   proc_stop   — halt the loop
//
// The main conversation calls these like:
//   {"name":"proc_start","input":{"task":"..."}}
//   {"name":"proc_status","input":{"pid":2}}
//   {"name":"proc_stop","input":{"pid":2}}
// ---------------------------------------------------------------------------

type ProcDriver struct{}

type proc_tool_input struct {
	Task     string  `json:"task"`
	Pid      int64   `json:"pid"`
	Action   string  `json:"action"`
	Interval float64 `json:"interval_min"` // tick interval in minutes (optional)
}

func (d *ProcDriver) Match(name string) bool {
	n := strings.ToLower(name)
	return n == "proc_start" || n == "proc_status" || n == "proc_stop" || n == "proc_tick"
}

func (d *ProcDriver) Execute(name string, inputJSON string, workDir string) string {
	var input proc_tool_input
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return fmt.Sprintf("parse error: %v", err)
	}

	switch strings.ToLower(name) {
	case "proc_start":
		return procDriverStart(input)
	case "proc_status":
		return procDriverStatus(input.Pid)
	case "proc_stop":
		return procDriverStop(input.Pid)
	case "proc_tick":
		return procDriverTick(input.Pid)
	}
	return "unknown proc tool"
}

// ---------------------------------------------------------------------------
// proc_start: boot the full pipeline
//
// 1. Call PRSetup to discover refs
// 2. Call PRFork to create the process
// 3. Start the Proc loop
// ---------------------------------------------------------------------------

func procDriverStart(input proc_tool_input) string {
	task := input.Task
	if task == "" {
		return "proc_start: need task"
	}

	// Step 1: discover ref repos
	refs := PRSetup(task)
	if len(refs) == 0 {
		return "proc_start: 0 reference repos found"
	}

	// Step 2: fork a PR process
	pid, err := PRFork(".", "dev", task, refs)
	if err != nil {
		return fmt.Sprintf("proc_start: %v", err)
	}

	// Step 3: configure and start the Proc loop
	cfg := DefaultProcConfig()
	if input.Interval > 0 {
		cfg.TickInterval = time.Duration(input.Interval * float64(time.Minute))
	}
	// extract ref/dev repo from task if mentioned
	cfg.RefRepo = refs[0].Owner + "/" + refs[0].Name

	ps := ProcStart(pid, cfg)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("proc_start: pid=%d, %d refs, loop started (interval %v)\n",
		pid, len(refs), cfg.TickInterval))
	for _, r := range refs {
		sb.WriteString(fmt.Sprintf("  ref: %s/%s\n", r.Owner, r.Name))
	}

	total, _ := PRStepCount(pid)
	sb.WriteString(fmt.Sprintf("  %d initial steps queued\n", total))

	// show initial state
	sb.WriteString("\n")
	sb.WriteString(ps.ProcPs())

	log.Printf("proc_start: pid=%d refs=%d interval=%v", pid, len(refs), cfg.TickInterval)
	return sb.String()
}

// ---------------------------------------------------------------------------
// proc_status: non-blocking state read
// ---------------------------------------------------------------------------

func procDriverStatus(pid int64) string {
	if pid == 0 {
		return ProcStatusAll()
	}

	ps := ProcGet(pid)
	if ps == nil {
		// maybe there's a PR but no Proc loop
		pr := GetPR(pid)
		if pr == nil {
			return fmt.Sprintf("pid %d: not found", pid)
		}
		return fmt.Sprintf("pid %d: PR exists but no Proc loop running\n%s%s",
			pid, PRStepLog(pid), ConvRegistryStatus())
	}

	var sb strings.Builder
	sb.WriteString(ps.ProcPs())
	sb.WriteString("\n[STEP LOG]\n")
	sb.WriteString(PRStepLog(pid))
	sb.WriteString("\n[CONV REGISTRY]\n")
	sb.WriteString(ConvRegistryStatus())
	sb.WriteString("\n[SCHEDULE]\n")
	sb.WriteString(PRScheduleInfo())

	return sb.String()
}

// ---------------------------------------------------------------------------
// proc_stop: halt the loop gracefully
// ---------------------------------------------------------------------------

func procDriverStop(pid int64) string {
	if pid == 0 {
		ProcStopAll()
		return "all proc loops stopped"
	}

	ps := ProcGet(pid)
	if ps == nil {
		return fmt.Sprintf("pid %d: no proc loop running", pid)
	}

	ps.Stop()

	procInstancesMu.Lock()
	delete(procInstances, pid)
	procInstancesMu.Unlock()

	snap := ps.Snapshot()
	return fmt.Sprintf("pid %d: proc loop stopped at tick %d phase=%s status=%s",
		pid, snap.Tick, snap.Phase, snap.Status)
}

// ---------------------------------------------------------------------------
// proc_tick: force a single tick (for manual / debug use)
// ---------------------------------------------------------------------------

func procDriverTick(pid int64) string {
	ps := ProcGet(pid)
	if ps == nil {
		return fmt.Sprintf("pid %d: no proc loop running", pid)
	}

	result := ps.ForceTick()
	return fmt.Sprintf("tick %d: phase=%s action=%q spawned=%v elapsed=%v",
		result.Tick, result.Phase, result.Action, result.Spawned, result.Elapsed)
}

// ---------------------------------------------------------------------------
// Exported for main conversation use
// ---------------------------------------------------------------------------

func ProcDriverStart(task string) string {
	return procDriverStart(proc_tool_input{Task: task})
}

func ProcDriverStatus(pid int64) string {
	return procDriverStatus(pid)
}

func ProcDriverStop(pid int64) string {
	return procDriverStop(pid)
}

func ProcDriverTick(pid int64) string {
	return procDriverTick(pid)
}
