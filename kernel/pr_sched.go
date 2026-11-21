package kernel

import (
	"fmt"

	. "google.golang.org/adk/v2/include"
)

func PRSchedule() int64 {
	schedMu.Lock()
	defer schedMu.Unlock()
	prMu.Lock()
	defer prMu.Unlock()

	var best int
	var bestC int32 = -1

	for {
		best = 0
		bestC = -1
		for i := NR_TASKS - 1; i > 0; i-- {
			t := Task[i]
			if t == nil || prTable[int64(t.Pid)] == nil {
				continue
			}
			if t.State != PR_DRAFT {
				continue
			}
			if t.Counter > bestC {
				bestC = t.Counter
				best = i
			}
		}

		if bestC > 0 {
			break
		}

		if best == 0 {
			return 0
		}

		for i := NR_TASKS - 1; i > 0; i-- {
			t := Task[i]
			if t == nil || prTable[int64(t.Pid)] == nil {
				continue
			}
			t.Counter = (t.Counter >> 1) + t.Priority
		}
	}

	if best > 0 {
		return int64(Task[best].Pid)
	}
	return 0
}

func PRTick(pid int64, tokensUsed int64) {
	schedMu.Lock()
	defer schedMu.Unlock()

	for i := 1; i < NR_TASKS; i++ {
		t := Task[i]
		if t != nil && int64(t.Pid) == pid {
			t.TokenUsed += tokensUsed
			t.Counter--
			if t.Counter <= 0 {
				t.Counter = 0
			}
			return
		}
	}
}

func PRSetPriority(pid int64, priority int32) {
	schedMu.Lock()
	defer schedMu.Unlock()
	for i := 1; i < NR_TASKS; i++ {
		t := Task[i]
		if t != nil && int64(t.Pid) == pid {
			if priority < 1 {
				priority = 1
			}
			if priority > 40 {
				priority = 40
			}
			t.Priority = priority
			return
		}
	}
}

func PRCounterInfo(pid int64) (counter, priority int32, convergenceLimit int32) {
	schedMu.Lock()
	defer schedMu.Unlock()
	for i := 1; i < NR_TASKS; i++ {
		t := Task[i]
		if t != nil && int64(t.Pid) == pid {
			return t.Counter, t.Priority, 2 * t.Priority
		}
	}
	return 0, 0, 0
}

func PRScheduleInfo() string {
	schedMu.Lock()
	defer schedMu.Unlock()
	prMu.Lock()
	defer prMu.Unlock()

	out := fmt.Sprintf("  %-6s %-8s %-8s %-8s %-8s %-8s %s\n",
		"PID", "COUNTER", "PRIORITY", "2*P", "TOKENS", "BUDGET", "STATE")

	for i := 1; i < NR_TASKS; i++ {
		t := Task[i]
		if t == nil || prTable[int64(t.Pid)] == nil {
			continue
		}

		state := "?"
		switch t.State {
		case PR_DRAFT:
			state = "DRAFT"
		case PR_WORKING:
			state = "WORKING"
		case PR_BLOCKED:
			state = "BLOCKED"
		case PR_CLOSED:
			state = "CLOSED"
		case PR_MERGED:
			state = "MERGED"
		}

		out += fmt.Sprintf("  %-6d %-8d %-8d %-8d %-8d %-8d %s\n",
			t.Pid, t.Counter, t.Priority, 2*t.Priority,
			t.TokenUsed, t.TokenBudget, state)
	}
	return out
}
