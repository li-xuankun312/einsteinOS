package kernel

import (
	"runtime"
	"sync"
	"time"

	. "google.golang.org/adk/v2/include"
	"google.golang.org/adk/v2/mm"
)

type Sysinfo struct {
	Uptime    int64
	TotalRam  uint64
	FreeRam   uint64
	SharedRam uint64
	BufferRam uint64
	TotalSwap uint64
	FreeSwap  uint64
	Procs     uint16
	Loads     [3]uint64
}

var (
	bootTime   time.Time
	bootOnce   sync.Once
	hostname   string
	hostnameMu sync.Mutex
)

func ensureBoot() {
	bootOnce.Do(func() {
		bootTime = time.Now()
	})
}

func SysSethostname(name string) int {
	if Current.Uid != 0 && Current.Euid != 0 {
		return -EPERM
	}
	if len(name) > 64 {
		return -EINVAL
	}
	hostnameMu.Lock()
	defer hostnameMu.Unlock()
	hostname = name
	return 0
}

func SysGethostname() string {
	hostnameMu.Lock()
	defer hostnameMu.Unlock()
	if hostname == "" {
		return "localhost"
	}
	return hostname
}

func SysSysinfo(info *Sysinfo) int {
	ensureBoot()
	if info == nil {
		return -EFAULT
	}

	info.Uptime = int64(time.Since(bootTime).Seconds())

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	info.TotalRam = mem.Sys
	info.FreeRam = mem.Sys - mem.Alloc
	info.SharedRam = 0
	info.BufferRam = 0

	info.TotalSwap = uint64(mm.SWAP_PAGES) * 4096
	info.FreeSwap = uint64(mm.SwapFreeCount()) * 4096

	procs := uint16(0)
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil {
			procs++
		}
	}
	info.Procs = procs

	return 0
}

func BootTimeUnix() int64 {
	ensureBoot()
	return bootTime.Unix()
}

func UptimeSeconds() int64 {
	ensureBoot()
	return int64(time.Since(bootTime).Seconds())
}

func CountRunningTasks() int {
	count := 0
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil && Task[i].State == TASK_RUNNING {
			count++
		}
	}
	return count
}

func CountTotalTasks() int {
	count := 0
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil {
			count++
		}
	}
	return count
}

func TaskTableSnapshot() []struct {
	Slot    int
	Pid     int32
	State   int32
	Pgrp    int32
	Session int32
} {
	var snap []struct {
		Slot    int
		Pid     int32
		State   int32
		Pgrp    int32
		Session int32
	}
	for i := 0; i < NR_TASKS; i++ {
		t := Task[i]
		if t == nil {
			continue
		}
		snap = append(snap, struct {
			Slot    int
			Pid     int32
			State   int32
			Pgrp    int32
			Session int32
		}{
			Slot:    i,
			Pid:     t.Pid,
			State:   t.State,
			Pgrp:    t.Pgrp,
			Session: t.Session,
		})
	}
	return snap
}
