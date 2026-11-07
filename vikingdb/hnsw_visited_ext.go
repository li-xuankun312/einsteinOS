package vikingdb

import (
	"sync"
	"sync/atomic"
)

type generationVisited struct {
	gen     uint64
	nodeGen []uint64
	size    int
}

func newGenerationVisited(size int) *generationVisited {
	return &generationVisited{
		gen:     1,
		nodeGen: make([]uint64, size),
		size:    size,
	}
}

func (gv *generationVisited) Visit(node uint64) {
	if int(node) >= len(gv.nodeGen) {
		gv.grow(node)
	}
	gv.nodeGen[node] = gv.gen
}

func (gv *generationVisited) Visited(node uint64) bool {
	if int(node) >= len(gv.nodeGen) {
		return false
	}
	return gv.nodeGen[node] == gv.gen
}

func (gv *generationVisited) CheckAndVisit(node uint64) bool {
	if int(node) >= len(gv.nodeGen) {
		gv.grow(node)
	}
	was := gv.nodeGen[node] == gv.gen
	gv.nodeGen[node] = gv.gen
	return was
}

func (gv *generationVisited) Reset() {
	gv.gen++
	if gv.gen == 0 {
		gv.gen = 1
		for i := range gv.nodeGen {
			gv.nodeGen[i] = 0
		}
	}
}

func (gv *generationVisited) grow(node uint64) {
	need := int(node) + 1
	if need < len(gv.nodeGen)*2 {
		need = len(gv.nodeGen) * 2
	}
	newSlice := make([]uint64, need)
	copy(newSlice, gv.nodeGen)
	gv.nodeGen = newSlice
}

type visitedPool struct {
	pool sync.Pool
	size int
}

func newVisitedPool(initialSize int) *visitedPool {
	return &visitedPool{
		size: initialSize,
		pool: sync.Pool{
			New: func() interface{} {
				return newGenerationVisited(initialSize)
			},
		},
	}
}

func (vp *visitedPool) Get() *generationVisited {
	v := vp.pool.Get().(*generationVisited)
	v.Reset()
	return v
}

func (vp *visitedPool) Put(v *generationVisited) {
	vp.pool.Put(v)
}

type concurrentVisited struct {
	shards    [16]visitedShard
	shardMask uint64
}

type visitedShard struct {
	mu    sync.Mutex
	nodes map[uint64]bool
}

func newConcurrentVisited() *concurrentVisited {
	cv := &concurrentVisited{shardMask: 15}
	for i := range cv.shards {
		cv.shards[i].nodes = make(map[uint64]bool)
	}
	return cv
}

func (cv *concurrentVisited) Visit(node uint64) {
	shard := &cv.shards[node&cv.shardMask]
	shard.mu.Lock()
	shard.nodes[node] = true
	shard.mu.Unlock()
}

func (cv *concurrentVisited) Visited(node uint64) bool {
	shard := &cv.shards[node&cv.shardMask]
	shard.mu.Lock()
	v := shard.nodes[node]
	shard.mu.Unlock()
	return v
}

func (cv *concurrentVisited) CheckAndVisit(node uint64) bool {
	shard := &cv.shards[node&cv.shardMask]
	shard.mu.Lock()
	was := shard.nodes[node]
	shard.nodes[node] = true
	shard.mu.Unlock()
	return was
}

func (cv *concurrentVisited) Reset() {
	for i := range cv.shards {
		cv.shards[i].mu.Lock()
		cv.shards[i].nodes = make(map[uint64]bool)
		cv.shards[i].mu.Unlock()
	}
}

func (cv *concurrentVisited) Count() int {
	total := 0
	for i := range cv.shards {
		cv.shards[i].mu.Lock()
		total += len(cv.shards[i].nodes)
		cv.shards[i].mu.Unlock()
	}
	return total
}

type atomicVisited struct {
	bits []atomic.Uint64
	size int
}

func newAtomicVisited(size int) *atomicVisited {
	wordCount := (size + 63) / 64
	return &atomicVisited{
		bits: make([]atomic.Uint64, wordCount),
		size: size,
	}
}

func (av *atomicVisited) Visit(node uint64) {
	idx := node / 64
	if int(idx) >= len(av.bits) {
		return
	}
	bit := uint64(1) << (node % 64)
	for {
		old := av.bits[idx].Load()
		if av.bits[idx].CompareAndSwap(old, old|bit) {
			return
		}
	}
}

func (av *atomicVisited) Visited(node uint64) bool {
	idx := node / 64
	if int(idx) >= len(av.bits) {
		return false
	}
	return av.bits[idx].Load()&(uint64(1)<<(node%64)) != 0
}

func (av *atomicVisited) CheckAndVisit(node uint64) bool {
	idx := node / 64
	if int(idx) >= len(av.bits) {
		return false
	}
	bit := uint64(1) << (node % 64)
	for {
		old := av.bits[idx].Load()
		if old&bit != 0 {
			return true
		}
		if av.bits[idx].CompareAndSwap(old, old|bit) {
			return false
		}
	}
}

func (av *atomicVisited) Reset() {
	for i := range av.bits {
		av.bits[i].Store(0)
	}
}

func (av *atomicVisited) Count() int {
	count := 0
	for i := range av.bits {
		v := av.bits[i].Load()
		for v != 0 {
			count++
			v &= v - 1
		}
	}
	return count
}
