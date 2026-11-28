package fs

import (
	"log"
	"sync"

	. "google.golang.org/adk/v2/include"
)

var (
	StartBuffer *BufferHead
	hashTable   [NR_HASH]*BufferHead
	freeList    *BufferHead
	bufferWait  *TaskStruct
	NrBuffers   int
	buffers     []BufferHead
	bufferData  []byte
	bufMu       sync.Mutex
)

var (
	sleepOnFn  func(**TaskStruct)
	wakeUpFn   func(**TaskStruct)
	llRwBlockFn func(int, *BufferHead)
	syncInodesFn func()
	putSuperFn   func(int)
	invalidateInodesFn func(int)
)

func SetSleepOn(fn func(**TaskStruct))          { sleepOnFn = fn }
func SetWakeUp(fn func(**TaskStruct))            { wakeUpFn = fn }
func SetLlRwBlock(fn func(int, *BufferHead))     { llRwBlockFn = fn }
func SetSyncInodes(fn func())                    { syncInodesFn = fn }
func SetPutSuper(fn func(int))                   { putSuperFn = fn }
func SetInvalidateInodes(fn func(int))           { invalidateInodesFn = fn }

func waitOnBuffer(bh *BufferHead) {
	bufMu.Lock()
	for bh.BLock != 0 {
		bufMu.Unlock()
		if sleepOnFn != nil {
			sleepOnFn(&bh.BWait)
		}
		bufMu.Lock()
	}
	bufMu.Unlock()
}

func SysSync() int {
	SyncInodes()
	for i := 0; i < NrBuffers; i++ {
		bh := &buffers[i]
		waitOnBuffer(bh)
		if bh.BDirt != 0 {
			llRwBlock(WRITE, bh)
		}
	}
	return 0
}

func SyncDev(dev int) int {
	for i := 0; i < NrBuffers; i++ {
		bh := &buffers[i]
		if bh.BDev != uint16(dev) { continue }
		waitOnBuffer(bh)
		if bh.BDev == uint16(dev) && bh.BDirt != 0 {
			llRwBlock(WRITE, bh)
		}
	}
	SyncInodes()
	for i := 0; i < NrBuffers; i++ {
		bh := &buffers[i]
		if bh.BDev != uint16(dev) { continue }
		waitOnBuffer(bh)
		if bh.BDev == uint16(dev) && bh.BDirt != 0 {
			llRwBlock(WRITE, bh)
		}
	}
	return 0
}

func invalidateBuffers(dev int) {
	for i := 0; i < NrBuffers; i++ {
		bh := &buffers[i]
		if bh.BDev != uint16(dev) { continue }
		waitOnBuffer(bh)
		if bh.BDev == uint16(dev) {
			bh.BUptodate = 0
			bh.BDirt = 0
		}
	}
}

func CheckDiskChange(dev int) {
	if MAJOR(uint32(dev)) != 2 { return }
	invalidateBuffers(dev)
}

func hashfn(dev, block int) int {
	return int(uint(dev^block) % NR_HASH)
}

func removeFromQueues(bh *BufferHead) {
	if bh.BNext != nil { bh.BNext.BPrev = bh.BPrev }
	if bh.BPrev != nil { bh.BPrev.BNext = bh.BNext }
	if hashTable[hashfn(int(bh.BDev), int(bh.BBlocknr))] == bh {
		hashTable[hashfn(int(bh.BDev), int(bh.BBlocknr))] = bh.BNext
	}
	if bh.BPrevFree == nil || bh.BNextFree == nil {
		log.Printf("fs: Free block list corrupted")
		return
	}
	bh.BPrevFree.BNextFree = bh.BNextFree
	bh.BNextFree.BPrevFree = bh.BPrevFree
	if freeList == bh { freeList = bh.BNextFree }
}

func insertIntoQueues(bh *BufferHead) {
	bh.BNextFree = freeList
	bh.BPrevFree = freeList.BPrevFree
	freeList.BPrevFree.BNextFree = bh
	freeList.BPrevFree = bh
	bh.BPrev = nil
	bh.BNext = nil
	if bh.BDev == 0 { return }
	bh.BNext = hashTable[hashfn(int(bh.BDev), int(bh.BBlocknr))]
	hashTable[hashfn(int(bh.BDev), int(bh.BBlocknr))] = bh
	if bh.BNext != nil { bh.BNext.BPrev = bh }
}

func findBuffer(dev, block int) *BufferHead {
	for tmp := hashTable[hashfn(dev, block)]; tmp != nil; tmp = tmp.BNext {
		if tmp.BDev == uint16(dev) && tmp.BBlocknr == uint32(block) {
			return tmp
		}
	}
	return nil
}

func GetHashTable(dev, block int) *BufferHead {
	for {
		bh := findBuffer(dev, block)
		if bh == nil { return nil }
		bh.BCount++
		waitOnBuffer(bh)
		if bh.BDev == uint16(dev) && bh.BBlocknr == uint32(block) {
			return bh
		}
		bh.BCount--
	}
}

func badness(bh *BufferHead) int { return int(bh.BDirt)<<1 + int(bh.BLock) }

func Getblk(dev, block int) *BufferHead {
repeat:
	if bh := GetHashTable(dev, block); bh != nil { return bh }
	tmp := freeList
	var bh *BufferHead
	for {
		if tmp.BCount == 0 {
			if bh == nil || badness(tmp) < badness(bh) {
				bh = tmp
				if badness(tmp) == 0 { break }
			}
		}
		tmp = tmp.BNextFree
		if tmp == freeList { break }
	}
	if bh == nil {
		if sleepOnFn != nil { sleepOnFn(&bufferWait) }
		goto repeat
	}
	waitOnBuffer(bh)
	if bh.BCount != 0 { goto repeat }
	for bh.BDirt != 0 {
		SyncDev(int(bh.BDev))
		waitOnBuffer(bh)
		if bh.BCount != 0 { goto repeat }
	}
	if findBuffer(dev, block) != nil { goto repeat }
	bh.BCount = 1
	bh.BDirt = 0
	bh.BUptodate = 0
	removeFromQueues(bh)
	bh.BDev = uint16(dev)
	bh.BBlocknr = uint32(block)
	insertIntoQueues(bh)
	return bh
}

func Brelse(buf *BufferHead) {
	if buf == nil { return }
	waitOnBuffer(buf)
	if buf.BCount == 0 {
		log.Printf("fs: Trying to free free buffer")
		return
	}
	buf.BCount--
	if wakeUpFn != nil { wakeUpFn(&bufferWait) }
}

func Bread(dev, block int) *BufferHead {
	bh := Getblk(dev, block)
	if bh == nil { log.Printf("fs: bread: getblk returned NULL"); return nil }
	if bh.BUptodate != 0 { return bh }
	llRwBlock(READ, bh)
	waitOnBuffer(bh)
	if bh.BUptodate != 0 { return bh }
	Brelse(bh)
	return nil
}

func BreadPage(address uint32, dev int, b [4]int) {
	var bh [4]*BufferHead
	for i := 0; i < 4; i++ {
		if b[i] != 0 {
			bh[i] = Getblk(dev, b[i])
			if bh[i] != nil && bh[i].BUptodate == 0 { llRwBlock(READ, bh[i]) }
		}
	}
	for i := 0; i < 4; i++ {
		if bh[i] != nil {
			waitOnBuffer(bh[i])
			Brelse(bh[i])
		}
		address += BLOCK_SIZE
	}
}

func Breada(dev, first int, blocks ...int) *BufferHead {
	bh := Getblk(dev, first)
	if bh == nil { log.Printf("fs: breada: getblk returned NULL"); return nil }
	if bh.BUptodate == 0 { llRwBlock(READ, bh) }
	for _, b := range blocks {
		if b < 0 { break }
		tmp := Getblk(dev, b)
		if tmp != nil {
			if tmp.BUptodate == 0 { llRwBlock(READA, tmp) }
			tmp.BCount--
		}
	}
	waitOnBuffer(bh)
	if bh.BUptodate != 0 { return bh }
	Brelse(bh)
	return nil
}

func llRwBlock(rw int, bh *BufferHead) {
	if llRwBlockFn != nil { llRwBlockFn(rw, bh) }
}

func BufferInit(bufferEnd int32) {
	numBuffers := 256
	if bufferEnd > 0 {
		numBuffers = int(bufferEnd) / (BLOCK_SIZE + 64)
		if numBuffers < 32 { numBuffers = 32 }
		if numBuffers > 4096 { numBuffers = 4096 }
	}
	buffers = make([]BufferHead, numBuffers)
	bufferData = make([]byte, numBuffers*BLOCK_SIZE)
	for i := 0; i < numBuffers; i++ {
		h := &buffers[i]
		h.BDev = 0; h.BDirt = 0; h.BCount = 0; h.BLock = 0
		h.BUptodate = 0; h.BWait = nil; h.BNext = nil; h.BPrev = nil
		h.BData = bufferData[i*BLOCK_SIZE : (i+1)*BLOCK_SIZE]
		if i > 0 { h.BPrevFree = &buffers[i-1] }
		if i < numBuffers-1 { h.BNextFree = &buffers[i+1] }
	}
	buffers[0].BPrevFree = &buffers[numBuffers-1]
	buffers[numBuffers-1].BNextFree = &buffers[0]
	freeList = &buffers[0]
	StartBuffer = &buffers[0]
	NrBuffers = numBuffers
	for i := 0; i < NR_HASH; i++ { hashTable[i] = nil }
	log.Printf("fs: buffer_init: %d buffers", NrBuffers)
}
