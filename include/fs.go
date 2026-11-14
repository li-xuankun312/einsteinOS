package include

func IS_SEEKABLE(x int) bool { return x >= 1 && x <= 3 }

const (
	READ   = 0
	WRITE  = 1
	READA  = 2
	WRITEA = 3
)

func MAJOR(a uint32) uint32 { return a >> 8 }
func MINOR(a uint32) uint32 { return a & 0xff }

const (
	NAME_LEN       = 14
	ROOT_INO       = 1
	I_MAP_SLOTS    = 8
	Z_MAP_SLOTS    = 8
	SUPER_MAGIC    = 0x137F
	NR_OPEN        = 20
	NR_INODE       = 32
	NR_FILE        = 64
	NR_SUPER       = 8
	NR_HASH        = 307
	BLOCK_SIZE     = 1024
	BLOCK_SIZE_BITS = 10
)

func PIPE_HEAD(inode *MInode) uint16 { return inode.IZone[0] }
func PIPE_TAIL(inode *MInode) uint16 { return inode.IZone[1] }
func PIPE_SIZE(inode *MInode) uint16 {
	return (inode.IZone[0] - inode.IZone[1]) & (PAGE_SIZE - 1)
}
func PIPE_EMPTY(inode *MInode) bool { return inode.IZone[0] == inode.IZone[1] }
func PIPE_FULL(inode *MInode) bool {
	return PIPE_SIZE(inode) == (PAGE_SIZE - 1)
}

type BufferBlock [BLOCK_SIZE]byte

type BufferHead struct {
	BData      []byte
	BBlocknr   uint32
	BDev       uint16
	BUptodate  uint8
	BDirt      uint8
	BCount     uint8
	BLock      uint8
	BWait      *TaskStruct
	BPrev      *BufferHead
	BNext      *BufferHead
	BPrevFree  *BufferHead
	BNextFree  *BufferHead
}

type DInode struct {
	IMode  uint16
	IUid   uint16
	ISize  uint32
	ITime  uint32
	IGid   uint8
	INlinks uint8
	IZone  [9]uint16
}

type MInode struct {
	IMode   uint16
	IUid    uint16
	ISize   uint32
	IMtime  uint32
	IGid    uint8
	INlinks uint8
	IZone   [9]uint16
	IWait   *TaskStruct
	IAtime  uint32
	ICtime  uint32
	IDev    uint16
	INum    uint16
	ICount  uint16
	ILock   uint8
	IDirt   uint8
	IPipe   uint8
	IMount  uint8
	ISeek   uint8
	IUpdate uint8
}

type File struct {
	FMode  uint16
	FFlags uint16
	FCount uint16
	FInode *MInode
	FPos   int64
}

type SuperBlock struct {
	SNinodes       uint16
	SNzones        uint16
	SImapBlocks    uint16
	SZmapBlocks    uint16
	SFirstdatazone uint16
	SLogZoneSize   uint16
	SMaxSize       uint32
	SMagic         uint16
	SImap [I_MAP_SLOTS]*BufferHead
	SZmap [Z_MAP_SLOTS]*BufferHead
	SDev   uint16
	SIsup  *MInode
	SImount *MInode
	STime  uint32
	SWait  *TaskStruct
	SLock  uint8
	SRdOnly uint8
	SDirt  uint8
}

type DSuperBlock struct {
	SNinodes       uint16
	SNzones        uint16
	SImapBlocks    uint16
	SZmapBlocks    uint16
	SFirstdatazone uint16
	SLogZoneSize   uint16
	SMaxSize       uint32
	SMagic         uint16
}

type DirEntry struct {
	Inode uint16
	Name  [NAME_LEN]byte
}

var ROOT_DEV int
