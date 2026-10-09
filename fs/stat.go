package fs

import (
	. "google.golang.org/adk/v2/include"
)

type Stat struct {
	StDev   uint16
	StIno   uint16
	StMode  uint16
	StNlink uint8
	StUid   uint16
	StGid   uint8
	StRdev  uint16
	StSize  uint32
	StAtime uint32
	StMtime uint32
	StCtime uint32
}

func cpStat(inode *MInode) Stat {
	return Stat{
		StDev:   inode.IDev,
		StIno:   inode.INum,
		StMode:  inode.IMode,
		StNlink: inode.INlinks,
		StUid:   inode.IUid,
		StGid:   inode.IGid,
		StRdev:  inode.IZone[0],
		StSize:  inode.ISize,
		StAtime: inode.IAtime,
		StMtime: inode.IMtime,
		StCtime: inode.ICtime,
	}
}

func SysStat(filename string) (Stat, int) {
	var inode *MInode
	if nameiFn != nil { inode = nameiFn(filename) }
	if inode == nil { return Stat{}, -ENOENT }
	st := cpStat(inode)
	Iput(inode)
	return st, 0
}

func SysFstat(fd uint32) (Stat, int) {
	if fd >= NR_OPEN || Current.Filp[fd] == nil { return Stat{}, -EBADF }
	f := Current.Filp[fd]
	inode := f.FInode
	if inode == nil { return Stat{}, -EBADF }
	return cpStat(inode), 0
}
