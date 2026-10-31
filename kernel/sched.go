package kernel

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"google.golang.org/adk/v2/include"
)

func signalBit(nr int) int32 { return 1 << (nr - 1) }

var blockable int32 = ^(signalBit(include.SIGKILL) | signalBit(include.SIGSTOP))

var schedMu sync.Mutex

func ShowTask(nr int, p *include.TaskStruct) {
	names := [5]string{"RUNNING", "INTERRUPTIBLE", "UNINTERRUPTIBLE", "ZOMBIE", "STOPPED"}
	s := "?"
	if p.State >= 0 && p.State < 5 {
		s = names[p.State]
	}
	fmt.Fprintf(os.Stderr, "%d: pid=%d state=%s father=%d counter=%d prio=%d\n",
		nr, p.Pid, s, p.Father, p.Counter, p.Priority)
}

func ShowStat() {
	for i := 0; i < include.NR_TASKS; i++ {
		if include.Task[i] != nil {
			ShowTask(i, include.Task[i])
		}
	}
}

func switchTo(n int) {
	if include.Task[n] == include.Current {
		return
	}
	include.Current = include.Task[n]
}

func Schedule() {
	for i := include.NR_TASKS - 1; i > 0; i-- {
		p := include.Task[i]
		if p == nil {
			continue
		}
		if p.Alarm != 0 && p.Alarm < include.Jiffies {
			p.Signal |= signalBit(include.SIGALRM)
			p.Alarm = 0
		}
		if (p.Signal & ^(blockable & p.Blocked)) != 0 &&
			p.State == include.TASK_INTERRUPTIBLE {
			p.State = include.TASK_RUNNING
		}
	}

	for {
		c := int32(-1)
		next := 0
		for i := include.NR_TASKS - 1; i > 0; i-- {
			t := include.Task[i]
			if t == nil {
				continue
			}
			if t.State == include.TASK_RUNNING && t.Counter > c {
				c = t.Counter
				next = i
			}
		}
		if c != 0 {
			switchTo(next)
			return
		}
		for i := include.NR_TASKS - 1; i > 0; i-- {
			if include.Task[i] != nil {
				include.Task[i].Counter = (include.Task[i].Counter >> 1) + include.Task[i].Priority
			}
		}
	}
}

func SysPause() int {
	include.Current.State = include.TASK_INTERRUPTIBLE
	Schedule()
	return 0
}

func SleepOn(p **include.TaskStruct) {
	if p == nil {
		return
	}
	if include.Current == include.Task[0] {
		Printk("task[0] trying to sleep\n")
		return
	}
	tmp := *p
	*p = include.Current
	include.Current.State = include.TASK_UNINTERRUPTIBLE
	Schedule()
	if tmp != nil {
		tmp.State = include.TASK_RUNNING
	}
}

func InterruptibleSleepOn(p **include.TaskStruct) {
	if p == nil {
		return
	}
	if include.Current == include.Task[0] {
		Printk("task[0] trying to sleep\n")
		return
	}
	tmp := *p
	*p = include.Current

	for {
		include.Current.State = include.TASK_INTERRUPTIBLE
		Schedule()
		if *p == nil || *p == include.Current {
			break
		}
		(*p).State = include.TASK_RUNNING
	}

	*p = nil
	if tmp != nil {
		tmp.State = include.TASK_RUNNING
	}
}

func WakeUp(p **include.TaskStruct) {
	if p != nil && *p != nil {
		(*p).State = include.TASK_RUNNING
		*p = nil
	}
}

const maxTimerRequests = 64

type timerEntry struct {
	jiffies int32
	fn      func()
	next    *timerEntry
}

var (
	timerPool [maxTimerRequests]timerEntry
	timerHead *timerEntry
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

	var entry *timerEntry
	for i := range timerPool {
		if timerPool[i].fn == nil {
			entry = &timerPool[i]
			break
		}
	}
	if entry == nil {
		Panic("no free timer slots")
	}

	entry.fn = fn
	entry.jiffies = ticks
	entry.next = timerHead
	timerHead = entry

	for entry.next != nil && entry.next.jiffies < entry.jiffies {
		entry.jiffies -= entry.next.jiffies
		entry.fn, entry.next.fn = entry.next.fn, entry.fn
		entry.jiffies, entry.next.jiffies = entry.next.jiffies, entry.jiffies
		entry = entry.next
	}
}

func DoTimer(cpl int32) {
	if cpl != 0 {
		include.Current.Utime++
	} else {
		include.Current.Stime++
	}

	timerMu.Lock()
	if timerHead != nil {
		timerHead.jiffies--
		for timerHead != nil && timerHead.jiffies <= 0 {
			fn := timerHead.fn
			timerHead.fn = nil
			timerHead = timerHead.next
			if fn != nil {
				timerMu.Unlock()
				fn()
				timerMu.Lock()
			}
		}
	}
	timerMu.Unlock()

	include.Current.Counter--
	if include.Current.Counter > 0 {
		return
	}
	include.Current.Counter = 0
	if cpl == 0 {
		return
	}
	Schedule()
}

func SysAlarm(seconds int32) int32 {
	old := include.Current.Alarm
	if old != 0 {
		old = (old - include.Jiffies) / include.HZ
	}
	if seconds > 0 {
		include.Current.Alarm = include.Jiffies + include.HZ*seconds
	} else {
		include.Current.Alarm = 0
	}
	return old
}

func SysGetpid() int32   { return include.Current.Pid }
func SysGetppid() int32  { return include.Current.Father }
func SysGetuid() uint16  { return include.Current.Uid }
func SysGeteuid() uint16 { return include.Current.Euid }
func SysGetgid() uint16  { return include.Current.Gid }
func SysGetegid() uint16 { return include.Current.Egid }

func SysNice(increment int32) int {
	if include.Current.Priority-increment > 0 {
		include.Current.Priority -= increment
	}
	return 0
}

func SchedInit() {
	initTask := &include.TaskStruct{
		State:    include.TASK_RUNNING,
		Counter:  15,
		Priority: 15,
		Pid:      0,
		Father:   -1,
		Leader:   1,
		Tty:      -1,
		Umask:    0022,
	}

	for i := 1; i < include.NR_TASKS; i++ {
		include.Task[i] = nil
	}
	include.Task[0] = initTask
	include.Current = initTask

	include.Jiffies = 0
	include.StartupTime = int32(time.Now().Unix())

	go timerTicker()

	log.Printf("kernel: sched_init done, idle priority=%d bound=%d",
		initTask.Priority, 2*initTask.Priority)
}

func timerTicker() {
	ticker := time.NewTicker(time.Second / include.HZ)
	defer ticker.Stop()
	for range ticker.C {
		schedMu.Lock()
		include.Jiffies++
		DoTimer(1)
		schedMu.Unlock()
	}
}
