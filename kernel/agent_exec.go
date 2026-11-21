package kernel

import (
	. "google.golang.org/adk/v2/include"
)

type AgentImage struct {
	SystemPrompt string
	Skills       []string
	Model        string
	Effort       uint8
	TokenBudget  int64
}

func AgentExec(pid int64, img *AgentImage) int {
	schedMu.Lock()
	var t *TaskStruct
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil && int64(Task[i].Pid) == pid {
			t = Task[i]
			break
		}
	}
	schedMu.Unlock()

	if t == nil {
		return -ESRCH
	}

	metaMu.Lock()
	m, ok := taskMeta[pid]
	if !ok {
		m = &TaskMeta{}
		taskMeta[pid] = m
	}
	m.Prompt = img.SystemPrompt
	metaMu.Unlock()

	schedMu.Lock()
	if img.TokenBudget > 0 {
		t.TokenBudget = img.TokenBudget
		t.TokenUsed = 0
	}
	if img.Effort > 0 {
		t.Effort = img.Effort
		switch img.Effort {
		case EFFORT_LOW:
			t.Priority = 5
		case EFFORT_MEDIUM:
			t.Priority = 15
		case EFFORT_HIGH:
			t.Priority = 30
		}
	}
	t.Counter = t.Priority
	schedMu.Unlock()

	return 0
}

var agentSkills = make(map[int64][]string)

func SetAgentSkills(pid int64, skills []string) {
	metaMu.Lock()
	defer metaMu.Unlock()
	agentSkills[pid] = skills
}

func GetAgentSkills(pid int64) []string {
	metaMu.Lock()
	defer metaMu.Unlock()
	return agentSkills[pid]
}
