package kernel

import (
	"time"

	. "google.golang.org/adk/v2/include"
)

func SysFtime() int32   { return -ENOSYS }
func SysBreak() int32   { return -ENOSYS }
func SysPtrace() int32  { return -ENOSYS }
func SysStty() int32    { return -ENOSYS }
func SysGtty() int32    { return -ENOSYS }
func SysRename() int32  { return -ENOSYS }
func SysProf() int32    { return -ENOSYS }
func SysAcct() int32    { return -ENOSYS }
func SysPhys() int32    { return -ENOSYS }
func SysLock() int32    { return -ENOSYS }
func SysMpx() int32     { return -ENOSYS }
func SysUlimit() int32  { return -ENOSYS }

func SysSetregid(rgid, egid int32) int32 {
	if rgid > 0 {
		if int32(Current.Gid) == rgid || suser() {
			Current.Gid = uint16(rgid)
		} else {
			return -EPERM
		}
	}
	if egid > 0 {
		if int32(Current.Gid) == egid ||
			int32(Current.Egid) == egid ||
			int32(Current.Sgid) == egid ||
			suser() {
			Current.Egid = uint16(egid)
		} else {
			return -EPERM
		}
	}
	return 0
}

func SysSetgid(gid int32) int32 {
	return SysSetregid(gid, gid)
}

func SysTime(tloc *int32) int32 {
	i := CURRENT_TIME()
	if tloc != nil {
		*tloc = i
	}
	return i
}

func SysSetreuid(ruid, euid int32) int32 {
	oldRuid := int32(Current.Uid)

	if ruid > 0 {
		if int32(Current.Euid) == ruid ||
			oldRuid == ruid ||
			suser() {
			Current.Uid = uint16(ruid)
		} else {
			return -EPERM
		}
	}
	if euid > 0 {
		if oldRuid == euid ||
			int32(Current.Euid) == euid ||
			suser() {
			Current.Euid = uint16(euid)
		} else {
			Current.Uid = uint16(oldRuid)
			return -EPERM
		}
	}
	return 0
}

func SysSetuid(uid int32) int32 {
	return SysSetreuid(uid, uid)
}

func SysStime(tptr *int32) int32 {
	if !suser() {
		return -EPERM
	}
	if tptr != nil {
		StartupTime = *tptr - Jiffies/HZ
	}
	return 0
}

type Tms struct {
	TmsUtime  int32
	TmsStime  int32
	TmsCutime int32
	TmsCstime int32
}

func SysTimes(tbuf *Tms) int32 {
	if tbuf != nil {
		tbuf.TmsUtime = Current.Utime
		tbuf.TmsStime = Current.Stime
		tbuf.TmsCutime = Current.Cutime
		tbuf.TmsCstime = Current.Cstime
	}
	return Jiffies
}

func SysBrk(endDataSeg uint32) uint32 {
	if endDataSeg >= Current.EndCode &&
		endDataSeg < Current.StartStack-16384 {
		Current.Brk = endDataSeg
	}
	return Current.Brk
}

func SysSetpgid(pid, pgid int32) int32 {
	if pid == 0 {
		pid = Current.Pid
	}
	if pgid == 0 {
		pgid = Current.Pid
	}
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil && Task[i].Pid == pid {
			if Task[i].Leader != 0 {
				return -EPERM
			}
			if Task[i].Session != Current.Session {
				return -EPERM
			}
			Task[i].Pgrp = pgid
			return 0
		}
	}
	return -ESRCH
}

func SysGetpgrp() int32 {
	return Current.Pgrp
}

func SysSetsid() int32 {
	if Current.Leader != 0 && !suser() {
		return -EPERM
	}
	Current.Leader = 1
	Current.Session = Current.Pid
	Current.Pgrp = Current.Pid
	Current.Tty = -1
	return Current.Pgrp
}

type Utsname struct {
	Sysname  [65]byte
	Nodename [65]byte
	Release  [65]byte
	Version  [65]byte
	Machine  [65]byte
}

func SysUname(name *Utsname) int32 {
	if name == nil {
		return -EINVAL
	}
	copy(name.Sysname[:], "linux .0")
	copy(name.Nodename[:], "nodename")
	copy(name.Release[:], "release ")
	copy(name.Version[:], "version ")
	copy(name.Machine[:], "machine ")
	return 0
}

func SysUmask(mask uint16) uint16 {
	old := Current.Umask
	Current.Umask = mask & 0777
	return old
}

func UnixTime() int32 {
	return int32(time.Now().Unix())
}
