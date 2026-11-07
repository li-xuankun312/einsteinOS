package fs

import (
	. "google.golang.org/adk/v2/include"
)


type ioctlPtr func(dev int, cmd int, arg int) int

var ttyIoctlFn func(int, int, int) int

func SetTtyIoctl(fn func(int, int, int) int) { ttyIoctlFn = fn }

var ioctlTable = [...]ioctlPtr{
	nil,
	nil,
	nil,
	nil,
	nil,
	nil,
	nil,
	nil,
}

func init() {
	wrapper := func(dev, cmd, arg int) int {
		if ttyIoctlFn != nil { return ttyIoctlFn(dev, cmd, arg) }
		return -ENOTTY
	}
	ioctlTable[4] = wrapper
	ioctlTable[5] = wrapper
}

func SysIoctl(fd uint32, cmd uint32, arg uint32) int {
	if fd >= NR_OPEN || Current.Filp[fd] == nil { return -EBADF }
	filp := Current.Filp[fd]
	if filp.FInode == nil { return -EBADF }
	mode := filp.FInode.IMode
	if !S_ISCHR(mode) && !S_ISBLK(mode) { return -EINVAL_FS }
	dev := int(filp.FInode.IZone[0])
	major := int(MAJOR(uint32(dev)))
	if major >= len(ioctlTable) { return -ENODEV }
	if ioctlTable[major] == nil { return -ENOTTY }
	return ioctlTable[major](dev, int(cmd), int(arg))
}
