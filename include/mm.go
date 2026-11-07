package include

const PAGE_SIZE = 4096

const (
	BUFFER_END_1M  = 0x100000
	BUFFER_END_2M  = 0x200000
	BUFFER_END_4M  = 0x400000
	MEMORY_END_16M = 0x1000000
)

const (
	PG_PRESENT  = 0x001
	PG_RW       = 0x002
	PG_USER     = 0x004
	PG_ACCESSED = 0x020
	PG_DIRTY    = 0x040
)

func PAGE_MASK() uint32 { return ^uint32(PAGE_SIZE - 1) }

type PageInfo struct {
	Flags    uint32
	RefCount int32
	MapCount int32
}
