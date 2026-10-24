package kernel

import (
	"encoding/json"
	"log"
	"strings"
	"sync"
)

// ToolSignalCollector observes tool_use/tool_result events and extracts
// structured signals. Replaces parse_kv_blocks + parse_last_status.
//
// Instead of asking the agent to write "compile=pass" in free text,
// the collector watches what the agent DOES:
//   - bash("make") exit code → compile status
//   - bash("git push") exit code → push status
//   - create_file/str_replace_editor → files written
//   - bash("git diff --stat") → diff summary

type ToolSignalCollector struct {
	mu           sync.Mutex
	filesWritten []string
	compileRuns  []CompileRun
	gitPushes    []GitPush
	bashCommands []BashRecord
}

type CompileRun struct {
	Command string
	Output  string
	Passed  bool
}

type GitPush struct {
	Branch string
	Output string
	Passed bool
}

type BashRecord struct {
	Command string
	Output  string
	Round   int
}

func NewToolSignalCollector() *ToolSignalCollector {
	return &ToolSignalCollector{}
}

// Observe processes one tool_use + tool_result pair.
// Called from completionLoop after shadow execution.
func (c *ToolSignalCollector) Observe(toolName, inputJSON, output string, round int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch toolName {
	case "bash", "terminal":
		cmd := extractBashCommand(inputJSON)
		c.bashCommands = append(c.bashCommands, BashRecord{
			Command: cmd, Output: output, Round: round,
		})
		c.classifyBash(cmd, output)

	case "create", "create_file", "write_to_file":
		path := extractFilePath(inputJSON)
		if path != "" {
			c.filesWritten = append(c.filesWritten, path)
		}

	case "str_replace_editor", "str_replace":
		path := extractFilePath(inputJSON)
		if path != "" {
			// deduplicate: str_replace on same file is edit, not new file
			found := false
			for _, f := range c.filesWritten {
				if f == path {
					found = true
					break
				}
			}
			if !found {
				c.filesWritten = append(c.filesWritten, path)
			}
		}
	}
}

func (c *ToolSignalCollector) classifyBash(cmd, output string) {
	lower := strings.ToLower(cmd)
	outLower := strings.ToLower(output)

	// compile detection
	buildCmds := []string{"make", "go build", "nvcc", "gcc", "g++", "cmake --build", "cargo build"}
	for _, bc := range buildCmds {
		if strings.Contains(lower, bc) {
			passed := !containsAny(outLower, []string{
				"error:", "fatal:", "undefined reference", "cannot find",
				"failed", "build failed", "compilation error",
				"no rule to make", "exit status",
			})
			c.compileRuns = append(c.compileRuns, CompileRun{
				Command: cmd, Output: output, Passed: passed,
			})
			return
		}
	}

	// git push detection
	if strings.Contains(lower, "git push") {
		passed := !containsAny(outLower, []string{
			"error:", "fatal:", "rejected", "failed", "permission denied",
		})
		branch := ""
		// extract branch from "git push origin branch-name"
		parts := strings.Fields(cmd)
		for i, p := range parts {
			if p == "push" && i+2 < len(parts) {
				branch = parts[i+2]
			}
		}
		c.gitPushes = append(c.gitPushes, GitPush{
			Branch: branch, Output: output, Passed: passed,
		})
	}
}

// --- query methods: kernel reads these instead of parsing text ---

// CompileStatus returns the result of the last compile command.
// Replaces: parse_last_status(text)["compile"]
func (c *ToolSignalCollector) CompileStatus() (passed bool, found bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.compileRuns) == 0 {
		return false, false
	}
	last := c.compileRuns[len(c.compileRuns)-1]
	return last.Passed, true
}

// LastCompileOutput returns the raw output of the last compile command.
// Replaces: status["compile_error"]
func (c *ToolSignalCollector) LastCompileOutput() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.compileRuns) == 0 {
		return ""
	}
	return c.compileRuns[len(c.compileRuns)-1].Output
}

// GitPushStatus returns whether git push succeeded.
func (c *ToolSignalCollector) GitPushStatus() (passed bool, found bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.gitPushes) == 0 {
		return false, false
	}
	last := c.gitPushes[len(c.gitPushes)-1]
	return last.Passed, true
}

// FilesWritten returns all files the agent created or edited.
// Replaces: status["files_written"]
func (c *ToolSignalCollector) FilesWritten() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.filesWritten))
	copy(out, c.filesWritten)
	return out
}

// ToStatusMap returns a map[string]string compatible with the old
// parse_last_status format, for gradual migration. New code should
// use the typed methods above.
func (c *ToolSignalCollector) ToStatusMap() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()

	status := map[string]string{}

	if len(c.compileRuns) > 0 {
		last := c.compileRuns[len(c.compileRuns)-1]
		if last.Passed {
			status["compile"] = "pass"
		} else {
			status["compile"] = "fail"
			status["compile_error"] = truncForSignal(last.Output, 500)
		}
	}

	if len(c.gitPushes) > 0 {
		last := c.gitPushes[len(c.gitPushes)-1]
		if last.Passed {
			status["push"] = "pass"
			status["push_branch"] = last.Branch
		} else {
			status["push"] = "fail"
		}
	}

	if len(c.filesWritten) > 0 {
		status["files_written"] = strings.Join(c.filesWritten, ",")
	}

	return status
}

// --- per-pid collector registry ---

var (
	pidCollectors   = make(map[int64]*ToolSignalCollector)
	pidCollectorsMu sync.Mutex
)

func SetCollector(pid int64, c *ToolSignalCollector) {
	pidCollectorsMu.Lock()
	pidCollectors[pid] = c
	pidCollectorsMu.Unlock()
}

func GetCollector(pid int64) *ToolSignalCollector {
	pidCollectorsMu.Lock()
	defer pidCollectorsMu.Unlock()
	return pidCollectors[pid]
}

// ObserveToolCall is called from model/claudeweb to feed tool events
// into the current process's collector.
func ObserveToolCall(toolName, inputJSON, output string, round int) {
	pid := int64(SysGetpid())
	pidCollectorsMu.Lock()
	c := pidCollectors[pid]
	pidCollectorsMu.Unlock()
	if c != nil {
		c.Observe(toolName, inputJSON, output, round)
	}

	// also check termination for the latest forked child
	forkConvMu.Lock()
	childPid := lastForkedPid
	forkConvMu.Unlock()
	if childPid > 0 && childPid != pid {
		pidCollectorsMu.Lock()
		cc := pidCollectors[childPid]
		pidCollectorsMu.Unlock()
		if cc != nil {
			cc.Observe(toolName, inputJSON, output, round)
		}
	}
}

// CollectChild extracts the structured status from a child's collector.
// Replaces: dev_collect_child's parse_last_status pipeline.
func CollectChild(childPid int64) map[string]string {
	pidCollectorsMu.Lock()
	c := pidCollectors[childPid]
	pidCollectorsMu.Unlock()
	if c == nil {
		log.Printf("tool_signal: no collector for pid %d", childPid)
		return nil
	}
	status := c.ToStatusMap()
	if len(status) > 0 {
		log.Printf("tool_signal: pid %d status from tool signals: %v", childPid, status)
	}
	return status
}

// MarshalSignals serializes a collector's data for debugging/logging.
func (c *ToolSignalCollector) MarshalSignals() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	data := map[string]interface{}{
		"files_written": c.filesWritten,
		"compile_runs":  len(c.compileRuns),
		"git_pushes":    len(c.gitPushes),
		"bash_commands":  len(c.bashCommands),
	}
	b, _ := json.Marshal(data)
	return string(b)
}
