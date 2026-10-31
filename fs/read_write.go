package fs

import (
	"log"

	. "google.golang.org/adk/v2/include"
)

const (
	EINVAL_FS = 22
)

func S_ISCHR(mode uint16) bool { return (mode & 0xF000) == 0x2000 }

var (
	rwCharFn    func(rw int, dev uint16, buf []byte, count int, pos *int64) int
	readPipeFn  func(inode *MInode, buf []byte, count int) int
	writePipeFn func(inode *MInode, buf []byte, count int) int
	blockReadFn func(dev int, pos *int64, buf []byte, count int) int
	blockWriteFn func(dev int, pos *int64, buf []byte, count int) int
	fileReadFn  func(inode *MInode, filp *File, buf []byte, count int) int
	fileWriteFn func(inode *MInode, filp *File, buf []byte, count int) int
	verifyAreaFn func(uint32, int32)
)

func SetRwChar(fn func(int, uint16, []byte, int, *int64) int)        { rwCharFn = fn }
func SetReadPipe(fn func(*MInode, []byte, int) int)                   { readPipeFn = fn }
func SetWritePipe(fn func(*MInode, []byte, int) int)                  { writePipeFn = fn }
func SetBlockRead(fn func(int, *int64, []byte, int) int)              { blockReadFn = fn }
func SetBlockWrite(fn func(int, *int64, []byte, int) int)             { blockWriteFn = fn }
func SetFileRead(fn func(*MInode, *File, []byte, int) int)            { fileReadFn = fn }
func SetFileWrite(fn func(*MInode, *File, []byte, int) int)           { fileWriteFn = fn }
func SetVerifyArea(fn func(uint32, int32))                             { verifyAreaFn = fn }

func SysLseek(fd uint32, offset int64, origin int) int64 {
	if fd >= NR_OPEN || Current.Filp[fd] == nil { return -EBADF }
	file := Current.Filp[fd]
	if file.FInode == nil { return -EBADF }
	if !IS_SEEKABLE(int(MAJOR(uint32(file.FInode.IDev)))) { return -EBADF }
	if file.FInode.IPipe != 0 { return -ESPIPE }
	switch origin {
	case 0:
		if offset < 0 { return -int64(EINVAL_FS) }
		file.FPos = offset
	case 1:
		if file.FPos+offset < 0 { return -int64(EINVAL_FS) }
		file.FPos += offset
	case 2:
		tmp := int64(file.FInode.ISize) + offset
		if tmp < 0 { return -int64(EINVAL_FS) }
		file.FPos = tmp
	default:
		return -int64(EINVAL_FS)
	}
	return file.FPos
}

func SysRead(fd uint32, buf []byte, count int) int {
	if fd >= NR_OPEN || count < 0 || Current.Filp[fd] == nil {
		return -EINVAL_FS
	}
	file := Current.Filp[fd]
	if count == 0 { return 0 }
	inode := file.FInode
	if inode == nil { return -EINVAL_FS }
	if inode.IPipe != 0 {
		if file.FMode&1 != 0 && readPipeFn != nil {
			return readPipeFn(inode, buf, count)
		}
		return -EIO
	}
	if S_ISCHR(inode.IMode) && rwCharFn != nil {
		return rwCharFn(READ, inode.IZone[0], buf, count, &file.FPos)
	}
	if S_ISBLK(inode.IMode) && blockReadFn != nil {
		return blockReadFn(int(inode.IZone[0]), &file.FPos, buf, count)
	}
	if S_ISDIR(inode.IMode) || S_ISREG(inode.IMode) {
		if int64(count)+file.FPos > int64(inode.ISize) {
			count = int(int64(inode.ISize) - file.FPos)
		}
		if count <= 0 { return 0 }
		if fileReadFn != nil { return fileReadFn(inode, file, buf, count) }
	}
	log.Printf("fs: (Read) inode.i_mode=%06o", inode.IMode)
	return -EINVAL_FS
}

func SysWrite(fd uint32, buf []byte, count int) int {
	if fd >= NR_OPEN || count < 0 || Current.Filp[fd] == nil {
		return -EINVAL_FS
	}
	file := Current.Filp[fd]
	if count == 0 { return 0 }
	inode := file.FInode
	if inode == nil { return -EINVAL_FS }
	if inode.IPipe != 0 {
		if file.FMode&2 != 0 && writePipeFn != nil {
			return writePipeFn(inode, buf, count)
		}
		return -EIO
	}
	if S_ISCHR(inode.IMode) && rwCharFn != nil {
		return rwCharFn(WRITE, inode.IZone[0], buf, count, &file.FPos)
	}
	if S_ISBLK(inode.IMode) && blockWriteFn != nil {
		return blockWriteFn(int(inode.IZone[0]), &file.FPos, buf, count)
	}
	if S_ISREG(inode.IMode) {
		if fileWriteFn != nil { return fileWriteFn(inode, file, buf, count) }
	}
	log.Printf("fs: (Write) inode.i_mode=%06o", inode.IMode)
	return -EINVAL_FS
}
