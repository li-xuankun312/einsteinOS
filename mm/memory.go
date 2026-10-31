package mm

import (
	"log"
	"sync"

	. "google.golang.org/adk/v2/include"
)

func oom() {
	log.Printf("out of memory")
	if doExitFn != nil {
		doExitFn(int32(SIGSEGV))
	}
}

var doExitFn func(int32)
func SetDoExit(fn func(int32)) { doExitFn = fn }

func invalidate() {}

const (
	LOW_MEM       = 0x100000
	PAGING_MEMORY = 15 * 1024 * 1024
	PAGING_PAGES  = PAGING_MEMORY >> 12
	USED          = 100
)

func MAP_NR(addr uint32) uint32 { return (addr - LOW_MEM) >> 12 }

func CODE_SPACE(addr uint32) bool {
	return ((addr+4095)&^uint32(4095)) < Current.StartCode+Current.EndCode
}

var HIGH_MEMORY uint32

func copyPage(from, to uint32) {
	src := physMem[from : from+PAGE_SIZE]
	dst := physMem[to : to+PAGE_SIZE]
	copy(dst, src)
}

var memMap [PAGING_PAGES]uint8

var physMem []byte
var physMemMu sync.Mutex

var pgDir [1024]uint32

func getPageTableEntry(pgTableAddr uint32, index int) *uint32 {
	offset := pgTableAddr + uint32(index)*4
	if int(offset+4) > len(physMem) {
		return nil
	}
	return &pageTableStore[offset>>2]
}

var pageTableStore []uint32

func GetFreePage() uint32 {
	physMemMu.Lock()
	defer physMemMu.Unlock()

	for i := PAGING_PAGES - 1; i >= 0; i-- {
		if memMap[i] == 0 {
			memMap[i] = 1
			addr := uint32(i)<<12 + LOW_MEM
			if int(addr+PAGE_SIZE) <= len(physMem) {
				for j := uint32(0); j < PAGE_SIZE; j++ {
					physMem[addr+j] = 0
				}
			}
			return addr
		}
	}
	return 0
}

func FreePage(addr uint32) {
	if addr < LOW_MEM {
		return
	}
	if addr >= HIGH_MEMORY {
		log.Printf("mm: trying to free nonexistent page %x", addr)
		return
	}
	physMemMu.Lock()
	defer physMemMu.Unlock()
	addr -= LOW_MEM
	addr >>= 12
	if memMap[addr] > 0 {
		memMap[addr]--
		return
	}
	memMap[addr] = 0
	log.Printf("mm: trying to free free page")
}

func FreePageTables(from uint32, size int32) int {
	if from&0x3fffff != 0 {
		log.Printf("mm: free_page_tables called with wrong alignment")
		return -1
	}
	if from == 0 {
		log.Printf("mm: Trying to free up swapper memory space")
		return -1
	}
	sz := uint32((int64(size) + 0x3fffff) >> 22)
	dirIdx := (from >> 22)

	for ; sz > 0; sz-- {
		if pgDir[dirIdx]&1 == 0 {
			dirIdx++
			continue
		}
		pgTable := pgDir[dirIdx] & 0xfffff000
		for nr := uint32(0); nr < 1024; nr++ {
			entry := getPageTableEntry(pgTable, int(nr))
			if entry != nil && *entry&1 != 0 {
				FreePage(*entry & 0xfffff000)
			}
			if entry != nil {
				*entry = 0
			}
		}
		FreePage(pgDir[dirIdx] & 0xfffff000)
		pgDir[dirIdx] = 0
		dirIdx++
	}
	invalidate()
	return 0
}

func CopyPageTables(from, to uint32, size int32) int {
	if (from&0x3fffff) != 0 || (to&0x3fffff) != 0 {
		log.Printf("mm: copy_page_tables called with wrong alignment")
		return -1
	}
	fromDir := from >> 22
	toDir := to >> 22
	sz := uint32((int64(size) + 0x3fffff) >> 22)

	for ; sz > 0; sz-- {
		if pgDir[toDir]&1 != 0 {
			log.Printf("mm: copy_page_tables: already exist")
			return -1
		}
		if pgDir[fromDir]&1 == 0 {
			fromDir++
			toDir++
			continue
		}
		fromPgTable := pgDir[fromDir] & 0xfffff000

		toPgTableAddr := GetFreePage()
		if toPgTableAddr == 0 {
			return -1
		}
		pgDir[toDir] = toPgTableAddr | 7

		nr := uint32(1024)
		if from == 0 {
			nr = 0xA0
		}

		for ; nr > 0; nr-- {
			idx := 1024 - int(nr)
			fromEntry := getPageTableEntry(fromPgTable, idx)
			toEntry := getPageTableEntry(toPgTableAddr, idx)
			if fromEntry == nil || toEntry == nil {
				continue
			}

			thisPage := *fromEntry
			if thisPage&1 == 0 {
				continue
			}

			thisPage &= ^uint32(2)
			*toEntry = thisPage

			if thisPage > LOW_MEM {
				*fromEntry = thisPage
				physAddr := thisPage - LOW_MEM
				physAddr >>= 12
				physMemMu.Lock()
				if physAddr < PAGING_PAGES {
					memMap[physAddr]++
				}
				physMemMu.Unlock()
			}
		}
		fromDir++
		toDir++
	}
	invalidate()
	return 0
}

func PutPage(page, address uint32) uint32 {
	if page < LOW_MEM || page >= HIGH_MEMORY {
		log.Printf("mm: Trying to put page %x at %x", page, address)
	}
	physMemMu.Lock()
	mapIdx := (page - LOW_MEM) >> 12
	if mapIdx < PAGING_PAGES && memMap[mapIdx] != 1 {
		log.Printf("mm: mem_map disagrees with %x at %x", page, address)
	}
	physMemMu.Unlock()

	dirIdx := address >> 22
	if pgDir[dirIdx]&1 != 0 {
	} else {
		tmp := GetFreePage()
		if tmp == 0 {
			return 0
		}
		pgDir[dirIdx] = tmp | 7
	}
	pgTableAddr := pgDir[dirIdx] & 0xfffff000
	idx := (address >> 12) & 0x3ff
	entry := getPageTableEntry(pgTableAddr, int(idx))
	if entry != nil {
		*entry = page | 7
	}
	return page
}

func UnWpPage(tableEntry *uint32) {
	if tableEntry == nil {
		return
	}
	oldPage := *tableEntry & 0xfffff000

	physMemMu.Lock()
	mapIdx := MAP_NR(oldPage)
	if oldPage >= LOW_MEM && mapIdx < PAGING_PAGES && memMap[mapIdx] == 1 {
		*tableEntry |= 2
		physMemMu.Unlock()
		invalidate()
		return
	}
	physMemMu.Unlock()

	newPage := GetFreePage()
	if newPage == 0 {
		oom()
		return
	}

	if oldPage >= LOW_MEM {
		physMemMu.Lock()
		mapIdx := MAP_NR(oldPage)
		if mapIdx < PAGING_PAGES {
			memMap[mapIdx]--
		}
		physMemMu.Unlock()
	}
	*tableEntry = newPage | 7
	invalidate()
	copyPage(oldPage, newPage)
}

func DoWpPage(errorCode, address uint32) {
	dirIdx := address >> 22
	if pgDir[dirIdx]&1 == 0 {
		return
	}
	pgTableAddr := pgDir[dirIdx] & 0xfffff000
	idx := (address >> 12) & 0x3ff
	entry := getPageTableEntry(pgTableAddr, int(idx))
	if entry != nil {
		UnWpPage(entry)
	}
}

func WriteVerify(address uint32) {
	dirIdx := address >> 22
	page := pgDir[dirIdx]
	if page&1 == 0 {
		return
	}
	page &= 0xfffff000
	idx := (address >> 12) & 0x3ff
	entry := getPageTableEntry(page, int(idx))
	if entry != nil && (*entry&3) == 1 {
		UnWpPage(entry)
	}
}

func GetEmptyPage(address uint32) {
	tmp := GetFreePage()
	if tmp == 0 || PutPage(tmp, address) == 0 {
		FreePage(tmp)
		oom()
	}
}

func tryToShare(address uint32, p *TaskStruct) int {
	fromPage := (address >> 22)
	toPage := fromPage

	fromPage += (p.StartCode >> 22)
	toPage += (Current.StartCode >> 22)

	from := pgDir[fromPage]
	if from&1 == 0 {
		return 0
	}
	from &= 0xfffff000
	fromIdx := (address >> 12) & 0x3ff
	fromEntry := getPageTableEntry(from, int(fromIdx))
	if fromEntry == nil {
		return 0
	}
	physAddr := *fromEntry

	if (physAddr & 0x41) != 0x01 {
		return 0
	}
	physAddr &= 0xfffff000
	if physAddr >= HIGH_MEMORY || physAddr < LOW_MEM {
		return 0
	}

	to := pgDir[toPage]
	if to&1 == 0 {
		newPt := GetFreePage()
		if newPt != 0 {
			pgDir[toPage] = newPt | 7
		} else {
			oom()
			return 0
		}
	}
	to = pgDir[toPage] & 0xfffff000
	toIdx := (address >> 12) & 0x3ff
	toEntry := getPageTableEntry(to, int(toIdx))
	if toEntry != nil && *toEntry&1 != 0 {
		log.Printf("mm: try_to_share: to_page already exists")
		return 0
	}

	*fromEntry &= ^uint32(2)
	if toEntry != nil {
		*toEntry = *fromEntry
	}
	invalidate()

	physMemMu.Lock()
	physAddr -= LOW_MEM
	physAddr >>= 12
	if physAddr < PAGING_PAGES {
		memMap[physAddr]++
	}
	physMemMu.Unlock()
	return 1
}

func sharePage(address uint32) int {
	if Current.Executable == nil {
		return 0
	}
	if Current.Executable.ICount < 2 {
		return 0
	}
	for i := NR_TASKS - 1; i > 0; i-- {
		if Task[i] == nil {
			continue
		}
		if Current == Task[i] {
			continue
		}
		if Task[i].Executable != Current.Executable {
			continue
		}
		if tryToShare(address, Task[i]) != 0 {
			return 1
		}
	}
	return 0
}

func DoNoPage(errorCode, address uint32) {
	address &= 0xfffff000
	tmp := address - Current.StartCode

	if Current.Executable == nil || tmp >= Current.EndData {
		GetEmptyPage(address)
		return
	}
	if sharePage(tmp) != 0 {
		return
	}

	page := GetFreePage()
	if page == 0 {
		oom()
		return
	}

	i := int32(tmp) + 4096 - int32(Current.EndData)
	tmpAddr := page + 4096
	for i > 0 {
		i--
		tmpAddr--
		if int(tmpAddr) < len(physMem) {
			physMem[tmpAddr] = 0
		}
	}

	if PutPage(page, address) != 0 {
		return
	}
	FreePage(page)
	oom()
}

func MemInit(startMem, endMem uint32) {
	HIGH_MEMORY = endMem

	physMem = make([]byte, endMem)
	pageTableStore = make([]uint32, endMem/4)

	for i := 0; i < PAGING_PAGES; i++ {
		memMap[i] = USED
	}
	i := MAP_NR(startMem)
	remain := (endMem - startMem) >> 12
	for remain > 0 {
		remain--
		if i < PAGING_PAGES {
			memMap[i] = 0
		}
		i++
	}

	free := 0
	for j := 0; j < PAGING_PAGES; j++ {
		if memMap[j] == 0 {
			free++
		}
	}
	log.Printf("mm: mem_init done, %d pages free", free)
}

func CalcMem() {
	free := 0
	for i := 0; i < PAGING_PAGES; i++ {
		if memMap[i] == 0 {
			free++
		}
	}
	log.Printf("%d pages free (of %d)", free, PAGING_PAGES)

	for i := 2; i < 1024; i++ {
		if pgDir[i]&1 != 0 {
			pgTable := pgDir[i] & 0xfffff000
			k := 0
			for j := 0; j < 1024; j++ {
				entry := getPageTableEntry(pgTable, j)
				if entry != nil && *entry&1 != 0 {
					k++
				}
			}
			log.Printf("Pg-dir[%d] uses %d pages", i, k)
		}
	}
}

func FreeMemCount() int {
	physMemMu.Lock()
	defer physMemMu.Unlock()
	free := 0
	for i := 0; i < int(PAGING_PAGES); i++ {
		if memMap[i] == 0 {
			free++
		}
	}
	return free
}

func UsedMemCount() int {
	physMemMu.Lock()
	defer physMemMu.Unlock()
	used := 0
	for i := 0; i < int(PAGING_PAGES); i++ {
		if memMap[i] != 0 {
			used++
		}
	}
	return used
}

func SharedMemCount() int {
	physMemMu.Lock()
	defer physMemMu.Unlock()
	shared := 0
	for i := 0; i < int(PAGING_PAGES); i++ {
		if memMap[i] > 1 {
			shared++
		}
	}
	return shared
}

func GetPageRefCount(addr uint32) byte {
	nr := MAP_NR(addr)
	if nr >= PAGING_PAGES {
		return 0
	}
	physMemMu.Lock()
	defer physMemMu.Unlock()
	return memMap[nr]
}
