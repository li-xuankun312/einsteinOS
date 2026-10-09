package kernel

import (
	. "google.golang.org/adk/v2/include"
)

var lastPid int32 = 0

func VerifyArea(addr uint32, size int32) {
	start := addr
	size += int32(start & 0xfff)
	start &= 0xfffff000
	start += GetBase(Current.Ldt[2])
	for size > 0 {
		size -= 4096
		if writeVerifyFn != nil {
			writeVerifyFn(start)
		}
		start += 4096
	}
}

var writeVerifyFn func(address uint32)

func SetWriteVerify(fn func(uint32)) {
	writeVerifyFn = fn
}

func copyMem(nr int, p *TaskStruct) int {
	var oldDataBase, newDataBase, dataLimit uint32
	var oldCodeBase, newCodeBase, codeLimit uint32

	codeLimit = GetLimit(Current.Ldt[1])
	dataLimit = GetLimit(Current.Ldt[2])
	oldCodeBase = GetBase(Current.Ldt[1])
	oldDataBase = GetBase(Current.Ldt[2])

	if oldDataBase != oldCodeBase {
		Panic("We don't support separate I&D")
	}
	if dataLimit < codeLimit {
		Panic("Bad data_limit")
	}

	newDataBase = uint32(nr) * 0x4000000
	newCodeBase = newDataBase
	p.StartCode = newCodeBase

	SetBase(&p.Ldt[1], newCodeBase)
	SetBase(&p.Ldt[2], newDataBase)

	if copyPageTablesFn != nil {
		if copyPageTablesFn(oldDataBase, newDataBase, int32(dataLimit)) != 0 {
			Printk("free_page_tables: from copy_mem\n")
			if freePageTablesFn != nil {
				freePageTablesFn(newDataBase, int32(dataLimit))
			}
			return -ENOMEM
		}
	}
	return 0
}

var copyPageTablesFn func(from, to uint32, size int32) int
var freePageTablesFn func(from uint32, size int32) int

func SetCopyPageTables(fn func(uint32, uint32, int32) int) { copyPageTablesFn = fn }
func SetFreePageTables(fn func(uint32, int32) int)         { freePageTablesFn = fn }

func CopyProcess(nr int, ebp, edi, esi, gs int32,
	ebx, ecx, edx int32,
	fs, es, ds int32,
	eip, cs, eflags, esp, ss int32) int32 {

	p := &TaskStruct{}

	Task[nr] = p

	copyTaskStruct(p, Current)

	p.State = TASK_UNINTERRUPTIBLE
	p.Pid = lastPid
	p.Father = Current.Pid
	p.Counter = p.Priority
	p.Signal = 0
	p.Alarm = 0
	p.Leader = 0
	p.Utime = 0
	p.Stime = 0
	p.Cutime = 0
	p.Cstime = 0
	p.StartTime = Jiffies

	p.Tss.BackLink = 0
	p.Tss.Esp0 = PAGE_SIZE
	p.Tss.Ss0 = 0x10
	p.Tss.Eip = eip
	p.Tss.Eflags = eflags
	p.Tss.Eax = 0
	p.Tss.Ecx = ecx
	p.Tss.Edx = edx
	p.Tss.Ebx = ebx
	p.Tss.Esp = esp
	p.Tss.Ebp = ebp
	p.Tss.Esi = esi
	p.Tss.Edi = edi
	p.Tss.Es = es & 0xffff
	p.Tss.Cs = cs & 0xffff
	p.Tss.Ss = ss & 0xffff
	p.Tss.Ds = ds & 0xffff
	p.Tss.Fs = fs & 0xffff
	p.Tss.Gs = gs & 0xffff
	p.Tss.Ldt = int32(LDT(nr))
	p.Tss.TraceBitmap = 0x80000000

	if LastTaskUsedMath == Current {
	}

	if copyMem(nr, p) != 0 {
		Task[nr] = nil
		return -EAGAIN
	}

	for i := 0; i < NR_OPEN; i++ {
		f := p.Filp[i]
		if f != nil {
			f.FCount++
		}
	}

	if Current.Pwd != nil {
		Current.Pwd.ICount++
	}
	if Current.Root != nil {
		Current.Root.ICount++
	}
	if Current.Executable != nil {
		Current.Executable.ICount++
	}

	p.State = TASK_RUNNING

	return lastPid
}

func FindEmptyProcess() int {
repeat:
	lastPid++
	if lastPid < 0 {
		lastPid = 1
	}
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil && Task[i].Pid == lastPid {
			goto repeat
		}
	}
	for i := 1; i < NR_TASKS; i++ {
		if Task[i] == nil {
			return i
		}
	}
	return -EAGAIN
}

func copyTaskStruct(dst, src *TaskStruct) {
	dst.State = src.State
	dst.Counter = src.Counter
	dst.Priority = src.Priority
	dst.Signal = src.Signal
	dst.Sigaction = src.Sigaction
	dst.Blocked = src.Blocked
	dst.ExitCode = src.ExitCode
	dst.StartCode = src.StartCode
	dst.EndCode = src.EndCode
	dst.EndData = src.EndData
	dst.Brk = src.Brk
	dst.StartStack = src.StartStack
	dst.Pid = src.Pid
	dst.Father = src.Father
	dst.Pgrp = src.Pgrp
	dst.Session = src.Session
	dst.Leader = src.Leader
	dst.Uid = src.Uid
	dst.Euid = src.Euid
	dst.Suid = src.Suid
	dst.Gid = src.Gid
	dst.Egid = src.Egid
	dst.Sgid = src.Sgid
	dst.Alarm = src.Alarm
	dst.Utime = src.Utime
	dst.Stime = src.Stime
	dst.Cutime = src.Cutime
	dst.Cstime = src.Cstime
	dst.StartTime = src.StartTime
	dst.UsedMath = src.UsedMath
	dst.Tty = src.Tty
	dst.Umask = src.Umask
	dst.Pwd = src.Pwd
	dst.Root = src.Root
	dst.Executable = src.Executable
	dst.CloseOnExec = src.CloseOnExec
	dst.Filp = src.Filp
	dst.Ldt = src.Ldt
	dst.Tss = src.Tss
	dst.TokenBudget = src.TokenBudget
	dst.TokenUsed = 0
	dst.ContextSize = 0
	dst.ContextLimit = src.ContextLimit
	dst.Effort = src.Effort
}
