package mm

import (
	"log"
	"sync"
)

const (
	SWAP_BITS  = 4096
	SWAP_PAGES = SWAP_BITS
)

var (
	swapBitmap   [SWAP_BITS / 8]byte
	swapMu       sync.Mutex
	swapInCount  uint64
	swapOutCount uint64
	swapUsed     int
)

func SwapInit(dev int) {
	swapMu.Lock()
	defer swapMu.Unlock()

	for i := range swapBitmap {
		swapBitmap[i] = 0
	}
	swapBitmap[0] = 0x01
	swapUsed = 0

	log.Printf("mm: swap init on dev 0x%x, %d pages available", dev, SWAP_PAGES-1)
}

func getSwapPage() int {
	for i := 1; i < SWAP_PAGES; i++ {
		byteIdx := i / 8
		bitIdx := uint(i % 8)
		if swapBitmap[byteIdx]&(1<<bitIdx) == 0 {
			swapBitmap[byteIdx] |= 1 << bitIdx
			swapUsed++
			return i
		}
	}
	return 0
}

func freeSwapPage(nr int) {
	if nr < 1 || nr >= SWAP_PAGES {
		return
	}
	byteIdx := nr / 8
	bitIdx := uint(nr % 8)
	if swapBitmap[byteIdx]&(1<<bitIdx) == 0 {
		log.Printf("mm: double-free swap page %d", nr)
		return
	}
	swapBitmap[byteIdx] &^= 1 << bitIdx
	swapUsed--
}

func SwapOutPage(pageAddr uint32) int {
	swapMu.Lock()
	defer swapMu.Unlock()

	slot := getSwapPage()
	if slot == 0 {
		return 0
	}

	writeSwapSlot(slot, pageAddr)
	swapOutCount++
	return slot
}

func SwapInPage(slot int) uint32 {
	swapMu.Lock()
	defer swapMu.Unlock()

	page := GetFreePage()
	if page == 0 {
		return 0
	}

	readSwapSlot(slot, page)
	freeSwapPage(slot)
	swapInCount++

	return page
}

func writeSwapSlot(slot int, pageAddr uint32) {
	_ = slot
	_ = pageAddr
}

func readSwapSlot(slot int, pageAddr uint32) {
	_ = slot
	_ = pageAddr
}

func SwapFreeCount() int {
	swapMu.Lock()
	defer swapMu.Unlock()
	return SWAP_PAGES - 1 - swapUsed
}

func SwapUsedCount() int {
	swapMu.Lock()
	defer swapMu.Unlock()
	return swapUsed
}

func GetSwapStats() (in, out uint64) {
	swapMu.Lock()
	defer swapMu.Unlock()
	return swapInCount, swapOutCount
}

func TryToSwapOut(addr uint32) bool {
	dirIdx := addr >> 22
	tblIdx := (addr >> 12) & 0x3FF

	if pgDir[dirIdx]&1 == 0 {
		return false
	}

	pageTable := pgDir[dirIdx] & 0xFFFFF000
	entry := getPageTableEntry(pageTable, int(tblIdx))
	if entry == nil || *entry&1 == 0 {
		return false
	}

	physPage := *entry & 0xFFFFF000
	nr := MAP_NR(physPage)
	if nr >= PAGING_PAGES {
		return false
	}

	physMemMu.Lock()
	if memMap[nr] > 1 {
		physMemMu.Unlock()
		return false
	}
	physMemMu.Unlock()

	slot := SwapOutPage(physPage)
	if slot == 0 {
		return false
	}

	*entry = uint32(slot<<1) | 0
	FreePage(physPage)
	return true
}

func CheckAndSwapIn(entry *uint32) bool {
	if entry == nil || *entry&1 != 0 {
		return false
	}
	slot := int(*entry >> 1)
	if slot == 0 {
		return false
	}
	page := SwapInPage(slot)
	if page == 0 {
		return false
	}
	*entry = page | 7
	return true
}

func GetSwapSlotInfo(slot int) (used bool) {
	if slot < 1 || slot >= SWAP_PAGES {
		return false
	}
	swapMu.Lock()
	defer swapMu.Unlock()
	byteIdx := slot / 8
	bitIdx := uint(slot % 8)
	return swapBitmap[byteIdx]&(1<<bitIdx) != 0
}
