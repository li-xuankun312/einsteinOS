package lib

var Errno int

var (
	SysExitFn   func(int)
	SysCloseFn  func(int) int
	SysDupFn    func(uint32) int
	SysOpenFn   func(string, int, int) int
	SysWriteFn  func(int, []byte, int) int
	SysSetsidFn func() int
	SysWaitpidFn func(int, *int, int) int
	SysExecveFn func(string, []string, []string) int
)

func Exit(exitCode int) {
	if SysExitFn != nil { SysExitFn(exitCode) }
}

func Close(fd int) int {
	if SysCloseFn != nil { return SysCloseFn(fd) }
	return -1
}

func Dup(fd uint32) int {
	if SysDupFn != nil { return SysDupFn(fd) }
	return -1
}

func Open(filename string, flag, mode int) int {
	if SysOpenFn != nil { return SysOpenFn(filename, flag, mode) }
	return -1
}

func Write(fd int, buf []byte, count int) int {
	if SysWriteFn != nil { return SysWriteFn(fd, buf, count) }
	return -1
}

func Setsid() int {
	if SysSetsidFn != nil { return SysSetsidFn() }
	return -1
}

func Waitpid(pid int, stat *int, options int) int {
	if SysWaitpidFn != nil { return SysWaitpidFn(pid, stat, options) }
	return -1
}

func Execve(file string, argv, envp []string) int {
	if SysExecveFn != nil { return SysExecveFn(file, argv, envp) }
	return -1
}
