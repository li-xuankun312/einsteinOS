package kernel

import (
	"sync"
	"time"

	. "google.golang.org/adk/v2/include"
)

type ResourceLimits struct {
	Limits [RLIM_NLIMITS]Rlimit
}

var (
	resourceMu    sync.Mutex
	processLimits [NR_TASKS]ResourceLimits
)

func InitResourceLimits(slot int) {
	for i := 0; i < RLIM_NLIMITS; i++ {
		processLimits[slot].Limits[i] = Rlimit{
			RlimCur: RLIM_INFINITY,
			RlimMax: RLIM_INFINITY,
		}
	}
	processLimits[slot].Limits[RLIMIT_NOFILE] = Rlimit{
		RlimCur: int32(NR_OPEN),
		RlimMax: int32(NR_OPEN),
	}
	processLimits[slot].Limits[RLIMIT_NPROC] = Rlimit{
		RlimCur: int32(NR_TASKS - 1),
		RlimMax: int32(NR_TASKS - 1),
	}
	processLimits[slot].Limits[RLIMIT_STACK] = Rlimit{
		RlimCur: 8 * 1024 * 1024,
		RlimMax: RLIM_INFINITY,
	}
}

func SysGetRlimit(resource int, rlim *Rlimit) int {
	if resource < 0 || resource >= RLIM_NLIMITS {
		return -EINVAL
	}
	if rlim == nil {
		return -EFAULT
	}
	resourceMu.Lock()
	defer resourceMu.Unlock()
	slot := GetTaskSlot(Current)
	if slot < 0 {
		return -ESRCH
	}
	*rlim = processLimits[slot].Limits[resource]
	return 0
}

func SysSetRlimit(resource int, rlim *Rlimit) int {
	if resource < 0 || resource >= RLIM_NLIMITS {
		return -EINVAL
	}
	if rlim == nil {
		return -EFAULT
	}
	resourceMu.Lock()
	defer resourceMu.Unlock()
	slot := GetTaskSlot(Current)
	if slot < 0 {
		return -ESRCH
	}
	cur := &processLimits[slot].Limits[resource]
	if rlim.RlimMax > cur.RlimMax {
		if Current.Uid != 0 && Current.Euid != 0 {
			return -EPERM
		}
	}
	if rlim.RlimCur > rlim.RlimMax {
		return -EINVAL
	}
	*cur = *rlim
	return 0
}

func GetProcessRusage(who int, ru *Rusage) int {
	if ru == nil {
		return -EFAULT
	}
	switch who {
	case RUSAGE_SELF:
		ru.RuUtime = Timeval{TvSec: Current.Utime / int32(HZ),
			TvUsec: (Current.Utime % int32(HZ)) * 1000000 / int32(HZ)}
		ru.RuStime = Timeval{TvSec: Current.Stime / int32(HZ),
			TvUsec: (Current.Stime % int32(HZ)) * 1000000 / int32(HZ)}
	case RUSAGE_CHILDREN:
		ru.RuUtime = Timeval{TvSec: Current.Cutime / int32(HZ),
			TvUsec: (Current.Cutime % int32(HZ)) * 1000000 / int32(HZ)}
		ru.RuStime = Timeval{TvSec: Current.Cstime / int32(HZ),
			TvUsec: (Current.Cstime % int32(HZ)) * 1000000 / int32(HZ)}
	default:
		return -EINVAL
	}
	return 0
}

func SysGetPriority(which, who int) int {
	switch which {
	case PRIO_PROCESS:
		if who == 0 {
			return int(Current.Priority)
		}
		t := GetTaskByPid(int32(who))
		if t == nil {
			return -ESRCH
		}
		return int(t.Priority)
	case PRIO_PGRP:
		pgrp := int32(who)
		if pgrp == 0 {
			pgrp = Current.Pgrp
		}
		maxPrio := -1
		for i := 1; i < NR_TASKS; i++ {
			if Task[i] != nil && Task[i].Pgrp == pgrp {
				if int(Task[i].Priority) > maxPrio {
					maxPrio = int(Task[i].Priority)
				}
			}
		}
		if maxPrio < 0 {
			return -ESRCH
		}
		return maxPrio
	default:
		return -EINVAL
	}
}

func SysSetPriority(which, who, prio int) int {
	if prio < 0 {
		prio = 0
	}
	if prio > 40 {
		prio = 40
	}
	switch which {
	case PRIO_PROCESS:
		if who == 0 {
			Current.Priority = int32(prio)
			return 0
		}
		t := GetTaskByPid(int32(who))
		if t == nil {
			return -ESRCH
		}
		if Current.Uid != 0 && Current.Uid != t.Uid {
			return -EPERM
		}
		t.Priority = int32(prio)
		return 0
	default:
		return -EINVAL
	}
}

func SysGetTimeOfDay(tv *Timeval, tz *Timezone) int {
	if tv != nil {
		now := time.Now()
		tv.TvSec = int32(now.Unix())
		tv.TvUsec = int32(now.Nanosecond() / 1000)
	}
	if tz != nil {
		_, offset := time.Now().Zone()
		tz.TzMinuteswest = int32(-offset / 60)
		tz.TzDsttime = 0
	}
	return 0
}

func CopyResourceLimits(from, to int) {
	resourceMu.Lock()
	defer resourceMu.Unlock()
	if from >= 0 && from < NR_TASKS && to >= 0 && to < NR_TASKS {
		processLimits[to] = processLimits[from]
	}
}

func CheckResourceLimit(resource int, value int32) bool {
	resourceMu.Lock()
	defer resourceMu.Unlock()
	slot := GetTaskSlot(Current)
	if slot < 0 {
		return true
	}
	limit := processLimits[slot].Limits[resource].RlimCur
	if limit == RLIM_INFINITY {
		return true
	}
	return value <= limit
}
