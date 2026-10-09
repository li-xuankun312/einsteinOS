package kernel

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	. "google.golang.org/adk/v2/include"
)

func _S(nr int) int32          { return 1 << (nr - 1) }

var _BLOCKABLE int32 = ^(_S(SIGKILL) | _S(SIGSTOP))

func ShowTask(nr int, p *TaskStruct) {
	stateStr := "?"
	switch p.State {
	case TASK_RUNNING:
		stateStr = "RUNNING"
	case TASK_INTERRUPTIBLE:
		stateStr = "INTERRUPTIBLE"
	case TASK_UNINTERRUPTIBLE:
		stateStr = "UNINTERRUPTIBLE"
	case TASK_ZOMBIE:
		stateStr = "ZOMBIE"
	case TASK_STOPPED:
		stateStr = "STOPPED"
	}
	fmt.Fprintf(os.Stderr, "%d: pid=%d, state=%s, father=%d\n",
		nr, p.Pid, stateStr, p.Father)
}

func ShowStat() {
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil {
			ShowTask(i, Task[i])
		}
	}
}

var schedMu sync.Mutex

var RunCh [NR_TASKS]chan struct{}

func switchTo(n int) {
	if Task[n] == Current {
		return
	}
	Current = Task[n]
}

func MathStateRestore() {
}

func Schedule() {
	var i, next int
	var c int32

	for i = NR_TASKS - 1; i > 0; i-- {
		p := Task[i]
		if p == nil {
			continue
		}
		if p.Alarm != 0 && p.Alarm < Jiffies {
			p.Signal |= 1 << (SIGALRM - 1)
			p.Alarm = 0
		}
		if (p.Signal & ^(_BLOCKABLE & p.Blocked)) != 0 &&
			p.State == TASK_INTERRUPTIBLE {
			p.State = TASK_RUNNING
		}
	}

	for {
		c = -1
		next = 0
		i = NR_TASKS
		for i > 1 {
			i--
			if Task[i] == nil {
				continue
			}
			if Task[i].State == TASK_RUNNING && Task[i].Counter > c {
				c = Task[i].Counter
				next = i
			}
		}
		if c != 0 {
			break
		}
		for i = NR_TASKS - 1; i > 0; i-- {
			if Task[i] != nil {
				Task[i].Counter = (Task[i].Counter >> 1) + Task[i].Priority
			}
		}
	}
	switchTo(next)
}

func SysPause() int {
	Current.State = TASK_INTERRUPTIBLE
	Schedule()
	return 0
}

func SleepOn(p **TaskStruct) {
	if p == nil {
		return
	}
	if Current == Task[0] {
		Printk("task[0] trying to sleep\n")
		return
	}
	tmp := *p
	*p = Current
	Current.State = TASK_UNINTERRUPTIBLE
	Schedule()
	if tmp != nil {
		tmp.State = 0
	}
}

func InterruptibleSleepOn(p **TaskStruct) {
	if p == nil {
		return
	}
	if Current == Task[0] {
		Printk("task[0] trying to sleep\n")
		return
	}
	tmp := *p
	*p = Current
repeat:
	Current.State = TASK_INTERRUPTIBLE
	Schedule()
	if *p != nil && *p != Current {
		(*p).State = 0
		goto repeat
	}
	*p = nil
	if tmp != nil {
		tmp.State = 0
	}
}

func WakeUp(p **TaskStruct) {
	if p != nil && *p != nil {
		(*p).State = 0
		*p = nil
	}
}

var (
	waitMotor  [4]*TaskStruct
	monTimer   [4]int32
	moffTimer  [4]int32
	currentDOR uint8 = 0x0C
)

func TicksToFloppyOn(nr uint32) int32 {
	if nr > 3 {
		Panic("floppy_on: nr>3")
	}
	return 0
}

func FloppyOn(nr uint32) {
}

func FloppyOff(nr uint32) {
	moffTimer[nr] = 3 * HZ
}

func DoFloppyTimer() {
}

const TIME_REQUESTS = 64

type timerListEntry struct {
	jiffies int32
	fn      func()
	next    *timerListEntry
}

var (
	timerList [TIME_REQUESTS]timerListEntry
	nextTimer *timerListEntry
	timerMu   sync.Mutex
)

func AddTimer(ticks int32, fn func()) {
	if fn == nil {
		return
	}
	timerMu.Lock()
	defer timerMu.Unlock()

	if ticks <= 0 {
		fn()
		return
	}

	var p *timerListEntry
	for i := 0; i < TIME_REQUESTS; i++ {
		if timerList[i].fn == nil {
			p = &timerList[i]
			break
		}
	}
	if p == nil {
		Panic("No more time requests free")
	}
	p.fn = fn
	p.jiffies = ticks
	p.next = nextTimer
	nextTimer = p

	for p.next != nil && p.next.jiffies < p.jiffies {
		p.jiffies -= p.next.jiffies
		p.fn, p.next.fn = p.next.fn, p.fn
		p.jiffies, p.next.jiffies = p.next.jiffies, p.jiffies
		p = p.next
	}
}

func DoTimer(cpl int32) {
	if cpl != 0 {
		Current.Utime++
	} else {
		Current.Stime++
	}

	timerMu.Lock()
	if nextTimer != nil {
		nextTimer.jiffies--
		for nextTimer != nil && nextTimer.jiffies <= 0 {
			fn := nextTimer.fn
			nextTimer.fn = nil
			nextTimer = nextTimer.next
			if fn != nil {
				timerMu.Unlock()
				fn()
				timerMu.Lock()
			}
		}
	}
	timerMu.Unlock()

	if currentDOR&0xf0 != 0 {
		DoFloppyTimer()
	}

	Current.Counter--
	if Current.Counter > 0 {
		return
	}
	Current.Counter = 0
	if cpl == 0 {
		return
	}
	Schedule()
}

func SysAlarm(seconds int32) int32 {
	old := Current.Alarm
	if old != 0 {
		old = (old - Jiffies) / HZ
	}
	if seconds > 0 {
		Current.Alarm = Jiffies + HZ*seconds
	} else {
		Current.Alarm = 0
	}
	return old
}

func SysGetpid() int32  { return Current.Pid }
func SysGetppid() int32 { return Current.Father }
func SysGetuid() uint16 { return Current.Uid }
func SysGeteuid() uint16 { return Current.Euid }
func SysGetgid() uint16 { return Current.Gid }
func SysGetegid() uint16 { return Current.Egid }

func SysNice(increment int32) int {
	if Current.Priority-increment > 0 {
		Current.Priority -= increment
	}
	return 0
}

func SchedInit() {

	initTask := &TaskStruct{}
	initTask.State = 0
	initTask.Counter = 15
	initTask.Priority = 15
	initTask.Signal = 0
	initTask.Blocked = 0
	initTask.ExitCode = 0
	initTask.StartCode = 0
	initTask.EndCode = 0
	initTask.EndData = 0
	initTask.Brk = 0
	initTask.StartStack = 0
	initTask.Pid = 0
	initTask.Father = -1
	initTask.Pgrp = 0
	initTask.Session = 0
	initTask.Leader = 1
	initTask.Uid = 0
	initTask.Euid = 0
	initTask.Suid = 0
	initTask.Gid = 0
	initTask.Egid = 0
	initTask.Sgid = 0
	initTask.Alarm = 0
	initTask.Utime = 0
	initTask.Stime = 0
	initTask.Cutime = 0
	initTask.Cstime = 0
	initTask.StartTime = 0
	initTask.UsedMath = 0
	initTask.Tty = -1
	initTask.Umask = 0022
	initTask.Pwd = nil
	initTask.Root = nil
	initTask.Executable = nil
	initTask.CloseOnExec = 0
	initTask.Ldt[0] = DescStruct{A: 0, B: 0}
	initTask.Ldt[1] = DescStruct{A: 0x9f, B: 0xc0fa00}
	initTask.Ldt[2] = DescStruct{A: 0x9f, B: 0xc0f200}
	initTask.Tss.Esp0 = PAGE_SIZE
	initTask.Tss.Ss0 = 0x10
	initTask.Tss.Cr3 = 0
	initTask.Tss.Ldt = int32(LDT(0))
	initTask.Tss.TraceBitmap = 0x80000000

	for i := 1; i < NR_TASKS; i++ {
		Task[i] = nil
	}
	Task[0] = initTask
	Current = initTask
	LastTaskUsedMath = nil

	Jiffies = 0
	StartupTime = int32(time.Now().Unix())

	go timerTicker()

	log.Printf("kernel: sched_init done")
}

func timerTicker() {
	ticker := time.NewTicker(time.Second / HZ)
	defer ticker.Stop()
	for range ticker.C {
		schedMu.Lock()
		Jiffies++
		DoTimer(1)
		schedMu.Unlock()
	}
}
