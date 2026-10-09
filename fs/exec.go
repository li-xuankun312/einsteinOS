package fs

import (
	"encoding/binary"
	"log"
	"strings"

	. "google.golang.org/adk/v2/include"
)

const (
	MAX_ARG_PAGES = 32
	S_ISUID       = 04000
	S_ISGID       = 02000
	ZMAGIC        = 0x10B
	N_TXTOFF_VAL  = BLOCK_SIZE
)

type ExecHeader struct {
	AMagic  uint32
	AText   uint32
	AData   uint32
	ABss    uint32
	ASyms   uint32
	AEntry  uint32
	ATrsize uint32
	ADrsize uint32
}

const EXEC_HEADER_SIZE = 32

var (
	freePageTablesFn func(uint32, uint32)
	putPageFn        func(uint32, uint32)
	sysExitFn        func(int)
	sysCloseFn       func(int) int
)

func SetFreePageTables(fn func(uint32, uint32)) { freePageTablesFn = fn }
func SetPutPage(fn func(uint32, uint32))        { putPageFn = fn }
func SetSysExit(fn func(int))                   { sysExitFn = fn }
func SetSysClose(fn func(int) int)              { sysCloseFn = fn }

func ReadExecHeader(data []byte) ExecHeader {
	var ex ExecHeader
	if len(data) < EXEC_HEADER_SIZE {
		return ex
	}
	ex.AMagic = binary.LittleEndian.Uint32(data[0:])
	ex.AText = binary.LittleEndian.Uint32(data[4:])
	ex.AData = binary.LittleEndian.Uint32(data[8:])
	ex.ABss = binary.LittleEndian.Uint32(data[12:])
	ex.ASyms = binary.LittleEndian.Uint32(data[16:])
	ex.AEntry = binary.LittleEndian.Uint32(data[20:])
	ex.ATrsize = binary.LittleEndian.Uint32(data[24:])
	ex.ADrsize = binary.LittleEndian.Uint32(data[28:])
	return ex
}

func WriteExecHeader(ex *ExecHeader) []byte {
	buf := make([]byte, EXEC_HEADER_SIZE)
	binary.LittleEndian.PutUint32(buf[0:], ex.AMagic)
	binary.LittleEndian.PutUint32(buf[4:], ex.AText)
	binary.LittleEndian.PutUint32(buf[8:], ex.AData)
	binary.LittleEndian.PutUint32(buf[12:], ex.ABss)
	binary.LittleEndian.PutUint32(buf[16:], ex.ASyms)
	binary.LittleEndian.PutUint32(buf[20:], ex.AEntry)
	binary.LittleEndian.PutUint32(buf[24:], ex.ATrsize)
	binary.LittleEndian.PutUint32(buf[28:], ex.ADrsize)
	return buf
}

func ValidateExecHeader(ex *ExecHeader, fileSize uint32) int {
	if ex.AMagic != ZMAGIC {
		return -ENOEXEC
	}
	if ex.ATrsize != 0 || ex.ADrsize != 0 {
		return -ENOEXEC
	}
	if ex.AText+ex.AData+ex.ABss > 0x3000000 {
		return -ENOMEM
	}
	if fileSize < ex.AText+ex.AData+ex.ASyms+N_TXTOFF_VAL {
		return -ENOEXEC
	}
	return 0
}

func MakeAoutBinary(textSize, dataSize, bssSize, entry uint32) []byte {
	hdr := ExecHeader{
		AMagic:  ZMAGIC,
		AText:   textSize,
		AData:   dataSize,
		ABss:    bssSize,
		AEntry:  entry,
		ATrsize: 0,
		ADrsize: 0,
		ASyms:   0,
	}
	header := WriteExecHeader(&hdr)
	padding := make([]byte, N_TXTOFF_VAL-EXEC_HEADER_SIZE)
	text := make([]byte, textSize)
	data := make([]byte, dataSize)
	var out []byte
	out = append(out, header...)
	out = append(out, padding...)
	out = append(out, text...)
	out = append(out, data...)
	return out
}

var argPages [MAX_ARG_PAGES][]byte

func copyStrings(argv []string, page *[MAX_ARG_PAGES][]byte, p uint32) uint32 {
	for i := len(argv) - 1; i >= 0; i-- {
		s := argv[i]
		slen := uint32(len(s) + 1)
		if p < slen {
			return 0
		}
		p -= slen
		pageNr := int(p / PAGE_SIZE)
		offset := int(p % PAGE_SIZE)
		if pageNr >= MAX_ARG_PAGES {
			return 0
		}
		if page[pageNr] == nil {
			page[pageNr] = make([]byte, PAGE_SIZE)
		}
		for j := 0; j < len(s); j++ {
			idx := offset + j
			pg := pageNr + idx/PAGE_SIZE
			off := idx % PAGE_SIZE
			if pg < MAX_ARG_PAGES {
				if page[pg] == nil {
					page[pg] = make([]byte, PAGE_SIZE)
				}
				page[pg][off] = s[j]
			}
		}
		termIdx := offset + len(s)
		termPg := pageNr + termIdx/PAGE_SIZE
		termOff := termIdx % PAGE_SIZE
		if termPg < MAX_ARG_PAGES {
			if page[termPg] == nil {
				page[termPg] = make([]byte, PAGE_SIZE)
			}
			page[termPg][termOff] = 0
		}
	}
	return p
}

func changeLdt(textSize uint32, p *TaskStruct) {
	codeLimit := textSize
	if codeLimit < 0x4000000 {
		codeLimit = 0x4000000
	}
	dataLimit := uint32(0x4000000)
	SetLimit(&p.Ldt[1], codeLimit)
	SetLimit(&p.Ldt[2], dataLimit)
	newBase := uint32(p.Pid) * 0x4000000
	SetBase(&p.Ldt[1], newBase)
	SetBase(&p.Ldt[2], newBase)
	p.StartCode = newBase
}

func DoExecve(filename string, argv []string, envp []string) int {
	var page [MAX_ARG_PAGES][]byte
	var bh *BufferHead
	var ex ExecHeader
	var eUid uint16
	var eGid uint16
	argc := len(argv)
	_ = argc
	p := uint32(PAGE_SIZE*MAX_ARG_PAGES - 4)

	inode := Namei(filename)
	if inode == nil {
		return -ENOENT
	}

	shBang := false

	var i int
	var block int
restart_interp:
	if !S_ISREG(inode.IMode) {
		Iput(inode)
		goto exec_error1
	}
	i = int(inode.IMode)
	eUid = Current.Euid
	eGid = Current.Egid
	if i&S_ISUID != 0 {
		eUid = inode.IUid
	}
	if i&S_ISGID != 0 {
		eGid = uint16(inode.IGid)
	}
	if Current.Euid == inode.IUid {
		i >>= 6
	} else if uint8(Current.Egid) == inode.IGid {
		i >>= 3
	}
	if i&1 == 0 && !((inode.IMode&0111) != 0 && suser()) {
		Iput(inode)
		goto exec_error1
	}

	block = Bmap(inode, 0)
	if block == 0 {
		Iput(inode)
		goto exec_error1
	}
	bh = Bread(int(inode.IDev), block)
	if bh == nil {
		Iput(inode)
		goto exec_error1
	}
	ex = ReadExecHeader(bh.BData)

	if len(bh.BData) >= 2 && bh.BData[0] == '#' && bh.BData[1] == '!' && !shBang {
		end := strings.IndexByte(string(bh.BData[2:]), '\n')
		if end < 0 {
			end = 1022
		}
		if end > 1022 {
			end = 1022
		}
		line := strings.TrimSpace(string(bh.BData[2 : 2+end]))
		Brelse(bh)
		Iput(inode)
		if line == "" {
			goto exec_error1
		}
		parts := strings.Fields(line)
		interp := parts[0]
		if !shBang {
			shBang = true
			p = copyStrings(envp, &page, p)
			if len(argv) > 1 {
				p = copyStrings(argv[1:], &page, p)
			}
		}
		newArgv := []string{interp}
		if len(parts) > 1 {
			newArgv = append(newArgv, parts[1:]...)
		}
		newArgv = append(newArgv, filename)
		if len(argv) > 1 {
			newArgv = append(newArgv, argv[1:]...)
		}
		argv = newArgv

		inode = Namei(interp)
		if inode == nil {
			goto exec_error1
		}
		goto restart_interp
	}

	Brelse(bh)

	if ret := ValidateExecHeader(&ex, inode.ISize); ret != 0 {
		Iput(inode)
		return ret
	}

	if !shBang {
		p = copyStrings(envp, &page, p)
		p = copyStrings(argv, &page, p)
		if p == 0 {
			Iput(inode)
			goto exec_error1
		}
	}

	if freePageTablesFn != nil {
		freePageTablesFn(
			GetBase(Current.Ldt[1]),
			uint32(GetLimit(Current.Ldt[1])),
		)
		freePageTablesFn(
			GetBase(Current.Ldt[2]),
			uint32(GetLimit(Current.Ldt[2])),
		)
	}

	if Current.Executable != nil {
		Iput(Current.Executable)
	}
	Current.Executable = inode

	for j := 0; j < 32; j++ {
		Current.Sigaction[j].SaHandler = nil
		Current.Sigaction[j].SaHandlerType = SIG_HANDLER_DEFAULT
	}
	for j := 0; j < NR_OPEN; j++ {
		if (Current.CloseOnExec>>j)&1 != 0 {
			if sysCloseFn != nil {
				sysCloseFn(j)
			}
		}
	}
	Current.CloseOnExec = 0
	Current.UsedMath = 0

	changeLdt(ex.AText, Current)

	Current.Brk = ex.ABss + ex.AData + ex.AText
	Current.EndData = ex.AData + ex.AText
	Current.EndCode = ex.AText
	Current.StartStack = p & 0xFFFFF000
	Current.Euid = eUid
	Current.Egid = eGid

	for j := 0; j < MAX_ARG_PAGES; j++ {
		if page[j] != nil && putPageFn != nil {
			pg := getExecFreePage()
			if pg != 0 {
				copy(physMemSlice(pg, PAGE_SIZE), page[j])
				putPageFn(pg, Current.StartStack+uint32(j)*PAGE_SIZE)
			}
		}
	}

	log.Printf("fs: exec %s: text=%d data=%d bss=%d entry=%#x",
		filename, ex.AText, ex.AData, ex.ABss, ex.AEntry)
	return 0

exec_error1:
	log.Printf("fs: exec error for %s", filename)
	return -ENOEXEC
}

func physMemSlice(addr uint32, size uint32) []byte {
	return make([]byte, size)
}

func getExecFreePage() uint32 {
	if getFrePageFn != nil {
		return getFrePageFn()
	}
	return 0
}
