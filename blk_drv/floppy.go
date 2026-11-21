package blk_drv

import (
	"log"
	"sync"
	"time"

	. "google.golang.org/adk/v2/include"
)

const (
	FLOPPY_MAJOR     = 2
	MAX_REPLIES      = 7
	NR_DRIVES        = 4
	FLOPPY_DMA       = 2
	FLOPPY_IRQ       = 6
	MOTOR_ON_DELAY   = 50
	MOTOR_OFF_DELAY  = 300
	RATE_500K        = 0x00
	RATE_300K        = 0x01
	RATE_250K        = 0x02
)

const (
	FD_STATUS   = 0x3f4
	FD_DATA     = 0x3f5
	FD_DOR      = 0x3f2
	FD_DIR      = 0x3f7
	FD_DCR      = 0x3f7
	STATUS_BUSY = 0x10
	STATUS_DIR  = 0x40
	STATUS_READY = 0x80
)

const (
	FD_RECALIBRATE = 0x07
	FD_SEEK        = 0x0f
	FD_READ        = 0xe6
	FD_WRITE       = 0xc5
	FD_SENSEI      = 0x08
	FD_SPECIFY     = 0x03
)

type FloppyStruct struct {
	Size  uint32
	Sect  uint32
	Head  uint32
	Track uint32
	Stretch uint32
	Gap   byte
	Rate  byte
	Spec1 byte
}

var floppyType = [5]FloppyStruct{
	{0, 0, 0, 0, 0, 0, 0, 0},
	{2880, 18, 2, 80, 0, 0x1B, RATE_500K, 0xCF},
	{1440, 9, 2, 80, 0, 0x2A, RATE_250K, 0xDF},
	{720, 9, 2, 40, 1, 0x2A, RATE_250K, 0xDF},
	{2400, 15, 2, 80, 0, 0x1B, RATE_500K, 0xDF},
}

var (
	floppyMu        sync.Mutex
	currentDrive    byte
	curSpec1        byte = 0xFF
	curRate         byte = 0xFF
	seekTrack       [NR_DRIVES]byte
	currentTrack    [NR_DRIVES]byte
	motorState      [NR_DRIVES]byte
	motorOnCount    [NR_DRIVES]uint16
	motorOffCount   [NR_DRIVES]uint16
	needRecal       [NR_DRIVES]byte
	floppyRecalibrate int
	floppyResetFlag   int
	floppySeekFlag    int
	replyBuffer     [MAX_REPLIES]byte
)

func FloppyInit() {
	BlkDev[FLOPPY_MAJOR].RequestFn = doFloppyRequest
	for i := 0; i < NR_DRIVES; i++ {
		motorState[i] = 0
		motorOnCount[i] = 0
		motorOffCount[i] = 0
		needRecal[i] = 1
		currentTrack[i] = 255
		seekTrack[i] = 0
	}
	log.Printf("blk_drv: floppy_init done")
}

var floppyData [NR_DRIVES][]byte

func SetFloppyImage(drive int, data []byte) {
	if drive < 0 || drive >= NR_DRIVES {
		return
	}
	floppyMu.Lock()
	defer floppyMu.Unlock()
	floppyData[drive] = make([]byte, len(data))
	copy(floppyData[drive], data)
}

func doFloppyRequest() {
	floppyMu.Lock()
	defer floppyMu.Unlock()

	for {
		req := BlkDev[FLOPPY_MAJOR].CurrentRequest
		if req == nil {
			return
		}

		drive := req.Dev & 0x03
		if drive >= NR_DRIVES || floppyData[drive] == nil {
			endFloppyRequest(0)
			continue
		}

		floppy := &floppyType[1]
		if floppy.Size == 0 {
			endFloppyRequest(0)
			continue
		}

		sector := req.Sector
		if sector >= floppy.Size {
			endFloppyRequest(0)
			continue
		}

		addr := int(sector) * 512
		nBytes := int(req.NrSectors) * 512
		diskLen := len(floppyData[drive])

		if addr >= diskLen {
			endFloppyRequest(0)
			continue
		}
		if addr+nBytes > diskLen {
			nBytes = diskLen - addr
		}

		time.Sleep(time.Millisecond)

		if req.Cmd == READ {
			copy(req.Buffer[:nBytes], floppyData[drive][addr:addr+nBytes])
		} else if req.Cmd == WRITE {
			copy(floppyData[drive][addr:addr+nBytes], req.Buffer[:nBytes])
		} else {
			endFloppyRequest(0)
			continue
		}
		endFloppyRequest(1)
	}
}

func endFloppyRequest(uptodate int) {
	EndRequestForDev(FLOPPY_MAJOR, uptodate)
}

func FloppyDeselect(nr uint32) {
	if nr >= NR_DRIVES {
		return
	}
	motorState[nr] = 0
}

func FloppyTimerTick() {
	for i := 0; i < NR_DRIVES; i++ {
		if motorOffCount[i] > 0 {
			motorOffCount[i]--
			if motorOffCount[i] == 0 {
				motorState[i] = 0
			}
		}
		if motorOnCount[i] > 0 {
			motorOnCount[i]--
			if motorOnCount[i] == 0 {
				motorState[i] = 1
			}
		}
	}
}

func floppyMotorOn(drive byte) {
	if drive >= NR_DRIVES {
		return
	}
	motorOnCount[drive] = MOTOR_ON_DELAY
	motorState[drive] = 1
}

func floppyMotorOff(drive byte) {
	if drive >= NR_DRIVES {
		return
	}
	motorOffCount[drive] = MOTOR_OFF_DELAY
}

func floppyChange(drive uint32) bool {
	return false
}

func FloppyGetGeometry(drive int) (sectors, heads, tracks uint32) {
	if drive < 0 || drive >= NR_DRIVES {
		return 0, 0, 0
	}
	ft := floppyType[1]
	return ft.Sect, ft.Head, ft.Track
}

func FloppyIsPresent(drive int) bool {
	if drive < 0 || drive >= NR_DRIVES {
		return false
	}
	return floppyData[drive] != nil
}
