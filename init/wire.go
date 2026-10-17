package init011


import (
	"log"

	"google.golang.org/adk/v2/blk_drv"
	"google.golang.org/adk/v2/chr_drv"
	"google.golang.org/adk/v2/fs"
	. "google.golang.org/adk/v2/include"
	"google.golang.org/adk/v2/kernel"
	"google.golang.org/adk/v2/lib"
	"google.golang.org/adk/v2/mm"
)

func WireAll() {
	kernel.SetWriteVerify(mm.WriteVerify)
	kernel.SetCopyPageTables(mm.CopyPageTables)
	kernel.SetFreePageTables(mm.FreePageTables)
	mm.SetDoExit(func(code int32) { kernel.DoExit(code) })

	fs.SetSleepOn(kernel.SleepOn)
	fs.SetWakeUp(kernel.WakeUp)

	fs.SetLlRwBlock(blk_drv.LlRwBlock)
	blk_drv.SetSleepOn(kernel.SleepOn)
	blk_drv.SetWakeUp(kernel.WakeUp)
	blk_drv.SetBread(fs.Bread)
	blk_drv.SetBrelse(fs.Brelse)

	chr_drv.SetInterruptibleSleepOn(kernel.InterruptibleSleepOn)
	chr_drv.SetWakeUp(kernel.WakeUp)
	chr_drv.SetSchedule(kernel.Schedule)
	fs.SetTtyRead(chr_drv.TtyRead)
	fs.SetTtyWrite(chr_drv.TtyWrite)
	fs.SetTtyIoctl(chr_drv.TtyIoctl)

	fs.SetFileRead(fs.FileRead)
	fs.SetFileWrite(fs.FileWrite)
	fs.SetReadPipe(fs.ReadPipe)
	fs.SetWritePipe(fs.WritePipe)
	fs.SetBlockRead(fs.BlockRead)
	fs.SetBlockWrite(fs.BlockWrite)
	fs.SetOpenNamei(fs.OpenNamei)
	fs.SetNamei(fs.Namei)
	fs.SetRwChar(fs.RwChar)

	fs.SetFreePage(mm.FreePage)
	fs.SetGetFreePage(mm.GetFreePage)
	fs.SetFreePageTables(func(from, size uint32) {
		mm.FreePageTables(from, int32(size))
	})
	fs.SetPutPage(func(page, addr uint32) {
		mm.PutPage(page, addr)
	})

	kernel.SetIput(fs.Iput)
	fs.SetSysExit(func(code int) { kernel.DoExit(int32(code)) })
	fs.SetSysClose(func(fd int) int { return kernel.SysClose(int32(fd)) })
	fs.SetVerifyArea(kernel.VerifyArea)

	fs.SetSuser(func() bool { return Current.Euid == 0 })

	fs.SetPutSuper(fs.PutSuper)
	fs.SetInvalidateInodes(fs.InvalidateInodes)

	lib.SysExitFn = func(code int) { kernel.DoExit(int32(code)) }
	lib.SysCloseFn = func(fd int) int { return kernel.SysClose(int32(fd)) }
	lib.SysDupFn = func(fd uint32) int { return fs.SysDup(fd) }
	lib.SysOpenFn = func(name string, flag, mode int) int { return fs.SysOpen(name, flag, mode) }
	lib.SysWriteFn = func(fd int, buf []byte, count int) int { return fs.SysWrite(uint32(fd), buf, count) }
	lib.SysSetsidFn = func() int { return int(kernel.SysSetsid()) }
	lib.SysWaitpidFn = func(pid int, stat *int, options int) int {
		var s int32
		ret := kernel.SysWaitpid(int32(pid), &s, int32(options))
		if stat != nil {
			*stat = int(s)
		}
		return int(ret)
	}
	lib.SysExecveFn = func(file string, argv, envp []string) int {
		return fs.DoExecve(file, argv, envp)
	}

	log.Println("wire: all hooks connected")
}

func BootWithImage(imageData []byte) bool {
	log.Println("wire: booting with disk image")

	ROOT_DEV = 0x300

	mm.MemInit(4*1024*1024, 16*1024*1024)
	kernel.TrapInit()
	blk_drv.BlkDevInit()
	chr_drv.ChrDevInit()
	chr_drv.TtyInit()
	kernel.SetStartupTime(0)
	kernel.SchedInit()
	fs.BufferInit(4 * 1024 * 1024)

	blk_drv.SetDiskImage(0, imageData)
	blk_drv.HdInit()

	WireAll()

	blk_drv.SysSetup()
	fs.MountRoot()

	if Current.Root == nil {
		log.Println("wire: mount failed — Current.Root is nil")
		return false
	}
	log.Println("wire: boot complete, root mounted")
	return true
}
