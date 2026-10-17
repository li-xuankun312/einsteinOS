package init011

import (
	"log"
	"time"

	"google.golang.org/adk/v2/blk_drv"
	"google.golang.org/adk/v2/chr_drv"
	"google.golang.org/adk/v2/fs"
	. "google.golang.org/adk/v2/include"
	"google.golang.org/adk/v2/kernel"
	"google.golang.org/adk/v2/mm"
)

var (
	memoryEnd       int32 = 16 * 1024 * 1024
	bufferMemoryEnd int32 = 4 * 1024 * 1024
	mainMemoryStart int32 = 4 * 1024 * 1024
	startupTS       int64
	rdStart         int32
)

func driveInfo() (cylinders, heads, sectors int32) {
	return 306, 4, 17
}

func timeInit() {
	startupTS = time.Now().Unix()
	kernel.SetStartupTime(startupTS)
}

func calcMemoryLayout() {
	extMem := int32(16 * 1024)
	memoryEnd = int32(1)*1024*1024 + extMem*1024
	if memoryEnd > 16*1024*1024 {
		memoryEnd = 16 * 1024 * 1024
	}
	if memoryEnd > 12*1024*1024 {
		bufferMemoryEnd = 4 * 1024 * 1024
	} else if memoryEnd > 6*1024*1024 {
		bufferMemoryEnd = 2 * 1024 * 1024
	} else {
		bufferMemoryEnd = 1 * 1024 * 1024
	}
	mainMemoryStart = bufferMemoryEnd
	if rdStart > 0 {
		mainMemoryStart += rdStart
	}
}

func KernelMain() {
	ROOT_DEV = 0x301

	calcMemoryLayout()

	mm.MemInit(uint32(mainMemoryStart), uint32(memoryEnd))
	kernel.TrapInit()
	blk_drv.BlkDevInit()
	chr_drv.ChrDevInit()
	chr_drv.TtyInit()
	chr_drv.KbInit()
	timeInit()
	kernel.SchedInit()
	fs.BufferInit(bufferMemoryEnd)
	blk_drv.HdInit()
	blk_drv.FloppyInit()

	WireAll()

	sti()

	go initProcess()

	for {
		kernel.Schedule()
		time.Sleep(10 * time.Millisecond)
	}
}

func sti() {
}

func initProcess() {
	blk_drv.SysSetup()

	log.Printf("init: %d buffers = %d bytes buffer space",
		fs.NrBuffers, fs.NrBuffers*BLOCK_SIZE)
	log.Printf("init: free mem = %d bytes", mm.FreeMemCount()*4096)

	fs.MountRoot()

	openConsoleTty()

	log.Printf("init: root filesystem mounted, system ready")
}

func openConsoleTty() {
	fd := fs.SysOpen("/dev/tty0", 2, 0)
	if fd < 0 {
		return
	}
	fs.SysDup(uint32(fd))
	fs.SysDup(uint32(fd))
}

func Shutdown() {
	fs.SysSync()
	log.Printf("init: system halted")
}

func SetRdStart(start int32) {
	rdStart = start
}

func GetBootTime() int64 {
	return startupTS
}

func GetMemoryLayout() (end, bufEnd, mainStart int32) {
	return memoryEnd, bufferMemoryEnd, mainMemoryStart
}
