package lib

import (
	"google.golang.org/adk/v2/fs"
	"google.golang.org/adk/v2/kernel"
)

func Read(fd int, buf []byte) int {
	return fs.SysRead(uint32(fd), buf, len(buf))
}

func Lseek(fd int, offset int64, whence int) int64 {
	return fs.SysLseek(uint32(fd), offset, whence)
}

func Creat(path string, mode int) int {
	return fs.SysCreat(path, mode)
}

func Dup2(oldfd, newfd int) int {
	return fs.SysDup2(uint32(oldfd), uint32(newfd))
}

func Pipe(fds []int) int {
	if len(fds) < 2 {
		return -1
	}
	return fs.SysPipe(fds)
}

func Stat(path string) (fs.Stat, int) {
	return fs.SysStat(path)
}

func Fstat(fd int) (fs.Stat, int) {
	return fs.SysFstat(uint32(fd))
}

func Chdir(path string) int {
	return fs.SysChdir(path)
}

func Chroot(path string) int {
	return fs.SysChroot(path)
}

func Chmod(path string, mode int) int {
	return fs.SysChmod(path, mode)
}

func Chown(path string, uid, gid int) int {
	return fs.SysChown(path, uid, gid)
}

func Mkdir(path string, mode int) int {
	return fs.SysMkdir(path, mode)
}

func Rmdir(path string) int {
	return fs.SysRmdir(path)
}

func Link(oldpath, newpath string) int {
	return fs.SysLink(oldpath, newpath)
}

func Unlink(path string) int {
	return fs.SysUnlink(path)
}

func Umask(mask uint16) uint16 {
	return kernel.SysUmask(mask)
}

func Fcntl(fd, cmd, arg int) int {
	return fs.SysFcntl(uint32(fd), uint32(cmd), uint32(arg))
}

func Access(path string, mode int) int {
	return fs.SysAccess(path, mode)
}

func Utime(path string, actime, modtime int32) int {
	return fs.SysUtime(path, actime, modtime)
}

func Sync() int {
	return fs.SysSync()
}
