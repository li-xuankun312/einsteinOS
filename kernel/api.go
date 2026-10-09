package kernel

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	. "google.golang.org/adk/v2/include"
)

type TaskMeta struct {
	Prompt string
	Pwd    string
	ConvID string
	Result string
	StdErr string
	Ctx    context.Context
	Cancel context.CancelFunc
	Cmd    *exec.Cmd
	stdout strings.Builder
	stderr strings.Builder
	Done   chan struct{}
}

var (
	taskMeta = make(map[int64]*TaskMeta)
	metaMu   sync.Mutex
)

func getMeta(pid int64) *TaskMeta {
	metaMu.Lock()
	defer metaMu.Unlock()
	m, ok := taskMeta[pid]
	if !ok {
		m = &TaskMeta{}
		taskMeta[pid] = m
	}
	return m
}

func Init() {
	SchedInit()
	if Task[0] == nil {
		Task[0] = &TaskStruct{}
	}
	Current = Task[0]
	Current.State = TASK_RUNNING
	Current.Pid = 0
	Current.Father = -1
	Current.Pgrp = 0
	Current.Session = 0
	Current.Leader = 1
	Current.Tty = -1
	Current.Umask = 0022
	Current.Counter = 15
	Current.Priority = 15
	taskMeta[0] = &TaskMeta{Pwd: "."}
}

func Fork(prompt string, pwd string) (int64, error) {
	nr := FindEmptyProcess()
	if nr < 0 {
		return 0, fmt.Errorf("fork: no free task slots")
	}

	pid := CopyProcess(nr, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if pid < 0 {
		return 0, fmt.Errorf("fork: copy_process failed (%d)", pid)
	}

	SetTokenBudget(int64(pid), DEFAULT_TOKEN_BUDGET)
	SetContextLimit(int64(pid), DEFAULT_CONTEXT_LIMIT)
	SetEffort(int64(pid), EFFORT_MEDIUM)

	parentCtx := GetCOWContext(int64(Current.Pid))
	if parentCtx != nil {
		parentCtx.ForkTo(int64(pid))
	} else {
		cowCtx := NewCOWContext(int64(pid))
		cowCtx.Append(prompt)
	}

	ctx, cancel := context.WithCancel(context.Background())
	metaMu.Lock()
	taskMeta[int64(pid)] = &TaskMeta{
		Prompt: prompt,
		Pwd:    pwd,
		Ctx:    ctx,
		Cancel: cancel,
		Done:   make(chan struct{}),
	}
	metaMu.Unlock()

	return int64(pid), nil
}

func ForkExec(command string, workDir string) (int64, error) {
	nr := FindEmptyProcess()
	if nr < 0 {
		return 0, fmt.Errorf("fork: %d tasks full", NR_TASKS)
	}

	pid := CopyProcess(nr, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if pid < 0 {
		return 0, fmt.Errorf("fork: copy_process failed (%d)", pid)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	if workDir != "" {
		cmd.Dir = workDir
	}

	meta := &TaskMeta{
		Prompt: command,
		Pwd:    workDir,
		Ctx:    ctx,
		Cancel: cancel,
		Cmd:    cmd,
		Done:   make(chan struct{}),
	}
	cmd.Stdout = &meta.stdout
	cmd.Stderr = &meta.stderr

	metaMu.Lock()
	taskMeta[int64(pid)] = meta
	metaMu.Unlock()

	err := cmd.Start()
	if err != nil {
		schedMu.Lock()
		Task[nr] = nil
		schedMu.Unlock()
		cancel()
		return 0, fmt.Errorf("exec: %v", err)
	}

	go func() {
		werr := cmd.Wait()
		exitCode := int32(0)
		if werr != nil {
			if exitErr, ok := werr.(*exec.ExitError); ok {
				exitCode = int32(exitErr.ExitCode())
			} else {
				exitCode = 1
			}
		}

		metaMu.Lock()
		meta.Result = meta.stdout.String()
		meta.StdErr = meta.stderr.String()
		metaMu.Unlock()

		schedMu.Lock()
		for i := 1; i < NR_TASKS; i++ {
			if Task[i] != nil && int64(Task[i].Pid) == int64(pid) {
				Task[i].State = TASK_ZOMBIE
				Task[i].ExitCode = exitCode
				TellFather(Task[i].Father)
				break
			}
		}
		schedMu.Unlock()

		close(meta.Done)
	}()

	return int64(pid), nil
}

func Exit(pid int64, code int) {
	if cowCtx := GetCOWContext(pid); cowCtx != nil {
		cowCtx.Release()
	}

	schedMu.Lock()
	saved := Current
	for i := 1; i < NR_TASKS; i++ {
		if Task[i] != nil && int64(Task[i].Pid) == pid {
			Current = Task[i]
			schedMu.Unlock()

			metaMu.Lock()
			if m, ok := taskMeta[pid]; ok && m.Done != nil {
				select {
				case <-m.Done:
				default:
					close(m.Done)
				}
			}
			metaMu.Unlock()

			DoExit(int32(code) << 8)

			schedMu.Lock()
			Current = saved
			schedMu.Unlock()
			return
		}
	}
	schedMu.Unlock()
}

func Wait(pid int64) (int64, int, error) {
	if pid >= 0 {
		metaMu.Lock()
		meta := taskMeta[pid]
		metaMu.Unlock()
		if meta != nil && meta.Done != nil {
			<-meta.Done
		}
	}

	schedMu.Lock()
	wpid := int32(-1)
	if pid >= 0 {
		wpid = int32(pid)
	}
	var stat int32
	ret := SysWaitpid(wpid, &stat, 0)
	schedMu.Unlock()

	if ret < 0 {
		return 0, 0, fmt.Errorf("wait: %s", errName(int(ret)))
	}
	return int64(ret), int(stat >> 8), nil
}

func Kill(pid int64, sig int64) error {
	ret := SysKill(int32(pid), int32(sig))
	if ret != 0 {
		return fmt.Errorf("kill: no such process %d", pid)
	}

	if sig == int64(SIGKILL) {
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
	}
	return nil
}

func Ps() {
	schedMu.Lock()
	defer schedMu.Unlock()

	fmt.Printf("  %-6s %-6s %-10s %-8s %s\n", "PID", "PPID", "STATE", "CONVID", "CMD")
	fmt.Printf("  %-6s %-6s %-10s %-8s %s\n", "---", "----", "-----", "------", "---")
	for i := 0; i < NR_TASKS; i++ {
		t := Task[i]
		if t == nil {
			continue
		}
		stateName := "?"
		switch t.State {
		case TASK_RUNNING:
			stateName = "R"
		case TASK_INTERRUPTIBLE:
			stateName = "S"
		case TASK_UNINTERRUPTIBLE:
			stateName = "D"
		case TASK_ZOMBIE:
			stateName = "Z"
		case TASK_STOPPED:
			stateName = "T"
		}
		pid := int64(t.Pid)
		metaMu.Lock()
		m := taskMeta[pid]
		prompt := ""
		convID := ""
		if m != nil {
			prompt = m.Prompt
			convID = m.ConvID
			if len(prompt) > 60 {
				prompt = prompt[:60] + "..."
			}
			if len(convID) > 8 {
				convID = convID[:8]
			}
		}
		metaMu.Unlock()
		fmt.Printf("  %-6d %-6d %-10s %-8s %s\n", t.Pid, t.Father, stateName, convID, prompt)
	}
}

func GetTask(pid int64) *TaskStruct {
	schedMu.Lock()
	defer schedMu.Unlock()
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil && int64(Task[i].Pid) == pid {
			return Task[i]
		}
	}
	return nil
}

func GetCurrent() *TaskStruct {
	schedMu.Lock()
	defer schedMu.Unlock()
	return Current
}

func SetPwd(t *TaskStruct, pwd string) {
	m := getMeta(int64(t.Pid))
	m.Pwd = pwd
}

func GetPwd(t *TaskStruct) string {
	m := getMeta(int64(t.Pid))
	return m.Pwd
}

func TaskCtx(t *TaskStruct) context.Context {
	pid := int64(t.Pid)
	metaMu.Lock()
	defer metaMu.Unlock()
	if m, ok := taskMeta[pid]; ok && m.Ctx != nil {
		return m.Ctx
	}
	return context.Background()
}

func SetConvID(pid int64, convID string) {
	m := getMeta(pid)
	m.ConvID = convID
}

func SetResult(pid int64, result string) {
	m := getMeta(pid)
	m.Result = result
}

func GetResult(pid int64) string {
	metaMu.Lock()
	defer metaMu.Unlock()
	if m, ok := taskMeta[pid]; ok {
		return m.Result
	}
	return ""
}

func GetStderr(pid int64) string {
	metaMu.Lock()
	defer metaMu.Unlock()
	if m, ok := taskMeta[pid]; ok {
		return m.StdErr
	}
	return ""
}

func SetStartupTime(t int64) {
	StartupTime = int32(t)
}

const SIG_KILL int64 = 9

func errName(code int) string {
	switch code {
	case -ECHILD:
		return "ECHILD"
	case -EINTR:
		return "EINTR"
	default:
		return fmt.Sprintf("errno %d", -code)
	}
}
