package kernel

import (
	"google.golang.org/adk/v2/include"
)

var lastPid int32

var writeVerifyFn func(address uint32)

func SetWriteVerify(fn func(uint32)) { writeVerifyFn = fn }

func VerifyArea(addr uint32, size int32) {
	start := addr
	size += int32(start & 0xfff)
	start &= 0xfffff000
	start += include.GetBase(include.Current.Ldt[2])
	for size > 0 {
		size -= 4096
		if writeVerifyFn != nil {
			writeVerifyFn(start)
		}
		start += 4096
	}
}

var copyPageTablesFn func(from, to uint32, size int32) int
var freePageTablesFn func(from uint32, size int32) int

func SetCopyPageTables(fn func(uint32, uint32, int32) int) { copyPageTablesFn = fn }
func SetFreePageTables(fn func(uint32, int32) int)         { freePageTablesFn = fn }

func copyMem(nr int, p *include.TaskStruct) int {
	codeLimit := include.GetLimit(include.Current.Ldt[1])
	dataLimit := include.GetLimit(include.Current.Ldt[2])
	oldCodeBase := include.GetBase(include.Current.Ldt[1])
	oldDataBase := include.GetBase(include.Current.Ldt[2])

	if oldDataBase != oldCodeBase {
		Panic("separate I&D not supported")
	}
	if dataLimit < codeLimit {
		Panic("bad data_limit")
	}

	newBase := uint32(nr) * 0x4000000
	p.StartCode = newBase
	include.SetBase(&p.Ldt[1], newBase)
	include.SetBase(&p.Ldt[2], newBase)

	if copyPageTablesFn == nil {
		return 0
	}
	if copyPageTablesFn(oldDataBase, newBase, int32(dataLimit)) != 0 {
		Printk("free_page_tables: from copy_mem\n")
		if freePageTablesFn != nil {
			freePageTablesFn(newBase, int32(dataLimit))
		}
		return -include.ENOMEM
	}
	return 0
}

func CopyProcess(nr int, ebp, edi, esi, gs int32,
	ebx, ecx, edx int32,
	fs, es, ds int32,
	eip, cs, eflags, esp, ss int32) int32 {

	p := &include.TaskStruct{}
	*p = *include.Current

	include.Task[nr] = p

	p.State = include.TASK_UNINTERRUPTIBLE
	p.Pid = lastPid
	p.Father = include.Current.Pid
	p.Counter = p.Priority
	p.Signal = 0
	p.Alarm = 0
	p.Leader = 0
	p.Utime = 0
	p.Stime = 0
	p.Cutime = 0
	p.Cstime = 0
	p.StartTime = include.Jiffies
	p.TokenUsed = 0
	p.ContextSize = 0

	p.Tss = include.TssStruct{
		Esp0:        include.PAGE_SIZE,
		Ss0:         0x10,
		Eip:         eip,
		Eflags:      eflags,
		Eax:         0,
		Ecx:         ecx,
		Edx:         edx,
		Ebx:         ebx,
		Esp:         esp,
		Ebp:         ebp,
		Esi:         esi,
		Edi:         edi,
		Es:          es & 0xffff,
		Cs:          cs & 0xffff,
		Ss:          ss & 0xffff,
		Ds:          ds & 0xffff,
		Fs:          fs & 0xffff,
		Gs:          gs & 0xffff,
		Ldt:         int32(include.LDT(nr)),
		TraceBitmap: 0x80000000,
	}

	if copyMem(nr, p) != 0 {
		include.Task[nr] = nil
		return -include.EAGAIN
	}

	for i := 0; i < include.NR_OPEN; i++ {
		if f := p.Filp[i]; f != nil {
			f.FCount++
		}
	}

	if include.Current.Pwd != nil {
		include.Current.Pwd.ICount++
	}
	if include.Current.Root != nil {
		include.Current.Root.ICount++
	}
	if include.Current.Executable != nil {
		include.Current.Executable.ICount++
	}

	p.State = include.TASK_RUNNING
	return lastPid
}

func FindEmptyProcess() int {
	for {
		lastPid++
		if lastPid < 0 {
			lastPid = 1
		}

		conflict := false
		for i := 0; i < include.NR_TASKS; i++ {
			if include.Task[i] != nil && include.Task[i].Pid == lastPid {
				conflict = true
				break
			}
		}
		if !conflict {
			break
		}
	}

	for i := 1; i < include.NR_TASKS; i++ {
		if include.Task[i] == nil {
			return i
		}
	}
	return -include.EAGAIN
}
