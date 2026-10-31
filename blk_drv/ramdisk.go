package blk_drv

import (
	"log"
	"sync"

	. "google.golang.org/adk/v2/include"
)

const (
	RAMDISK_MAJOR = 1
	RAMDISK_SIZE  = 2 * 1024 * 1024
)

var (
	rdData   []byte
	rdLength int
	rdMu     sync.Mutex
)

func RdInit(memStart uint32, length int) uint32 {
	rdLength = length
	rdData = make([]byte, length)
	BlkDev[RAMDISK_MAJOR].RequestFn = doRdRequest
	log.Printf("blk_drv: ramdisk: %d bytes", length)
	return memStart + uint32(length)
}

func doRdRequest() {
	rdMu.Lock()
	defer rdMu.Unlock()

	for {
		req := BlkDev[RAMDISK_MAJOR].CurrentRequest
		if req == nil {
			return
		}
		if req.Dev != RAMDISK_MAJOR {
			log.Printf("blk_drv: ramdisk: request for wrong device")
			endRdRequest(0)
			continue
		}

		addr := int(req.Sector) * 512
		nBytes := int(req.NrSectors) * 512

		if addr+nBytes > rdLength {
			endRdRequest(0)
			continue
		}

		if req.Cmd == WRITE {
			copy(rdData[addr:addr+nBytes], req.Buffer[:nBytes])
		} else if req.Cmd == READ {
			copy(req.Buffer[:nBytes], rdData[addr:addr+nBytes])
		} else {
			log.Printf("blk_drv: ramdisk: unknown command %d", req.Cmd)
			endRdRequest(0)
			continue
		}
		endRdRequest(1)
	}
}

func endRdRequest(uptodate int) {
	EndRequestForDev(RAMDISK_MAJOR, uptodate)
}

func RdLoad() {
	if rdData == nil {
		return
	}
	log.Printf("blk_drv: ramdisk loaded")
}

func GetRdData() []byte {
	rdMu.Lock()
	defer rdMu.Unlock()
	out := make([]byte, rdLength)
	copy(out, rdData)
	return out
}

func SetRdData(data []byte) {
	rdMu.Lock()
	defer rdMu.Unlock()
	n := len(data)
	if n > rdLength {
		n = rdLength
	}
	copy(rdData[:n], data[:n])
}

func RdSize() int {
	return rdLength
}

func RdClear() {
	rdMu.Lock()
	defer rdMu.Unlock()
	for i := range rdData {
		rdData[i] = 0
	}
}

func RdRead(offset, length int) []byte {
	rdMu.Lock()
	defer rdMu.Unlock()
	if offset < 0 || offset >= rdLength {
		return nil
	}
	end := offset + length
	if end > rdLength {
		end = rdLength
	}
	out := make([]byte, end-offset)
	copy(out, rdData[offset:end])
	return out
}

func RdWrite(offset int, data []byte) int {
	rdMu.Lock()
	defer rdMu.Unlock()
	if offset < 0 || offset >= rdLength {
		return -1
	}
	end := offset + len(data)
	if end > rdLength {
		end = rdLength
	}
	n := end - offset
	copy(rdData[offset:end], data[:n])
	return n
}
