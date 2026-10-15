package kernel

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	. "google.golang.org/adk/v2/include"
)

type TaskMeta struct {
	Prompt       string
	Pwd          string
	ConvID       string
	Result       string
	StdErr       string
	Ctx          context.Context
	Cancel       context.CancelFunc
	Cmd          *exec.Cmd
	stdout       strings.Builder
	stderr       strings.Builder
	Done         chan struct{}
	LastActivity int64
}

func Touch(pid int64) {
	metaMu.Lock()
	if m, ok := taskMeta[pid]; ok {
		m.LastActivity = time.Now().Unix()
	}
	metaMu.Unlock()
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

var convExecFn func(prompt string) (string, string)

func SetConvExec(fn func(string) (string, string)) {
	convExecFn = fn
}

func GetConvExec() func(string) (string, string) {
	return convExecFn
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
	meta := &TaskMeta{
		Prompt: prompt,
		Pwd:    pwd,
		Ctx:    ctx,
		Cancel: cancel,
		Done:   make(chan struct{}),
	}
	metaMu.Lock()
	taskMeta[int64(pid)] = meta
	metaMu.Unlock()

	if convExecFn != nil {
		go func() {
			stdout, stderr := convExecFn(prompt)

			metaMu.Lock()
			meta.Result = stdout
			meta.StdErr = stderr
			metaMu.Unlock()

			schedMu.Lock()
			for i := 1; i < NR_TASKS; i++ {
				if Task[i] != nil && int64(Task[i].Pid) == int64(pid) {
					Task[i].State = TASK_ZOMBIE
					Task[i].ExitCode = 0
					TellFather(Task[i].Father)
					break
				}
			}
			schedMu.Unlock()

			close(meta.Done)
		}()
	}

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

const SIG_INT int64 = 2

func Wait(pid int64) (int64, int, error) {
	return WaitTimeout(pid, 5*time.Minute)
}

func WaitTimeout(pid int64, idleTimeout time.Duration) (int64, int, error) {
	if pid >= 0 {
		metaMu.Lock()
		meta := taskMeta[pid]
		metaMu.Unlock()
		if meta != nil && meta.Done != nil {
			Touch(pid)
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-meta.Done:
					goto waited
				case <-ticker.C:
					metaMu.Lock()
					last := meta.LastActivity
					metaMu.Unlock()
					idle := time.Since(time.Unix(last, 0))
					if idle > idleTimeout {
						log.Printf("wait: pid %d idle %v (no activity for %v), sending SIGINT", pid, idle, idleTimeout)
						sendSigint(pid)
						select {
						case <-meta.Done:
							log.Printf("wait: pid %d exited after SIGINT", pid)
						case <-time.After(10 * time.Second):
							log.Printf("wait: pid %d still alive 10s after SIGINT, giving up", pid)
							if meta.Cancel != nil {
								meta.Cancel()
							}
						}
						goto waited
					}
				}
			}
		}
	}
waited:

	schedMu.Lock()
	wpid := int32(-1)
	if pid >= 0 {
		wpid = int32(pid)
	}
	var stat int32
	ret := SysWaitpid(wpid, &stat, 0)
	schedMu.Unlock()

	if ret < 0 {
		if pid >= 0 && int(ret) == -ECHILD {
			metaMu.Lock()
			meta := taskMeta[pid]
			metaMu.Unlock()
			if meta != nil {
				exitCode := 0
				return pid, exitCode, nil
			}
		}
		return 0, 0, fmt.Errorf("wait: %s", errName(int(ret)))
	}
	return int64(ret), int(stat >> 8), nil
}

func sendSigint(pid int64) {
	metaMu.Lock()
	m := taskMeta[pid]
	metaMu.Unlock()
	if m != nil && m.Cmd != nil && m.Cmd.Process != nil {
		m.Cmd.Process.Signal(os.Interrupt)
	}
}

func Kill(pid int64, sig int64) error {
	ret := SysKill(int32(pid), int32(sig))
	if ret != 0 {
		return fmt.Errorf("kill: no such process %d", pid)
	}

	metaMu.Lock()
	m := taskMeta[pid]
	metaMu.Unlock()
	if m != nil {
		if m.Cmd != nil && m.Cmd.Process != nil {
			switch sig {
			case SIG_INT:
				m.Cmd.Process.Signal(os.Interrupt)
			case int64(SIGKILL):
				m.Cmd.Process.Kill()
			}
		}
		if sig == int64(SIGKILL) && m.Cancel != nil {
			m.Cancel()
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

func TouchByConv(convID string) {
	metaMu.Lock()
	defer metaMu.Unlock()
	now := time.Now().Unix()
	for _, m := range taskMeta {
		if m.ConvID == convID {
			m.LastActivity = now
			return
		}
	}
}

type ConvRecord struct {
	ConvID    string
	ParentPid int64
	Repo      string
	Role      string // "theorize_chunk_1", "dev_draft", "dev_fix", "dev_verify", "hw_probe", "ref_discover"
	Phase     int    // for multi-phase: 1,2,3
	Status    string // "running", "done", "error", "timeout"
	Created   time.Time
}

var (
	convRegistry   []ConvRecord
	convRegistryMu sync.Mutex
	stopConvFn     func(convID string)
)

func SetStopConvFn(fn func(string)) {
	stopConvFn = fn
}

var downloadConvFn func(convID string) (string, error)

func SetDownloadConvFn(fn func(string) (string, error)) {
	downloadConvFn = fn
}

func DownloadConv(convID string) (string, error) {
	if downloadConvFn == nil {
		return "", fmt.Errorf("no download function set")
	}
	return downloadConvFn(convID)
}

func RegisterConv(parentPid int64, convID, repo, role string, phase int) {
	convRegistryMu.Lock()
	convRegistry = append(convRegistry, ConvRecord{
		ConvID:    convID,
		ParentPid: parentPid,
		Repo:      repo,
		Role:      role,
		Phase:     phase,
		Status:    "running",
		Created:   time.Now(),
	})
	convRegistryMu.Unlock()
	log.Printf("conv_registry: registered %s repo=%s role=%s phase=%d",
		convID[:8], repo, role, phase)
}

func ConvDone(convID string, status string) {
	convRegistryMu.Lock()
	for i := range convRegistry {
		if convRegistry[i].ConvID == convID {
			convRegistry[i].Status = status
			break
		}
	}
	convRegistryMu.Unlock()
}

func FindConv(repo, role string, phase int) *ConvRecord {
	convRegistryMu.Lock()
	defer convRegistryMu.Unlock()
	for i := len(convRegistry) - 1; i >= 0; i-- {
		r := &convRegistry[i]
		if r.Repo == repo && r.Role == role && (phase == 0 || r.Phase == phase) {
			return r
		}
	}
	return nil
}

func FindConvsByRepo(repo string) []ConvRecord {
	convRegistryMu.Lock()
	defer convRegistryMu.Unlock()
	var out []ConvRecord
	for _, r := range convRegistry {
		if r.Repo == repo {
			out = append(out, r)
		}
	}
	return out
}

func ConvRegistryStatus() string {
	convRegistryMu.Lock()
	defer convRegistryMu.Unlock()
	if len(convRegistry) == 0 {
		return "no conversations registered"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("  %-8s %-20s %-20s %-3s %-8s %s\n",
		"CONVID", "REPO", "ROLE", "PH", "STATUS", "AGE"))
	for _, r := range convRegistry {
		age := time.Since(r.Created).Truncate(time.Second)
		sb.WriteString(fmt.Sprintf("  %-8s %-20s %-20s %-3d %-8s %v\n",
			r.ConvID[:8], truncDiff(r.Repo, 20), r.Role, r.Phase, r.Status, age))
	}
	return sb.String()
}

// TrackChildConv — backwards compat, calls RegisterConv with generic role
func TrackChildConv(parentPid int64, convID string) {
	RegisterConv(parentPid, convID, "", "child", 0)
}

// StopOrphanConvs — now just logs, does NOT kill conversations
func StopOrphanConvs(parentPid int64) {
	running := 0
	convRegistryMu.Lock()
	for _, r := range convRegistry {
		if r.ParentPid == parentPid && r.Status == "running" {
			running++
		}
	}
	convRegistryMu.Unlock()
	if running > 0 {
		log.Printf("conv_registry: pid %d has %d conversations still alive (preserved for reuse)", parentPid, running)
	}
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
