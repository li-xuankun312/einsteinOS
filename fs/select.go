package fs

import (
	. "google.golang.org/adk/v2/include"
)

const FD_SETSIZE = 32

type FdSet struct {
	Bits [FD_SETSIZE / 32]uint32
}

func fdIsset(fd int, set *FdSet) bool {
	if set == nil || fd < 0 || fd >= FD_SETSIZE {
		return false
	}
	return set.Bits[fd/32]&(1<<uint(fd%32)) != 0
}

func fdSet(fd int, set *FdSet) {
	if set == nil || fd < 0 || fd >= FD_SETSIZE {
		return
	}
	set.Bits[fd/32] |= 1 << uint(fd%32)
}

func fdClr(fd int, set *FdSet) {
	if set == nil || fd < 0 || fd >= FD_SETSIZE {
		return
	}
	set.Bits[fd/32] &^= 1 << uint(fd%32)
}

func fdZero(set *FdSet) {
	if set == nil {
		return
	}
	for i := range set.Bits {
		set.Bits[i] = 0
	}
}

func S_ISPIPE(mode uint16) bool { return (mode & 0xF000) == 0x1000 }
func S_ISLNK(mode uint16) bool  { return (mode & 0xF000) == 0xA000 }
func S_ISSOCK(mode uint16) bool { return (mode & 0xF000) == 0xC000 }

func checkIn(waitTable **TaskStruct, inode *MInode) int {
	if S_ISPIPE(inode.IMode) {
		if inode.ISize != 0 {
			return 1
		}
		if inode.ICount < 2 {
			return 1
		}
		return 0
	}
	if S_ISCHR(inode.IMode) || S_ISBLK(inode.IMode) {
		return 1
	}
	if S_ISREG(inode.IMode) {
		return 1
	}
	return 0
}

func checkOut(waitTable **TaskStruct, inode *MInode) int {
	if S_ISPIPE(inode.IMode) {
		if PIPE_EMPTY(inode) || PIPE_SIZE(inode) < PAGE_SIZE-1 {
			return 1
		}
		if inode.ICount < 2 {
			return 1
		}
		return 0
	}
	return 1
}

func checkEx(waitTable **TaskStruct, inode *MInode) int {
	if S_ISPIPE(inode.IMode) {
		if inode.ICount < 2 {
			return 1
		}
		return 0
	}
	return 0
}

func SysSelect(nfds int, readfds, writefds, exceptfds *FdSet, timeout int32) int {
	if nfds < 0 || nfds > NR_OPEN {
		return -EINVAL_FS
	}

	remaining := timeout
	count := 0

repeat:
	var waitTable *TaskStruct
	count = 0

	for fd := 0; fd < nfds; fd++ {
		if readfds != nil && fdIsset(fd, readfds) {
			if Current.Filp[fd] == nil || Current.Filp[fd].FInode == nil {
				return -EBADF
			}
			if checkIn(&waitTable, Current.Filp[fd].FInode) != 0 {
				count++
			} else {
				fdClr(fd, readfds)
			}
		}
		if writefds != nil && fdIsset(fd, writefds) {
			if Current.Filp[fd] == nil || Current.Filp[fd].FInode == nil {
				return -EBADF
			}
			if checkOut(&waitTable, Current.Filp[fd].FInode) != 0 {
				count++
			} else {
				fdClr(fd, writefds)
			}
		}
		if exceptfds != nil && fdIsset(fd, exceptfds) {
			if Current.Filp[fd] == nil || Current.Filp[fd].FInode == nil {
				return -EBADF
			}
			if checkEx(&waitTable, Current.Filp[fd].FInode) != 0 {
				count++
			} else {
				fdClr(fd, exceptfds)
			}
		}
	}

	if count > 0 {
		return count
	}

	if timeout > 0 {
		remaining--
		if remaining <= 0 {
			return 0
		}
	} else if timeout == 0 {
		return 0
	}

	Current.State = TASK_INTERRUPTIBLE
	scheduleSelect()

	if (Current.Signal & ^Current.Blocked) != 0 {
		return -EINTR
	}
	goto repeat
}

var scheduleFn func()

func SetScheduleForSelect(fn func()) { scheduleFn = fn }

func scheduleSelect() {
	if scheduleFn != nil {
		scheduleFn()
	}
}
