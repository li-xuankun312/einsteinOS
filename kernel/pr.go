package kernel

import (
	"fmt"
	"sync"
	"time"

	. "google.golang.org/adk/v2/include"
)

const (
	PR_DRAFT    = TASK_RUNNING
	PR_WORKING  = TASK_INTERRUPTIBLE
	PR_BLOCKED  = TASK_UNINTERRUPTIBLE
	PR_CLOSED   = TASK_ZOMBIE
	PR_MERGED   = TASK_STOPPED
)

type RefRepo struct {
	Owner    string
	Name     string
	CloneDir string
}

type PRProcess struct {
	Repo       string
	Branch     string
	PRNumber   int
	Title      string
	RefRepos   []RefRepo
	Commits    []PRCommit
	CreateTime time.Time
	MergeTime  time.Time
}

type PRCommit struct {
	SHA     string
	Message string
	Files   []string
	Diff    string
	Time    time.Time
}



var (
	prTable = make(map[int64]*PRProcess)
	prMu    sync.Mutex
)

func PRFork(repo, branch, title string, refRepos []RefRepo) (int64, error) {
	nr := FindEmptyProcess()
	if nr < 0 {
		return 0, fmt.Errorf("pr_fork: task table full (%d slots)", NR_TASKS)
	}

	pid := CopyProcess(nr, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if pid < 0 {
		return 0, fmt.Errorf("pr_fork: copy_process failed (%d)", pid)
	}

	SetTokenBudget(int64(pid), DEFAULT_TOKEN_BUDGET)
	SetContextLimit(int64(pid), DEFAULT_CONTEXT_LIMIT)
	SetEffort(int64(pid), EFFORT_HIGH)

	pr := &PRProcess{
		Repo:       repo,
		Branch:     branch,
		Title:      title,
		RefRepos:   refRepos,
		CreateTime: time.Now(),
	}

	prMu.Lock()
	prTable[int64(pid)] = pr
	prMu.Unlock()

	metaMu.Lock()
	taskMeta[int64(pid)] = &TaskMeta{
		Prompt: fmt.Sprintf("[PR] %s: %s", repo, title),
		Pwd:    ".",
		Done:   make(chan struct{}),
	}
	metaMu.Unlock()

	for _, ref := range refRepos {
		PRPushStep(int64(pid), &Step{
			Kind:  STEP_REF_CLONE,
			Owner: ref.Owner,
			Repo:  ref.Name,
		})
		PRPushStep(int64(pid), &Step{
			Kind:  STEP_REF_FIRST,
			Owner: ref.Owner,
			Repo:  ref.Name,
		})
		PRPushStep(int64(pid), &Step{
			Kind:  STEP_REF_COMPARE,
			Owner: ref.Owner,
			Repo:  ref.Name,
			Dir:   ".",
		})
	}

	return int64(pid), nil
}

func PRAddCommit(pid int64, sha, message string, files []string, diff string) {
	prMu.Lock()
	defer prMu.Unlock()
	pr := prTable[pid]
	if pr == nil {
		return
	}
	pr.Commits = append(pr.Commits, PRCommit{
		SHA:     sha,
		Message: message,
		Files:   files,
		Diff:    diff,
		Time:    time.Now(),
	})
}

func PRMerge(pid int64) error {
	schedMu.Lock()
	for i := 1; i < NR_TASKS; i++ {
		t := Task[i]
		if t != nil && int64(t.Pid) == pid {
			t.State = PR_MERGED
			t.ExitCode = 0
			schedMu.Unlock()

			prMu.Lock()
			if pr := prTable[pid]; pr != nil {
				pr.MergeTime = time.Now()
			}
			prMu.Unlock()

			TellFather(t.Father)
			return nil
		}
	}
	schedMu.Unlock()
	return fmt.Errorf("pr_merge: pid %d not found", pid)
}

func PRClose(pid int64, reason string) error {
	schedMu.Lock()
	for i := 1; i < NR_TASKS; i++ {
		t := Task[i]
		if t != nil && int64(t.Pid) == pid {
			t.State = PR_CLOSED
			t.ExitCode = 1
			schedMu.Unlock()
			TellFather(t.Father)
			return nil
		}
	}
	schedMu.Unlock()
	return fmt.Errorf("pr_close: pid %d not found", pid)
}

func PRBlock(pid int64) {
	schedMu.Lock()
	defer schedMu.Unlock()
	for i := 1; i < NR_TASKS; i++ {
		t := Task[i]
		if t != nil && int64(t.Pid) == pid && t.State == PR_DRAFT {
			t.State = PR_BLOCKED
			return
		}
	}
}

func PRUnblock(pid int64) {
	schedMu.Lock()
	defer schedMu.Unlock()
	for i := 1; i < NR_TASKS; i++ {
		t := Task[i]
		if t != nil && int64(t.Pid) == pid && t.State == PR_BLOCKED {
			t.State = PR_DRAFT
			return
		}
	}
}

func GetPR(pid int64) *PRProcess {
	prMu.Lock()
	defer prMu.Unlock()
	return prTable[pid]
}

func PRPs() string {
	schedMu.Lock()
	defer schedMu.Unlock()
	prMu.Lock()
	defer prMu.Unlock()

	out := fmt.Sprintf("  %-6s %-8s %-10s %-4s %-30s %s\n",
		"PID", "COUNTER", "STATE", "C#", "REPO", "TITLE")

	for i := 1; i < NR_TASKS; i++ {
		t := Task[i]
		if t == nil {
			continue
		}
		pr := prTable[int64(t.Pid)]
		if pr == nil {
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

		title := pr.Title
		if len(title) > 40 {
			title = title[:40] + "..."
		}
		repo := pr.Repo
		if len(repo) > 30 {
			repo = repo[:30]
		}

		out += fmt.Sprintf("  %-6d %-8d %-10s %-4d %-30s %s\n",
			t.Pid, t.Counter, state,
			len(pr.Commits),
			repo, title)
	}
	return out
}
