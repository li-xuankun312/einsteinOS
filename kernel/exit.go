package kernel

import (
	. "google.golang.org/adk/v2/include"
)

const (
	WNOHANG   = 1
	WUNTRACED = 2
)

func Release(p *TaskStruct) {
	if p == nil {
		return
	}
	for i := 1; i < NR_TASKS; i++ {
		if Task[i] == p {
			Task[i] = nil
			Schedule()
			return
		}
	}
}

func SendSig(sig int32, p *TaskStruct, priv int) int {
	if p == nil || sig < 1 || sig > 32 {
		return -EINVAL
	}
	if priv != 0 || Current.Euid == p.Euid || suser() {
		p.Signal |= (1 << (sig - 1))
	} else {
		return -EPERM
	}
	return 0
}

func suser() bool {
	return Current.Euid == 0
}

func KillSession() {
	for i := NR_TASKS - 1; i > 0; i-- {
		if Task[i] != nil && Task[i].Session == Current.Session {
			Task[i].Signal |= 1 << (SIGHUP - 1)
		}
	}
}

func SysKill(pid int32, sig int32) int {
	retval := 0

	if pid == 0 {
		for i := NR_TASKS - 1; i > 0; i-- {
			if Task[i] != nil && Task[i].Pgrp == Current.Pid {
				if err := SendSig(sig, Task[i], 1); err != 0 {
					retval = err
				}
			}
		}
	} else if pid > 0 {
		for i := NR_TASKS - 1; i > 0; i-- {
			if Task[i] != nil && Task[i].Pid == pid {
				if err := SendSig(sig, Task[i], 0); err != 0 {
					retval = err
				}
			}
		}
	} else if pid == -1 {
		for i := NR_TASKS - 1; i > 0; i-- {
			if err := SendSig(sig, Task[i], 0); err != 0 {
				retval = err
			}
		}
	} else {
		for i := NR_TASKS - 1; i > 0; i-- {
			if Task[i] != nil && Task[i].Pgrp == -pid {
				if err := SendSig(sig, Task[i], 0); err != 0 {
					retval = err
				}
			}
		}
	}
	return retval
}

func TellFather(pid int32) {
	if pid < 0 {
		return
	}
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] == nil {
			continue
		}
		if Task[i].Pid != pid {
			continue
		}
		Task[i].Signal |= (1 << (SIGCHLD - 1))
		return
	}
	if Task[1] != nil {
		Task[1].Signal |= (1 << (SIGCHLD - 1))
	}
}

func DoExit(code int32) int {
	if freePageTablesFn != nil {
		freePageTablesFn(GetBase(Current.Ldt[1]),
			int32(GetLimit(Current.Ldt[1])))
		freePageTablesFn(GetBase(Current.Ldt[2]),
			int32(GetLimit(Current.Ldt[2])))
	}

	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil && Task[i].Father == Current.Pid {
			Task[i].Father = 1
			if Task[i].State == TASK_ZOMBIE {
				if Task[1] != nil {
					SendSig(SIGCHLD, Task[1], 1)
				}
			}
		}
	}

	for i := 0; i < NR_OPEN; i++ {
		if Current.Filp[i] != nil {
			SysClose(int32(i))
		}
	}

	if iputFn != nil {
		if Current.Pwd != nil {
			iputFn(Current.Pwd)
		}
		Current.Pwd = nil
		if Current.Root != nil {
			iputFn(Current.Root)
		}
		Current.Root = nil
		if Current.Executable != nil {
			iputFn(Current.Executable)
		}
		Current.Executable = nil
	}

	if Current.Leader != 0 && Current.Tty >= 0 {
	}

	if LastTaskUsedMath == Current {
		LastTaskUsedMath = nil
	}

	if Current.Leader != 0 {
		KillSession()
	}

	Current.State = TASK_ZOMBIE
	Current.ExitCode = code
	TellFather(Current.Father)

	Schedule()
	return -1
}

var iputFn func(*MInode)

func SetIput(fn func(*MInode)) { iputFn = fn }

func SysClose(fd int32) int {
	if fd < 0 || fd >= NR_OPEN {
		return -1
	}
	if Current.Filp[fd] != nil {
		Current.Filp[fd].FCount--
		if Current.Filp[fd].FCount == 0 {
		}
		Current.Filp[fd] = nil
	}
	return 0
}

func SysExit(errorCode int32) int {
	return DoExit((errorCode & 0xff) << 8)
}

func SysWaitpid(pid int32, statAddr *int32, options int32) int32 {
	var flag int32

repeat:
	flag = 0
	for i := NR_TASKS - 1; i > 0; i-- {
		p := Task[i]
		if p == nil || p == Current {
			continue
		}
		if p.Father != Current.Pid {
			continue
		}
		if pid > 0 {
			if p.Pid != pid {
				continue
			}
		} else if pid == 0 {
			if p.Pgrp != Current.Pgrp {
				continue
			}
		} else if pid != -1 {
			if p.Pgrp != -pid {
				continue
			}
		}
		switch p.State {
		case TASK_STOPPED:
			if options&WUNTRACED == 0 {
				continue
			}
			if statAddr != nil {
				*statAddr = 0x7f
			}
			return p.Pid

		case TASK_ZOMBIE:
			Current.Cutime += p.Utime
			Current.Cstime += p.Stime
			childPid := p.Pid
			code := p.ExitCode
			Release(p)
			if statAddr != nil {
				*statAddr = code
			}
			return childPid

		default:
			flag = 1
			continue
		}
	}

	if flag != 0 {
		if options&WNOHANG != 0 {
			return 0
		}
		Current.State = TASK_INTERRUPTIBLE
		Schedule()
		if Current.Signal&^(1<<(SIGCHLD-1)) == 0 {
			Current.Signal &= ^int32(1 << (SIGCHLD - 1))
			goto repeat
		}
		return -EINTR
	}
	return -ECHILD
}
