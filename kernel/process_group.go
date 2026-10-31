package kernel

import (
	. "google.golang.org/adk/v2/include"
)

func IsOrphanedPgrp(pgrp int32) bool {
	for i := 0; i < NR_TASKS; i++ {
		p := Task[i]
		if p == nil || p.Pgrp != pgrp {
			continue
		}
		if p.State == TASK_ZOMBIE {
			continue
		}
		father := p.Father
		for j := 0; j < NR_TASKS; j++ {
			if Task[j] != nil && Task[j].Pid == father {
				if Task[j].Pgrp != pgrp && Task[j].Session == p.Session {
					return false
				}
				break
			}
		}
	}
	return true
}

func HasStoppedJobs(pgrp int32) bool {
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil && Task[i].Pgrp == pgrp && Task[i].State == TASK_STOPPED {
			return true
		}
	}
	return false
}

func SysSetpgidFull(pid, pgid int32) int32 {
	if pid == 0 {
		pid = Current.Pid
	}
	if pgid == 0 {
		pgid = pid
	}

	for i := 0; i < NR_TASKS; i++ {
		p := Task[i]
		if p == nil || p.Pid != pid {
			continue
		}
		if p.Leader != 0 {
			return -EPERM
		}
		if p.Session != Current.Session {
			return -EPERM
		}
		if pid != Current.Pid {
			found := false
			for j := 0; j < NR_TASKS; j++ {
				if Task[j] != nil && Task[j].Pid == pid && Task[j].Father == Current.Pid {
					found = true
					break
				}
			}
			if !found {
				return -ESRCH
			}
		}
		if pgid != pid {
			exists := false
			for j := 0; j < NR_TASKS; j++ {
				if Task[j] != nil && Task[j].Pgrp == pgid && Task[j].Session == Current.Session {
					exists = true
					break
				}
			}
			if !exists {
				return -EPERM
			}
		}
		p.Pgrp = pgid
		return 0
	}
	return -ESRCH
}

func SysGetpgidOf(pid int32) int32 {
	if pid == 0 {
		return Current.Pgrp
	}
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil && Task[i].Pid == pid {
			return Task[i].Pgrp
		}
	}
	return -ESRCH
}

func SysGetsid(pid int32) int32 {
	if pid == 0 {
		return Current.Session
	}
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil && Task[i].Pid == pid {
			return Task[i].Session
		}
	}
	return -ESRCH
}

func CountProcessesInGroup(pgrp int32) int {
	count := 0
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil && Task[i].Pgrp == pgrp && Task[i].State != TASK_ZOMBIE {
			count++
		}
	}
	return count
}

func SendGroupSig(sig int32, pgrp int32) {
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil && Task[i].Pgrp == pgrp {
			SendSig(sig, Task[i], 1)
		}
	}
}

func GetTaskByPid(pid int32) *TaskStruct {
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil && Task[i].Pid == pid {
			return Task[i]
		}
	}
	return nil
}

func GetTaskSlot(p *TaskStruct) int {
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] == p {
			return i
		}
	}
	return -1
}

func CountChildren(pid int32) int {
	count := 0
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil && Task[i].Father == pid {
			count++
		}
	}
	return count
}

func CountZombies(pid int32) int {
	count := 0
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil && Task[i].Father == pid && Task[i].State == TASK_ZOMBIE {
			count++
		}
	}
	return count
}
