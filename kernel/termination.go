package kernel

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

type ToolEvent struct {
	ToolName string
	InputRaw string
	Output   string
	Round    int
	HasTools bool // this round had tool calls (vs text-only)
}

type StopSignal struct {
	Reason string
	Data   map[string]string // structured data extracted from tool calls
}

type TerminationCondition interface {
	Check(event ToolEvent) *StopSignal
	Reset()
	String() string
}

// --- MaxRounds: hard ceiling, replaces roleMaxRounds hardcode ---

type MaxRounds struct {
	Limit   int
	current int
}

func (c *MaxRounds) Check(e ToolEvent) *StopSignal {
	c.current = e.Round
	if c.current >= c.Limit {
		return &StopSignal{Reason: fmt.Sprintf("max_rounds=%d", c.Limit)}
	}
	return nil
}
func (c *MaxRounds) Reset()        { c.current = 0 }
func (c *MaxRounds) String() string { return fmt.Sprintf("MaxRounds(%d)", c.Limit) }

// --- Timeout: wall-clock limit ---

type Timeout struct {
	Duration time.Duration
	started  time.Time
}

func (c *Timeout) Check(_ ToolEvent) *StopSignal {
	if c.started.IsZero() {
		c.started = time.Now()
	}
	if time.Since(c.started) >= c.Duration {
		return &StopSignal{Reason: fmt.Sprintf("timeout=%v", c.Duration)}
	}
	return nil
}
func (c *Timeout) Reset()        { c.started = time.Time{} }
func (c *Timeout) String() string { return fmt.Sprintf("Timeout(%v)", c.Duration) }

// --- ConsecutiveTextRounds: agent stopped using tools = done working ---
// This is the key insight: when an agent finishes its task, it naturally
// stops calling tools and writes a text summary. We detect that transition
// without any prompt instruction.

type ConsecutiveTextRounds struct {
	Threshold int
	count     int
}

func (c *ConsecutiveTextRounds) Check(e ToolEvent) *StopSignal {
	if e.HasTools {
		c.count = 0
	} else {
		c.count++
	}
	if c.count >= c.Threshold {
		return &StopSignal{Reason: fmt.Sprintf("idle=%d_text_rounds", c.count)}
	}
	return nil
}
func (c *ConsecutiveTextRounds) Reset()        { c.count = 0 }
func (c *ConsecutiveTextRounds) String() string { return fmt.Sprintf("ConsecutiveTextRounds(%d)", c.Threshold) }

// --- BashExitZero: a specific bash command succeeded ---
// Matches bash tool calls where the command contains `pattern` and
// the output does NOT contain common failure markers.

type BashExitZero struct {
	Pattern string // e.g. "git push", "make", "go build"
	signal  *StopSignal
}

func (c *BashExitZero) Check(e ToolEvent) *StopSignal {
	if e.ToolName != "bash" && e.ToolName != "terminal" {
		return nil
	}
	cmd := extractBashCommand(e.InputRaw)
	if !strings.Contains(cmd, c.Pattern) {
		return nil
	}
	// check output for failure markers
	out := strings.ToLower(e.Output)
	failMarkers := []string{"error:", "fatal:", "failed", "permission denied",
		"command not found", "no such file", "exit code", "reject"}
	for _, m := range failMarkers {
		if strings.Contains(out, m) {
			return nil
		}
	}
	c.signal = &StopSignal{
		Reason: fmt.Sprintf("bash_ok:%s", c.Pattern),
		Data:   map[string]string{"command": cmd, "output_prefix": truncForSignal(e.Output, 500)},
	}
	return c.signal
}
func (c *BashExitZero) Reset()        { c.signal = nil }
func (c *BashExitZero) String() string { return fmt.Sprintf("BashExitZero(%q)", c.Pattern) }

// --- FileWriteCount: expected number of files written via create/edit tools ---

type FileWriteCount struct {
	Expected int
	seen     map[string]bool
}

func (c *FileWriteCount) Check(e ToolEvent) *StopSignal {
	if c.seen == nil {
		c.seen = make(map[string]bool)
	}
	switch e.ToolName {
	case "create", "str_replace_editor", "write_to_file", "create_file":
		path := extractFilePath(e.InputRaw)
		if path != "" {
			c.seen[path] = true
		}
	}
	if len(c.seen) >= c.Expected {
		files := make([]string, 0, len(c.seen))
		for f := range c.seen {
			files = append(files, f)
		}
		return &StopSignal{
			Reason: fmt.Sprintf("files_written=%d/%d", len(c.seen), c.Expected),
			Data:   map[string]string{"files": strings.Join(files, ",")},
		}
	}
	return nil
}
func (c *FileWriteCount) Reset()        { c.seen = nil }
func (c *FileWriteCount) String() string { return fmt.Sprintf("FileWriteCount(%d)", c.Expected) }

// --- CompileResult: extract compile pass/fail from build commands ---
// Observes bash(make/go build/nvcc) output. Doesn't require the agent
// to "say" compile=pass — the kernel reads the tool result directly.

type CompileResult struct {
	BuildCmd string // "make", "go build", "nvcc"
	result   *StopSignal
}

func (c *CompileResult) Check(e ToolEvent) *StopSignal {
	if e.ToolName != "bash" && e.ToolName != "terminal" {
		return nil
	}
	cmd := extractBashCommand(e.InputRaw)
	if !strings.Contains(cmd, c.BuildCmd) {
		return nil
	}
	out := e.Output
	passed := !containsAny(strings.ToLower(out), []string{
		"error:", "fatal:", "undefined reference", "cannot find",
		"failed", "build failed", "compilation error",
	})
	c.result = &StopSignal{
		Reason: fmt.Sprintf("compile:%s", c.BuildCmd),
		Data: map[string]string{
			"compile": boolStr(passed),
			"command": cmd,
			"output":  truncForSignal(out, 1000),
		},
	}
	return c.result
}
func (c *CompileResult) Reset()        { c.result = nil }
func (c *CompileResult) String() string { return fmt.Sprintf("CompileResult(%q)", c.BuildCmd) }

// --- Or: any condition triggers termination ---

type Or struct {
	Conditions []TerminationCondition
}

func OrConditions(conds ...TerminationCondition) *Or {
	return &Or{Conditions: conds}
}

func (c *Or) Check(e ToolEvent) *StopSignal {
	for _, cond := range c.Conditions {
		if sig := cond.Check(e); sig != nil {
			return sig
		}
	}
	return nil
}
func (c *Or) Reset() {
	for _, cond := range c.Conditions {
		cond.Reset()
	}
}
func (c *Or) String() string {
	parts := make([]string, len(c.Conditions))
	for i, cond := range c.Conditions {
		parts[i] = cond.String()
	}
	return fmt.Sprintf("Or(%s)", strings.Join(parts, ", "))
}

// --- And: all conditions must fire ---

type And struct {
	Conditions []TerminationCondition
	fired      map[int]*StopSignal
}

func AndConditions(conds ...TerminationCondition) *And {
	return &And{Conditions: conds, fired: make(map[int]*StopSignal)}
}

func (c *And) Check(e ToolEvent) *StopSignal {
	for i, cond := range c.Conditions {
		if _, ok := c.fired[i]; ok {
			continue
		}
		if sig := cond.Check(e); sig != nil {
			c.fired[i] = sig
		}
	}
	if len(c.fired) == len(c.Conditions) {
		merged := &StopSignal{Data: map[string]string{}}
		reasons := make([]string, 0)
		for _, sig := range c.fired {
			reasons = append(reasons, sig.Reason)
			for k, v := range sig.Data {
				merged.Data[k] = v
			}
		}
		merged.Reason = strings.Join(reasons, " & ")
		return merged
	}
	return nil
}
func (c *And) Reset() {
	c.fired = make(map[int]*StopSignal)
	for _, cond := range c.Conditions {
		cond.Reset()
	}
}
func (c *And) String() string {
	parts := make([]string, len(c.Conditions))
	for i, cond := range c.Conditions {
		parts[i] = cond.String()
	}
	return fmt.Sprintf("And(%s)", strings.Join(parts, ", "))
}

// --- slot-based termination passing ---
// Same pattern as ForkRole: parent sets a global slot before Fork,
// child consumes it in completionLoop. No pid lookup, no race.
// Protected by forkConvMu (shared with ForkRole for atomicity).

var pendingTermination TerminationCondition // set by parent, consumed by child

func SetPendingTermination(cond TerminationCondition) {
	forkConvMu.Lock()
	pendingTermination = cond
	forkConvMu.Unlock()
}

func ConsumeTermination() TerminationCondition {
	forkConvMu.Lock()
	cond := pendingTermination
	pendingTermination = nil
	forkConvMu.Unlock()
	return cond
}

// --- per-pid signal storage (written after child exits, read by parent) ---

var (
	pidSignals   = make(map[int64]*StopSignal)
	pidSignalsMu sync.Mutex
)

func StoreSignal(pid int64, sig *StopSignal) {
	pidSignalsMu.Lock()
	pidSignals[pid] = sig
	pidSignalsMu.Unlock()
}

func GetSignal(pid int64) *StopSignal {
	pidSignalsMu.Lock()
	defer pidSignalsMu.Unlock()
	return pidSignals[pid]
}

// --- helpers ---

func extractBashCommand(inputJSON string) string {
	var input struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(inputJSON), &input); err == nil && input.Command != "" {
		return input.Command
	}
	// fallback: try to find command in raw text
	if idx := strings.Index(inputJSON, "command"); idx >= 0 {
		rest := inputJSON[idx:]
		if q1 := strings.Index(rest, "\""); q1 >= 0 {
			rest = rest[q1+1:]
			if q2 := strings.Index(rest, "\""); q2 >= 0 {
				rest = rest[q2+1:]
				if q3 := strings.Index(rest, "\""); q3 >= 0 {
					return rest[:q3]
				}
			}
		}
	}
	return inputJSON
}

func extractFilePath(inputJSON string) string {
	var input struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
		File     string `json:"file"`
	}
	if err := json.Unmarshal([]byte(inputJSON), &input); err == nil {
		if input.Path != "" {
			return input.Path
		}
		if input.FilePath != "" {
			return input.FilePath
		}
		return input.File
	}
	return ""
}

func containsAny(s string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func truncForSignal(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func boolStr(b bool) string {
	if b {
		return "pass"
	}
	return "fail"
}

// --- preset termination profiles for common roles ---

func TerminationForRole(role string) TerminationCondition {
	switch role {
	case "hw_probe", "ref_discover":
		return OrConditions(
			&MaxRounds{Limit: 3},
			&ConsecutiveTextRounds{Threshold: 2},
			&Timeout{Duration: 5 * time.Minute},
		)
	case "dir_survey", "file_survey":
		return OrConditions(
			&MaxRounds{Limit: 2},
			&ConsecutiveTextRounds{Threshold: 1},
		)
	case "theory_planner", "dev_planner":
		return OrConditions(
			&MaxRounds{Limit: 5},
			&ConsecutiveTextRounds{Threshold: 2},
			&Timeout{Duration: 10 * time.Minute},
		)
	case "review_conv":
		return OrConditions(
			&MaxRounds{Limit: 10},
			&ConsecutiveTextRounds{Threshold: 2}, // reviewer reads, then writes verdict = text round
			&Timeout{Duration: 10 * time.Minute},
		)
	default:
		// dev_unit and other roles: stop on git push success, or max 20, or timeout
		return OrConditions(
			&BashExitZero{Pattern: "git push"},
			&MaxRounds{Limit: 20},
			&Timeout{Duration: 20 * time.Minute},
		)
	}
}
