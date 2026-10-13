package kernel

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	. "google.golang.org/adk/v2/include"
)

type PRDriver struct{}

type pr_tool_input struct {
	Task   string `json:"task"`
	Pid    int64  `json:"pid"`
	Action string `json:"action"`
}

func (d *PRDriver) Match(name string) bool {
	n := strings.ToLower(name)
	return n == "pr_setup" || n == "pr_step" || n == "pr_status"
}

func (d *PRDriver) Execute(name string, inputJSON string, workDir string) string {
	var input pr_tool_input
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return fmt.Sprintf("parse error: %v", err)
	}

	switch strings.ToLower(name) {
	case "pr_setup":
		return pr_driver_setup(input.Task)
	case "pr_step":
		return pr_driver_step(input.Pid)
	case "pr_status":
		return pr_driver_status(input.Pid)
	}
	return "unknown pr tool"
}

func pr_driver_setup(task string) string {
	refs := PRSetup(task)
	if len(refs) == 0 {
		return "0 reference repos found"
	}

	pid, err := PRFork(".", "dev", task, refs)
	if err != nil {
		return fmt.Sprintf("pr_fork: %v", err)
	}

	total, _ := PRStepCount(pid)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("PR pid=%d, %d refs, %d steps\n", pid, len(refs), total))
	for _, r := range refs {
		sb.WriteString(fmt.Sprintf("  %s/%s\n", r.Owner, r.Name))
	}
	sb.WriteString(PRStepLog(pid))

	log.Printf("pr_driver: setup pid=%d refs=%d steps=%d", pid, len(refs), total)
	return sb.String()
}

func pr_driver_step(pid int64) string {
	step := PRCurrentStep(pid)
	if step == nil {
		return fmt.Sprintf("pid %d: no more steps", pid)
	}

	result, err := PRRunNext(pid)
	total, done := PRStepCount(pid)
	if err != nil {
		return fmt.Sprintf("step %d/%d %s error: %v",
			done, total, step_name(step.Kind), err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("step %d/%d %s done\n", done, total, step_name(step.Kind)))

	if step.ChildPid > 0 {
		stderr := GetStderr(step.ChildPid)
		if stderr != "" {
			sb.WriteString("[tool outputs]\n")
			if len(stderr) > 4000 {
				stderr = stderr[:4000]
			}
			sb.WriteString(stderr)
			sb.WriteString("\n")
		}
	}

	sb.WriteString("[result]\n")
	sb.WriteString(result)

	return sb.String()
}

func pr_driver_status(pid int64) string {
	if pid == 0 {
		return PRPs()
	}
	return PRStepLog(pid) + PRScheduleInfo()
}

func PRDriverSetup(task string) string {
	return pr_driver_setup(task)
}

func PRDriverStep(pid int64) string {
	if pid == 0 {
		pid = pr_find_active()
		if pid == 0 {
			return ""
		}
	}
	return pr_driver_step(pid)
}

func pr_find_active() int64 {
	schedMu.Lock()
	defer schedMu.Unlock()
	prMu.Lock()
	defer prMu.Unlock()
	for i := 1; i < NR_TASKS; i++ {
		t := Task[i]
		if t == nil {
			continue
		}
		if _, ok := prTable[int64(t.Pid)]; ok {
			if t.State == PR_DRAFT || t.State == PR_WORKING {
				return int64(t.Pid)
			}
		}
	}
	return 0
}
