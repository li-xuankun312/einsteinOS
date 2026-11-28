package fs

import (
	. "google.golang.org/adk/v2/include"
)

const (
	F_DUPFD  = 0
	F_GETFD  = 1
	F_SETFD  = 2
	F_GETFL  = 3
	F_SETFL  = 4
	F_GETLK  = 5
	F_SETLK  = 6
	F_SETLKW = 7
	O_NONBLOCK = 04000
)

func dupfd(fd, arg uint32) int {
	if fd >= NR_OPEN || Current.Filp[fd] == nil { return -EBADF }
	if arg >= NR_OPEN { return -EINVAL_FS }
	for arg < NR_OPEN {
		if Current.Filp[arg] == nil { break }
		arg++
	}
	if arg >= NR_OPEN { return -EMFILE }
	Current.CloseOnExec &^= 1 << arg
	Current.Filp[arg] = Current.Filp[fd]
	Current.Filp[arg].FCount++
	return int(arg)
}

func SysDup2(oldfd, newfd uint32) int {
	SysCloseFS(int(newfd))
	return dupfd(oldfd, newfd)
}

func SysDup(fildes uint32) int {
	return dupfd(fildes, 0)
}

func SysFcntl(fd, cmd uint32, arg uint32) int {
	if fd >= NR_OPEN || Current.Filp[fd] == nil { return -EBADF }
	filp := Current.Filp[fd]
	switch cmd {
	case F_DUPFD:
		return dupfd(fd, arg)
	case F_GETFD:
		return int((Current.CloseOnExec >> fd) & 1)
	case F_SETFD:
		if arg&1 != 0 {
			Current.CloseOnExec |= 1 << fd
		} else {
			Current.CloseOnExec &^= 1 << fd
		}
		return 0
	case F_GETFL:
		return int(filp.FFlags)
	case F_SETFL:
		filp.FFlags &^= uint16(O_APPEND | O_NONBLOCK)
		filp.FFlags |= uint16(arg) & uint16(O_APPEND|O_NONBLOCK)
		return 0
	case F_GETLK, F_SETLK, F_SETLKW:
		return -1
	default:
		return -1
	}
}
