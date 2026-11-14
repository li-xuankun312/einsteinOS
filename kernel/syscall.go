package kernel

import (
	"fmt"

	. "google.golang.org/adk/v2/include"
)

const NR_SYSCALLS = 72

type SyscallFunc func(args ...int32) int32

var SyscallTable [NR_SYSCALLS]SyscallFunc

func SyscallInit() {
	SyscallTable[0] = sysWrap0(SysSetup0)
	SyscallTable[1] = sysWrap1(func(a int32) int32 { SysExit(a); return 0 })
	SyscallTable[2] = sysWrap0(func() int32 { return int32(FindEmptyProcess()) })
	SyscallTable[4] = sysWrap1(func(a int32) int32 { return int32(SysWrite0(a)) })
	SyscallTable[7] = sysWrap1(SysWaitpid0)
	SyscallTable[9] = sysWrap0(func() int32 { return -ENOSYS })
	SyscallTable[11] = sysWrap0(func() int32 { return -ENOSYS })
	SyscallTable[13] = sysWrap0(func() int32 { return SysTime(nil) })
	SyscallTable[17] = sysWrap0(func() int32 { return -ENOSYS })
	SyscallTable[20] = sysWrap0(SysGetpid)
	SyscallTable[23] = sysWrap1(func(a int32) int32 { return int32(SysSetuid(a)) })
	SyscallTable[24] = sysWrap0(func() int32 { return int32(SysGetuid()) })
	SyscallTable[25] = sysWrap0(func() int32 { return -ENOSYS })
	SyscallTable[27] = sysWrap1(SysAlarm)
	SyscallTable[29] = sysWrap0(SysPause0)
	SyscallTable[30] = sysWrap0(func() int32 { return -ENOSYS })
	SyscallTable[37] = sysWrap0(func() int32 { return -ENOSYS })
	SyscallTable[39] = sysWrap0(func() int32 { return -ENOSYS })
	SyscallTable[43] = sysWrap0(func() int32 { return int32(SysTimes(nil)) })
	SyscallTable[46] = sysWrap1(func(a int32) int32 { return int32(SysSetgid(a)) })
	SyscallTable[47] = sysWrap0(func() int32 { return int32(SysGetgid()) })
	SyscallTable[48] = sysWrap0(func() int32 { return -ENOSYS })
	SyscallTable[49] = sysWrap0(func() int32 { return int32(SysGeteuid()) })
	SyscallTable[50] = sysWrap0(func() int32 { return int32(SysGetegid()) })
	SyscallTable[51] = sysWrap0(func() int32 { return -ENOSYS })
	SyscallTable[57] = sysWrap1(func(a int32) int32 { return int32(SysSetpgid(a, 0)) })
	SyscallTable[59] = sysWrap0(func() int32 { return -ENOSYS })
	SyscallTable[60] = sysWrap1(func(a int32) int32 { return int32(SysUmask(uint16(a))) })
	SyscallTable[63] = sysWrap0(func() int32 { return -ENOSYS })
	SyscallTable[64] = sysWrap0(SysGetppid)
	SyscallTable[65] = sysWrap0(SysGetpgrp)
	SyscallTable[66] = sysWrap0(SysSetsid)
	SyscallTable[68] = sysWrap0(func() int32 { return SysSgetmask() })
	SyscallTable[69] = sysWrap1(SysSsetmask)
	SyscallTable[70] = sysWrap0(func() int32 { return -ENOSYS })
	SyscallTable[71] = sysWrap0(func() int32 { return -ENOSYS })

	Printk("kernel: syscall table initialized, %d entries\n", NR_SYSCALLS)
}

func SysCall(nr int, args ...int32) int32 {
	if nr < 0 || nr >= NR_SYSCALLS || SyscallTable[nr] == nil {
		return -ENOSYS
	}
	return SyscallTable[nr](args...)
}

func SysSetup0() int32 { return 0 }

func SysPause0() int32 {
	Current.State = TASK_INTERRUPTIBLE
	Schedule()
	return 0
}

func SysWrite0(fd int32) int32 { return 0 }

func SysWaitpid0(pid int32) int32 {
	var stat int32
	return SysWaitpid(pid, &stat, 0)
}

func sysWrap0(fn func() int32) SyscallFunc {
	return func(args ...int32) int32 { return fn() }
}

func sysWrap1(fn func(int32) int32) SyscallFunc {
	return func(args ...int32) int32 {
		if len(args) < 1 {
			return -EINVAL
		}
		return fn(args[0])
	}
}

func sysWrap2(fn func(int32, int32) int32) SyscallFunc {
	return func(args ...int32) int32 {
		if len(args) < 2 {
			return -EINVAL
		}
		return fn(args[0], args[1])
	}
}

func SyscallName(nr int) string {
	names := map[int]string{
		0: "setup", 1: "exit", 2: "fork", 3: "read", 4: "write",
		5: "open", 6: "close", 7: "waitpid", 8: "creat", 9: "link",
		10: "unlink", 11: "execve", 12: "chdir", 13: "time", 14: "mknod",
		15: "chmod", 16: "chown", 17: "break", 18: "stat", 19: "lseek",
		20: "getpid", 21: "mount", 22: "umount", 23: "setuid", 24: "getuid",
		25: "stime", 26: "ptrace", 27: "alarm", 28: "fstat", 29: "pause",
		30: "utime", 33: "access", 34: "nice", 36: "sync", 37: "kill",
		38: "rename", 39: "mkdir", 40: "rmdir", 41: "dup", 42: "pipe",
		43: "times", 45: "brk", 46: "setgid", 47: "getgid", 48: "signal",
		49: "geteuid", 50: "getegid", 51: "acct", 52: "phys", 54: "ioctl",
		55: "fcntl", 57: "setpgid", 59: "ulimit", 60: "umask", 61: "chroot",
		62: "ustat", 63: "dup2", 64: "getppid", 65: "getpgrp", 66: "setsid",
		67: "sigaction", 68: "sgetmask", 69: "ssetmask", 70: "setreuid",
		71: "setregid",
	}
	if n, ok := names[nr]; ok {
		return n
	}
	return fmt.Sprintf("sys_%d", nr)
}
