package include

import "sync"

const (
	NR_TASKS = 64
	HZ       = 100
)

const (
	TASK_RUNNING         = 0
	TASK_INTERRUPTIBLE   = 1
	TASK_UNINTERRUPTIBLE = 2
	TASK_ZOMBIE          = 3
	TASK_STOPPED         = 4
)

type FnPtr func()

type I387Struct struct {
	Cwd     int32
	Swd     int32
	Twd     int32
	Fip     int32
	Fcs     int32
	Foo     int32
	Fos     int32
	StSpace [20]int32
}

type TssStruct struct {
	BackLink int32
	Esp0     int32
	Ss0      int32
	Esp1     int32
	Ss1      int32
	Esp2     int32
	Ss2      int32
	Cr3      int32
	Eip      int32
	Eflags   int32
	Eax      int32
	Ecx      int32
	Edx      int32
	Ebx      int32
	Esp      int32
	Ebp      int32
	Esi      int32
	Edi      int32
	Es       int32
	Cs       int32
	Ss       int32
	Ds       int32
	Fs       int32
	Gs       int32
	Ldt      int32
	TraceBitmap uint32
	I387     I387Struct
}

type TaskStruct struct {
	State    int32
	Counter  int32
	Priority int32
	Signal   int32
	Sigaction [32]Sigaction
	Blocked  int32

	ExitCode   int32
	StartCode  uint32
	EndCode    uint32
	EndData    uint32
	Brk        uint32
	StartStack uint32
	Pid        int32
	Father     int32
	Pgrp       int32
	Session    int32
	Leader     int32
	Uid        uint16
	Euid       uint16
	Suid       uint16
	Gid        uint16
	Egid       uint16
	Sgid       uint16
	Alarm      int32
	Utime      int32
	Stime      int32
	Cutime     int32
	Cstime     int32
	StartTime  int32
	UsedMath   uint16

	Tty        int32
	Umask      uint16
	Pwd        *MInode
	Root       *MInode
	Executable *MInode
	CloseOnExec uint32
	Filp       [NR_OPEN]*File

	Ldt [3]DescStruct
	Tss TssStruct

	Mu   sync.Mutex

	TokenBudget  int64
	TokenUsed    int64
	ContextSize  int64
	ContextLimit int64
	Effort       uint8
}

var (
	Task             [NR_TASKS]*TaskStruct
	LastTaskUsedMath *TaskStruct
	Current          *TaskStruct
	Jiffies          int32
	StartupTime      int32
)

func CURRENT_TIME() int32 {
	return StartupTime + Jiffies/HZ
}

func FIRST_TASK() *TaskStruct { return Task[0] }
func LAST_TASK() *TaskStruct  { return Task[NR_TASKS-1] }

const (
	FIRST_TSS_ENTRY = 4
	FIRST_LDT_ENTRY = FIRST_TSS_ENTRY + 1
)

func TSS(n int) uint32 { return uint32(n)<<4 + FIRST_TSS_ENTRY<<3 }
func LDT(n int) uint32 { return uint32(n)<<4 + FIRST_LDT_ENTRY<<3 }

func PAGE_ALIGN(n uint32) uint32 { return (n + 0xfff) &^ 0xfff }

func SetBase(desc *DescStruct, base uint32) {
	desc.A = (desc.A & 0x0000FFFF) | ((base & 0x0000FFFF) << 16)
	desc.B = (desc.B & 0x00FFFF00) | ((base >> 16) & 0xFF) | (base & 0xFF000000)
}

func GetBase(desc DescStruct) uint32 {
	return (desc.A >> 16) | ((desc.B & 0xFF) << 16) | (desc.B & 0xFF000000)
}

func SetLimit(desc *DescStruct, limit uint32) {
	limit = (limit - 1) >> 12
	desc.A = (desc.A & 0xFFFF0000) | (limit & 0x0000FFFF)
	desc.B = (desc.B & 0xFFF0FFFF) | (limit & 0x000F0000)
}

func GetLimit(desc DescStruct) uint32 {
	limit := (desc.A & 0x0000FFFF) | (desc.B & 0x000F0000)
	return (limit << 12) | 0xFFF
}
