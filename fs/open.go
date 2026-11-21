package fs

import (
	"log"

	. "google.golang.org/adk/v2/include"
)

const (
	ENOSYS_FS = 38
	O_CREAT   = 0100
	O_TRUNC   = 01000
)

var (
	openNameiFn func(pathname string, flag, mode int, resInode **MInode) int
	suserFn     func() bool
)

func SetOpenNamei(fn func(string, int, int, **MInode) int) { openNameiFn = fn }
func SetSuser(fn func() bool)                              { suserFn = fn }

func suser() bool {
	if suserFn != nil { return suserFn() }
	return Current.Euid == 0
}

func SysUstat() int { return -ENOSYS_FS }

func SysUtime(filename string, actime, modtime int32) int {
	var inode *MInode
	if nameiFn != nil { inode = nameiFn(filename) }
	if inode == nil { return -ENOENT }
	if actime == 0 && modtime == 0 {
		ct := CURRENT_TIME()
		actime = ct
		modtime = ct
	}
	inode.IAtime = uint32(actime)
	inode.IMtime = uint32(modtime)
	inode.IDirt = 1
	Iput(inode)
	return 0
}

func SysAccess(filename string, mode int) int {
	mode &= 0007
	var inode *MInode
	if nameiFn != nil { inode = nameiFn(filename) }
	if inode == nil { return -EACCES }
	iMode := int(inode.IMode & 0777)
	res := iMode
	Iput(inode)
	if Current.Uid == inode.IUid { res >>= 6 } else if uint8(Current.Gid) == inode.IGid { res >>= 3 }
	if (res & 0007 & mode) == mode { return 0 }
	if Current.Uid == 0 && (mode&1 == 0 || iMode&0111 != 0) { return 0 }
	return -EACCES
}

func SysChdir(filename string) int {
	var inode *MInode
	if nameiFn != nil { inode = nameiFn(filename) }
	if inode == nil { return -ENOENT }
	if !S_ISDIR(inode.IMode) { Iput(inode); return -ENOTDIR }
	Iput(Current.Pwd)
	Current.Pwd = inode
	return 0
}

func SysChroot(filename string) int {
	var inode *MInode
	if nameiFn != nil { inode = nameiFn(filename) }
	if inode == nil { return -ENOENT }
	if !S_ISDIR(inode.IMode) { Iput(inode); return -ENOTDIR }
	Iput(Current.Root)
	Current.Root = inode
	return 0
}

func SysChmod(filename string, mode int) int {
	var inode *MInode
	if nameiFn != nil { inode = nameiFn(filename) }
	if inode == nil { return -ENOENT }
	if Current.Euid != inode.IUid && !suser() {
		Iput(inode); return -EACCES
	}
	inode.IMode = uint16(mode&07777) | (inode.IMode &^ 07777)
	inode.IDirt = 1
	Iput(inode)
	return 0
}

func SysChown(filename string, uid, gid int) int {
	var inode *MInode
	if nameiFn != nil { inode = nameiFn(filename) }
	if inode == nil { return -ENOENT }
	if !suser() { Iput(inode); return -EACCES }
	inode.IUid = uint16(uid)
	inode.IGid = uint8(gid)
	inode.IDirt = 1
	Iput(inode)
	return 0
}

func SysOpen(filename string, flag, mode int) int {
	mode &= 0777 & ^int(Current.Umask)
	fd := -1
	for i := 0; i < NR_OPEN; i++ {
		if Current.Filp[i] == nil { fd = i; break }
	}
	if fd < 0 { return -EINVAL_FS }
	Current.CloseOnExec &^= uint32(1 << fd)
	var f *File
	for i := 0; i < NR_FILE; i++ {
		if FileTable[i].FCount == 0 { f = &FileTable[i]; break }
	}
	if f == nil { return -EINVAL_FS }
	Current.Filp[fd] = f
	f.FCount++
	var inode *MInode
	if openNameiFn != nil {
		ret := openNameiFn(filename, flag, mode, &inode)
		if ret < 0 {
			Current.Filp[fd] = nil
			f.FCount = 0
			return ret
		}
	}
	if inode != nil && S_ISCHR(inode.IMode) {
		if MAJOR(uint32(inode.IZone[0])) == 4 {
			if Current.Leader != 0 && Current.Tty < 0 {
				Current.Tty = int32(MINOR(uint32(inode.IZone[0])))
			}
		} else if MAJOR(uint32(inode.IZone[0])) == 5 {
			if Current.Tty < 0 {
				Iput(inode)
				Current.Filp[fd] = nil
				f.FCount = 0
				return -EPERM
			}
		}
	}
	if inode != nil && S_ISBLK(inode.IMode) {
		CheckDiskChange(int(inode.IZone[0]))
	}
	if inode != nil {
		f.FMode = inode.IMode
	}
	f.FFlags = uint16(flag)
	f.FCount = 1
	f.FInode = inode
	f.FPos = 0
	return fd
}

func SysCreat(pathname string, mode int) int {
	return SysOpen(pathname, O_CREAT|O_TRUNC, mode)
}

func SysCloseFS(fd int) int {
	if fd >= NR_OPEN { return -EINVAL_FS }
	Current.CloseOnExec &^= uint32(1 << fd)
	filp := Current.Filp[fd]
	if filp == nil { return -EINVAL_FS }
	Current.Filp[fd] = nil
	if filp.FCount == 0 {
		log.Printf("fs: Close: file count is 0")
		return -1
	}
	filp.FCount--
	if filp.FCount != 0 { return 0 }
	Iput(filp.FInode)
	return 0
}
