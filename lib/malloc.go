package lib

import "sync"

const (
	PAGE_SIZE_M    = 4096
	BLOCK_SIZES    = 9
	MEM_USED       = 100
)

type BucketDesc struct {
	Next       *BucketDesc
	Page       []byte
	FreePtr    int
	RefCnt     int
	BucketSize int
}

type BucketDir struct {
	Size  int
	Chain *BucketDesc
}

var bucketDirTable = [BLOCK_SIZES]BucketDir{
	{16, nil}, {32, nil}, {64, nil}, {128, nil},
	{256, nil}, {512, nil}, {1024, nil}, {2048, nil}, {4096, nil},
}

var freeBucketDescs *BucketDesc
var mallocMu sync.Mutex

func initBucketDesc() *BucketDesc {
	page := make([]byte, PAGE_SIZE_M)
	count := PAGE_SIZE_M / bucketDescSize()
	var first *BucketDesc
	for i := 0; i < count; i++ {
		bd := &BucketDesc{
			Page: page,
		}
		bd.Next = first
		first = bd
	}
	return first
}

func bucketDescSize() int { return 64 }

func KernelMalloc(size int) []byte {
	mallocMu.Lock()
	defer mallocMu.Unlock()
	if size <= 0 {
		return nil
	}
	var bdir *BucketDir
	for i := 0; i < BLOCK_SIZES; i++ {
		if bucketDirTable[i].Size >= size {
			bdir = &bucketDirTable[i]
			break
		}
	}
	if bdir == nil {
		return make([]byte, size)
	}
	bdesc := bdir.Chain
	for bdesc != nil {
		if bdesc.FreePtr >= 0 {
			break
		}
		bdesc = bdesc.Next
	}
	if bdesc == nil {
		if freeBucketDescs == nil {
			freeBucketDescs = initBucketDesc()
		}
		bdesc = freeBucketDescs
		freeBucketDescs = bdesc.Next
		bdesc.RefCnt = 0
		bdesc.BucketSize = bdir.Size
		bdesc.Page = make([]byte, PAGE_SIZE_M)
		bdesc.FreePtr = 0
		itemCount := PAGE_SIZE_M / bdir.Size
		for i := 0; i < itemCount-1; i++ {
			off := i * bdir.Size
			next := (i + 1) * bdir.Size
			bdesc.Page[off] = byte(next & 0xFF)
			bdesc.Page[off+1] = byte((next >> 8) & 0xFF)
		}
		lastOff := (itemCount - 1) * bdir.Size
		bdesc.Page[lastOff] = 0xFF
		bdesc.Page[lastOff+1] = 0xFF
		bdesc.Next = bdir.Chain
		bdir.Chain = bdesc
	}
	off := bdesc.FreePtr
	if off < 0 || off >= len(bdesc.Page) {
		return nil
	}
	nextFree := int(bdesc.Page[off]) | int(bdesc.Page[off+1])<<8
	if nextFree == 0xFFFF {
		bdesc.FreePtr = -1
	} else {
		bdesc.FreePtr = nextFree
	}
	bdesc.RefCnt++
	result := make([]byte, bdir.Size)
	copy(result, bdesc.Page[off:off+bdir.Size])
	return result
}

func KernelFree(obj []byte) {
	mallocMu.Lock()
	defer mallocMu.Unlock()
	if obj == nil {
		return
	}
	size := len(obj)
	var bdir *BucketDir
	for i := 0; i < BLOCK_SIZES; i++ {
		if bucketDirTable[i].Size >= size {
			bdir = &bucketDirTable[i]
			break
		}
	}
	if bdir == nil {
		return
	}
	bdesc := bdir.Chain
	for bdesc != nil {
		bdesc.RefCnt--
		if bdesc.RefCnt == 0 {
			prev := &bdir.Chain
			for *prev != bdesc {
				prev = &(*prev).Next
			}
			*prev = bdesc.Next
			bdesc.Next = freeBucketDescs
			freeBucketDescs = bdesc
		}
		return
	}
}
