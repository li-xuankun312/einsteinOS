package kernel

import (
	. "google.golang.org/adk/v2/include"
)

func SysSigpending() int32 {
	return int32(Current.Signal) & int32(Current.Blocked)
}

func SysSigsuspend(mask int32) int32 {
	oldBlocked := Current.Blocked
	Current.Blocked = mask & ^(int32(1)<<(SIGKILL-1) | int32(1)<<(SIGSTOP-1))

	Current.State = TASK_INTERRUPTIBLE
	Schedule()

	Current.Blocked = oldBlocked
	return -EINTR
}

func SendSigToTask(task *TaskStruct, sig int) int {
	if sig < 1 || sig > 32 {
		return -EINVAL
	}
	if task == nil {
		return -ESRCH
	}
	task.Signal |= 1 << uint(sig-1)
	if task.State == TASK_INTERRUPTIBLE {
		task.State = TASK_RUNNING
	}
	return 0
}

func IsSigBlocked(task *TaskStruct, sig int) bool {
	if sig == SIGKILL || sig == SIGSTOP {
		return false
	}
	return task.Blocked&(1<<int32(sig-1)) != 0
}

func IsSigPending(task *TaskStruct) bool {
	return (task.Signal & ^task.Blocked) != 0
}

func FlushSignals(task *TaskStruct) {
	task.Signal = 0
}

func DequeueSignal(task *TaskStruct) int {
	pending := task.Signal & ^task.Blocked
	if pending == 0 {
		return 0
	}
	for i := int32(0); i < 32; i++ {
		if pending&(1<<i) != 0 {
			task.Signal &^= 1 << i
			return int(i + 1)
		}
	}
	return 0
}

func SysKillPg(pgrp, sig int) int {
	if pgrp <= 0 {
		return -EINVAL
	}
	if sig < 0 || sig > 32 {
		return -EINVAL
	}
	found := false
	pg32 := int32(pgrp)
	for i := 1; i < NR_TASKS; i++ {
		t := Task[i]
		if t == nil || t.Pgrp != pg32 {
			continue
		}
		found = true
		if sig > 0 {
			t.Signal |= 1 << uint(sig-1)
		}
	}
	if !found {
		return -ESRCH
	}
	return 0
}

func SignalName(sig int) string {
	names := [...]string{
		"",
		"SIGHUP", "SIGINT", "SIGQUIT", "SIGILL",
		"SIGTRAP", "SIGABRT", "SIGBUS", "SIGFPE",
		"SIGKILL", "SIGUSR1", "SIGSEGV", "SIGUSR2",
		"SIGPIPE", "SIGALRM", "SIGTERM", "SIGSTKFLT",
		"SIGCHLD", "SIGCONT", "SIGSTOP", "SIGTSTP",
		"SIGTTIN", "SIGTTOU", "SIGURG", "SIGXCPU",
		"SIGXFSZ", "SIGVTALRM", "SIGPROF", "SIGWINCH",
		"SIGIO", "SIGPWR",
	}
	if sig < 1 || sig >= len(names) {
		return "SIG??"
	}
	return names[sig]
}

func CountPendingSignals(task *TaskStruct) int {
	count := 0
	pending := task.Signal & ^task.Blocked
	for i := 0; i < 32; i++ {
		if pending&(1<<uint(i)) != 0 {
			count++
		}
	}
	return count
}
