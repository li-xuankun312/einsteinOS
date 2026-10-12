
package kernel

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	. "google.golang.org/adk/v2/include"
)
func _S(nr int) int32 { return 1 << (nr - 1) }

var _BLOCKABLE int32 = ^(_S(SIGKILL) | _S(SIGSTOP))
func ShowTask(nr int, p *TaskStruct) {
	names := [5]string{"RUNNING", "INTERRUPTIBLE", "UNINTERRUPTIBLE", "ZOMBIE", "STOPPED"}
	s := "?"
	if p.State >= 0 && p.State < 5 {
		s = names[p.State]
	}
	fmt.Fprintf(os.Stderr, "%d: pid=%d state=%s father=%d counter=%d prio=%d\n",
		nr, p.Pid, s, p.Father, p.Counter, p.Priority)
}

func ShowStat() {
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil {
			ShowTask(i, Task[i])
		}
	}
}
var schedMu sync.Mutex

func switchTo(n int) {
	if Task[n] == Current {
		return
	}
	Current = Task[n]
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
			p.Signal |= _S(SIGALRM)
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
		tmp.State = TASK_RUNNING
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
		(*p).State = TASK_RUNNING
		goto repeat
	}
	*p = nil
	if tmp != nil {
		tmp.State = TASK_RUNNING
	}
}

func WakeUp(p **TaskStruct) {
	if p != nil && *p != nil {
		(*p).State = TASK_RUNNING
		*p = nil
	}
}
const TIME_REQUESTS = 64

type timerListEntry struct {
	jiffies int32 // differential delay from predecessor
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
func SysGetpid() int32   { return Current.Pid }
func SysGetppid() int32  { return Current.Father }
func SysGetuid() uint16  { return Current.Uid }
func SysGeteuid() uint16 { return Current.Euid }
func SysGetgid() uint16  { return Current.Gid }
func SysGetegid() uint16 { return Current.Egid }

func SysNice(increment int32) int {
	if Current.Priority-increment > 0 {
		Current.Priority -= increment
	}
	return 0
}
func SchedInit() {
	initTask := &TaskStruct{}
	initTask.State = TASK_RUNNING
	initTask.Counter = 15
	initTask.Priority = 15
	initTask.Pid = 0
	initTask.Father = -1
	initTask.Leader = 1
	initTask.Tty = -1
	initTask.Umask = 0022

	for i := 1; i < NR_TASKS; i++ {
		Task[i] = nil
	}
	Task[0] = initTask
	Current = initTask

	Jiffies = 0
	StartupTime = int32(time.Now().Unix())

	go timerTicker()

	log.Printf("kernel: sched_init done, idle priority=%d bound=%d",
		initTask.Priority, 2*initTask.Priority)
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
