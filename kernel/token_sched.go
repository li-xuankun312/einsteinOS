package kernel

import (
	"fmt"

	. "google.golang.org/adk/v2/include"
)

const (
	EFFORT_LOW    = 0
	EFFORT_MEDIUM = 1
	EFFORT_HIGH   = 2

	DEFAULT_TOKEN_BUDGET  = 100000
	DEFAULT_CONTEXT_LIMIT = 200000

	COMPACTION_THRESHOLD = 80
)

func TokenSchedule() int {
	schedMu.Lock()
	defer schedMu.Unlock()

	var best int
	var bestScore int64 = -1

	for i := 1; i < NR_TASKS; i++ {
		t := Task[i]
		if t == nil || t.State != TASK_RUNNING {
			continue
		}
		remaining := t.TokenBudget - t.TokenUsed
		if remaining <= 0 {
			t.State = TASK_STOPPED
			t.Signal |= 1 << (SIGSTOP - 1)
			continue
		}
		score := remaining * int64(t.Priority+1)
		if score > bestScore {
			bestScore = score
			best = i
		}
	}

	if best > 0 {
		switchTo(best)
	}
	return best
}

func AccountTokens(pid int64, tokens int64) {
	schedMu.Lock()
	defer schedMu.Unlock()
	for i := 0; i < NR_TASKS; i++ {
		t := Task[i]
		if t != nil && int64(t.Pid) == pid {
			t.TokenUsed += tokens
			t.Counter = int32((t.TokenBudget - t.TokenUsed) >> 10)
			if t.Counter < 0 {
				t.Counter = 0
			}
			break
		}
	}
}

func AccountContext(pid int64, contextTokens int64) int {
	schedMu.Lock()
	defer schedMu.Unlock()
	for i := 0; i < NR_TASKS; i++ {
		t := Task[i]
		if t != nil && int64(t.Pid) == pid {
			t.ContextSize = contextTokens
			if t.ContextLimit > 0 {
				pct := (t.ContextSize * 100) / t.ContextLimit
				if pct >= COMPACTION_THRESHOLD {
					return 1
				}
			}
			return 0
		}
	}
	return -1
}

func SetTokenBudget(pid int64, budget int64) {
	schedMu.Lock()
	defer schedMu.Unlock()
	for i := 0; i < NR_TASKS; i++ {
		t := Task[i]
		if t != nil && int64(t.Pid) == pid {
			t.TokenBudget = budget
			t.Counter = int32(budget >> 10)
			break
		}
	}
}

func SetContextLimit(pid int64, limit int64) {
	schedMu.Lock()
	defer schedMu.Unlock()
	for i := 0; i < NR_TASKS; i++ {
		t := Task[i]
		if t != nil && int64(t.Pid) == pid {
			t.ContextLimit = limit
			break
		}
	}
}

func SetEffort(pid int64, effort uint8) {
	schedMu.Lock()
	defer schedMu.Unlock()
	for i := 0; i < NR_TASKS; i++ {
		t := Task[i]
		if t != nil && int64(t.Pid) == pid {
			t.Effort = effort
			switch effort {
			case EFFORT_LOW:
				t.Priority = 5
			case EFFORT_MEDIUM:
				t.Priority = 15
			case EFFORT_HIGH:
				t.Priority = 30
			}
			break
		}
	}
}

func TokenPs() string {
	schedMu.Lock()
	defer schedMu.Unlock()

	out := fmt.Sprintf("  %-6s %-8s %-8s %-8s %-6s %-8s\n", "PID", "BUDGET", "USED", "CTX", "EFF", "STATE")
	for i := 0; i < NR_TASKS; i++ {
		t := Task[i]
		if t == nil {
			continue
		}
		eff := "med"
		switch t.Effort {
		case EFFORT_LOW:
			eff = "low"
		case EFFORT_HIGH:
			eff = "high"
		}
		st := "?"
		switch t.State {
		case TASK_RUNNING:
			st = "R"
		case TASK_ZOMBIE:
			st = "Z"
		case TASK_STOPPED:
			st = "T"
		case TASK_INTERRUPTIBLE:
			st = "S"
		}
		out += fmt.Sprintf("  %-6d %-8d %-8d %-8d %-6s %-8s\n",
			t.Pid, t.TokenBudget, t.TokenUsed, t.ContextSize, eff, st)
	}
	return out
}
