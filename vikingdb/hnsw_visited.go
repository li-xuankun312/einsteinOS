package vikingdb

type visitedSet struct {
	bits     []uint64
	touched  []int
	gen      uint64
	genSlice []uint64
	size     int
}

func newVisitedSet(size int) *visitedSet {
	wordCount := (size + 63) / 64
	return &visitedSet{
		bits:     make([]uint64, wordCount),
		genSlice: make([]uint64, wordCount),
		touched:  make([]int, 0, 64),
		size:     size,
	}
}

func (v *visitedSet) Visit(node uint64) {
	idx := node / 64
	if int(idx) >= len(v.bits) {
		v.grow(node)
	}
	bit := uint64(1) << (node % 64)
	if v.bits[idx]&bit == 0 {
		v.touched = append(v.touched, int(idx))
	}
	v.bits[idx] |= bit
}

func (v *visitedSet) Visited(node uint64) bool {
	idx := node / 64
	if int(idx) >= len(v.bits) {
		return false
	}
	return v.bits[idx]&(uint64(1)<<(node%64)) != 0
}

func (v *visitedSet) CheckAndVisit(node uint64) bool {
	idx := node / 64
	if int(idx) >= len(v.bits) {
		v.grow(node)
	}
	bit := uint64(1) << (node % 64)
	was := v.bits[idx] & bit
	if was == 0 {
		v.bits[idx] |= bit
		v.touched = append(v.touched, int(idx))
	}
	return was != 0
}

func (v *visitedSet) Reset() {
	for _, idx := range v.touched {
		v.bits[idx] = 0
	}
	v.touched = v.touched[:0]
}

func (v *visitedSet) grow(node uint64) {
	need := int(node/64) + 1
	for len(v.bits) < need {
		v.bits = append(v.bits, 0)
	}
}
