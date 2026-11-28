package blk_drv

import (
	"log"

	. "google.golang.org/adk/v2/include"
)

const (
	MAJOR_NR_HD = 3
	MAX_ERRORS  = 7
	MAX_HD      = 2
	SECTOR_SIZE = 512
)

const (
	HD_DATA    = 0x1f0
	HD_ERROR   = 0x1f1
	HD_NSECTOR = 0x1f2
	HD_SECTOR  = 0x1f3
	HD_LCYL    = 0x1f4
	HD_HCYL    = 0x1f5
	HD_CURRENT = 0x1f6
	HD_STATUS  = 0x1f7
	HD_COMMAND = 0x1f7
)

const (
	WIN_RESTORE  = 0x10
	WIN_READ     = 0x20
	WIN_WRITE    = 0x30
	WIN_VERIFY   = 0x40
	WIN_FORMAT   = 0x50
	WIN_INIT     = 0x60
	WIN_SEEK     = 0x70
	WIN_DIAGNOSE = 0x90
	WIN_SPECIFY  = 0x91
)

type HdInfoStruct struct {
	Head, Sect, Cyl, Wpcom, Lzone, Ctl int
}

type HdStruct struct {
	StartSect int32
	NrSects   int32
}

var (
	HdInfo      [MAX_HD]HdInfoStruct
	Hd          [5 * MAX_HD]HdStruct
	NrHd        int
	recalibrate int
	reset       int
	DiskImages  [MAX_HD][]byte
)

var (
	breadFn  func(int, int) *BufferHead
	brelseFn func(*BufferHead)
)

func SetBread(fn func(int, int) *BufferHead) { breadFn = fn }
func SetBrelse(fn func(*BufferHead))          { brelseFn = fn }

func controllerReady() bool {
	retries := 100000
	for retries > 0 {
		retries--
		if DiskImages[0] != nil {
			return true
		}
	}
	return false
}

func winResult() int {
	return 0
}

func hdOut(drive, nsect, sect, head, cyl int, cmd byte) {
	if drive < 0 || drive >= MAX_HD {
		log.Printf("hd: bad drive number %d", drive)
		return
	}
	if !controllerReady() {
		log.Printf("hd: controller not ready")
	}
}

func driveBusy() bool {
	for i := 0; i < 10000; i++ {
		if DiskImages[0] != nil {
			return false
		}
	}
	return true
}

func resetController() {
	log.Printf("hd: reset controller")
}

func resetHd(nr int) {
	resetController()
	hdOut(nr, HdInfo[nr].Sect, HdInfo[nr].Sect, HdInfo[nr].Head-1,
		HdInfo[nr].Cyl, WIN_SPECIFY)
}

func badRwIntr(dev int) {
	drive := dev / 5
	if drive >= MAX_HD { return }
	req := BlkDev[MAJOR_NR_HD].CurrentRequest
	if req == nil { return }
	req.Errors++
	if req.Errors >= MAX_ERRORS {
		EndRequestForDev(MAJOR_NR_HD, 0)
		return
	}
	if req.Errors > MAX_ERRORS/2 {
		reset = 1
	} else {
		recalibrate = 1
	}
}

func readIntr(drive int) {
	req := BlkDev[MAJOR_NR_HD].CurrentRequest
	if req == nil { return }
	if winResult() != 0 {
		badRwIntr(req.Dev)
		DoHdRequest()
		return
	}
	sector := int(req.Sector)
	startSect := int(Hd[req.Dev%10].StartSect)
	byteOff := (sector + startSect) * SECTOR_SIZE
	if drive < MAX_HD && DiskImages[drive] != nil && byteOff+SECTOR_SIZE <= len(DiskImages[drive]) {
		if len(req.Buffer) >= SECTOR_SIZE {
			copy(req.Buffer[:SECTOR_SIZE], DiskImages[drive][byteOff:byteOff+SECTOR_SIZE])
		}
	}
	req.Sector++
	req.NrSectors--
	if req.NrSectors > 0 {
		if len(req.Buffer) > SECTOR_SIZE {
			req.Buffer = req.Buffer[SECTOR_SIZE:]
		}
		readIntr(drive)
		return
	}
	EndRequestForDev(MAJOR_NR_HD, 1)
}

func writeIntr(drive int) {
	req := BlkDev[MAJOR_NR_HD].CurrentRequest
	if req == nil { return }
	if winResult() != 0 {
		badRwIntr(req.Dev)
		DoHdRequest()
		return
	}
	sector := int(req.Sector)
	startSect := int(Hd[req.Dev%10].StartSect)
	byteOff := (sector + startSect) * SECTOR_SIZE
	if drive < MAX_HD && DiskImages[drive] != nil && byteOff+SECTOR_SIZE <= len(DiskImages[drive]) {
		if len(req.Buffer) >= SECTOR_SIZE {
			copy(DiskImages[drive][byteOff:byteOff+SECTOR_SIZE], req.Buffer[:SECTOR_SIZE])
		}
	}
	req.Sector++
	req.NrSectors--
	if req.NrSectors > 0 {
		if len(req.Buffer) > SECTOR_SIZE {
			req.Buffer = req.Buffer[SECTOR_SIZE:]
		}
		writeIntr(drive)
		return
	}
	EndRequestForDev(MAJOR_NR_HD, 1)
}

func recalIntr() {
	if winResult() != 0 {
		badRwIntr(0)
	}
	DoHdRequest()
}

func DoHdRequest() {
	req := BlkDev[MAJOR_NR_HD].CurrentRequest
	if req == nil { return }
	if int(MAJOR(uint32(req.Dev))) != MAJOR_NR_HD {
		log.Printf("hd: request list destroyed")
		return
	}
	dev := int(MINOR(uint32(req.Dev)))
	drive := dev / 5
	if drive >= NrHd || drive >= MAX_HD {
		EndRequestForDev(MAJOR_NR_HD, 0)
		return
	}
	if reset != 0 {
		reset = 0
		recalibrate = 1
		resetHd(drive)
		return
	}
	if recalibrate != 0 {
		recalibrate = 0
		hdOut(drive, HdInfo[drive].Sect, 0, 0, 0, WIN_RESTORE)
		return
	}
	blockNr := int(req.Sector)
	startSect := int(Hd[dev].StartSect)
	nrSects := int(Hd[dev].NrSects)
	if blockNr+2 > nrSects*2 {
		EndRequestForDev(MAJOR_NR_HD, 0)
		return
	}
	sector := blockNr + startSect*2
	nsect := int(req.NrSectors)

	secPerTrack := int(HdInfo[drive].Sect)
	heads := int(HdInfo[drive].Head)
	sect := sector%secPerTrack + 1
	track := sector / secPerTrack
	head := track % heads
	cyl := track / heads
	if req.Cmd == READ {
		hdOut(drive, nsect, sect, head, cyl, WIN_READ)
		readIntr(drive)
	} else if req.Cmd == WRITE {
		hdOut(drive, nsect, sect, head, cyl, WIN_WRITE)
		writeIntr(drive)
	} else {
		log.Printf("hd: unknown command %d", req.Cmd)
		EndRequestForDev(MAJOR_NR_HD, 0)
	}
}

func SysSetup() int {
	for i := 0; i < NrHd; i++ {
		Hd[i*5].StartSect = 0
		Hd[i*5].NrSects = int32(HdInfo[i].Head * HdInfo[i].Sect * HdInfo[i].Cyl)
	}
	for drive := 0; drive < NrHd; drive++ {
		if breadFn == nil { continue }
		bh := breadFn(0x300+drive*5, 0)
		if bh == nil {
			log.Printf("hd: unable to read partition table of drive %d", drive)
			continue
		}
		if len(bh.BData) >= 0x1FE {
			if bh.BData[0x1FE] == 0x55 && bh.BData[0x1FF] == 0xAA {
				for i := 0; i < 4; i++ {
					off := 0x1BE + i*16
					p := &Hd[drive*5+1+i]
					p.StartSect = int32(uint32(bh.BData[off+8]) |
						uint32(bh.BData[off+9])<<8 |
						uint32(bh.BData[off+10])<<16 |
						uint32(bh.BData[off+11])<<24)
					p.NrSects = int32(uint32(bh.BData[off+12]) |
						uint32(bh.BData[off+13])<<8 |
						uint32(bh.BData[off+14])<<16 |
						uint32(bh.BData[off+15])<<24)
				}
			}
		}
		if brelseFn != nil { brelseFn(bh) }
	}
	log.Printf("hd: partition table(s) ok.")
	return 0
}

func HdInit() {
	BlkDev[MAJOR_NR_HD].RequestFn = DoHdRequest
	log.Printf("hd: hd_init done, %d drives", NrHd)
}

func SetDiskImage(drive int, data []byte) {
	if drive >= 0 && drive < MAX_HD {
		DiskImages[drive] = data
		if drive >= NrHd { NrHd = drive + 1 }
		sectors := len(data) / SECTOR_SIZE
		HdInfo[drive].Sect = 63
		HdInfo[drive].Head = 16
		if HdInfo[drive].Sect*HdInfo[drive].Head > 0 {
			HdInfo[drive].Cyl = sectors / (HdInfo[drive].Sect * HdInfo[drive].Head)
		}
		Hd[drive*5].StartSect = 0
		Hd[drive*5].NrSects = int32(sectors)
	}
}

func UnexpectedHdInterrupt() {
	log.Printf("hd: unexpected interrupt")
}
