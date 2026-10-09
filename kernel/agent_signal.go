package kernel

import (
	. "google.golang.org/adk/v2/include"
)

func AgentSendSignal(pid int64, sig int32) int {
	ret := SysKill(int32(pid), sig)
	if ret != 0 {
		return ret
	}

	switch sig {
	case SIGSTOP:
		metaMu.Lock()
		if m, ok := taskMeta[pid]; ok && m.Cancel != nil {
			m.Cancel()
		}
		metaMu.Unlock()

	case SIGKILL:
		metaMu.Lock()
		m := taskMeta[pid]
		metaMu.Unlock()
		if m != nil {
			if m.Cmd != nil && m.Cmd.Process != nil {
				m.Cmd.Process.Kill()
			}
			if m.Cancel != nil {
				m.Cancel()
			}
		}

	case SIGALRM:
		schedMu.Lock()
		for i := 0; i < NR_TASKS; i++ {
			t := Task[i]
			if t != nil && int64(t.Pid) == pid {
				if t.TokenUsed > t.TokenBudget {
					t.State = TASK_STOPPED
					go func() {
						AgentExit(pid, &AgentExitInfo{
							Code:   EXIT_TIMEOUT,
							Reason: "token budget alarm",
							Tokens: t.TokenUsed,
						})
					}()
				}
				break
			}
		}
		schedMu.Unlock()

	case SIGPIPE:
		go func() {
			AgentExit(pid, &AgentExitInfo{
				Code:   EXIT_UPSTREAM_DEAD,
				Reason: "downstream agent pipe closed",
			})
		}()
	}

	return 0
}

func AgentCheckSignals(pid int64) int32 {
	schedMu.Lock()
	defer schedMu.Unlock()
	for i := 0; i < NR_TASKS; i++ {
		t := Task[i]
		if t != nil && int64(t.Pid) == pid {
			pending := t.Signal & ^t.Blocked
			return pending
		}
	}
	return 0
}

func SetupBudgetAlarm(pid int64) {
	schedMu.Lock()
	defer schedMu.Unlock()
	for i := 0; i < NR_TASKS; i++ {
		t := Task[i]
		if t != nil && int64(t.Pid) == pid {
			if t.TokenBudget > 0 {
				ticks := int32(t.TokenBudget / 1000)
				if ticks < 100 {
					ticks = 100
				}
				t.Alarm = Jiffies + ticks
			}
			break
		}
	}
}
