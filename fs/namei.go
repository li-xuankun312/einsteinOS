package fs

import (
	"encoding/binary"
	"log"

	. "google.golang.org/adk/v2/include"
)

const (
	O_ACCMODE  = 03
	O_WRONLY   = 01
	O_RDONLY   = 00
	O_RDWR     = 02
	O_EXCL     = 0200
	MAY_EXEC   = 1
	MAY_WRITE  = 2
	MAY_READ   = 4
	I_REGULAR  = 0100000
	I_DIRECTORY = 0040000
	S_ISVTX    = 01000
)

const DIR_ENTRY_SIZE = 16
const DIR_ENTRIES_PER_BLOCK_V = BLOCK_SIZE / DIR_ENTRY_SIZE

func accMode(flag int) int {
	table := []int{4, 2, 6, 0xFF}
	return table[flag&O_ACCMODE]
}

func permission(inode *MInode, mask int) bool {
	mode := int(inode.IMode)
	if inode.IDev != 0 && inode.INlinks == 0 { return false }
	if Current.Euid == inode.IUid {
		mode >>= 6
	} else if uint8(Current.Egid) == inode.IGid {
		mode >>= 3
	}
	if (mode&mask&0007) == mask || suser() { return true }
	return false
}

func match(length int, name string, de *GoDirEntry) bool {
	if de == nil || de.Inode == 0 || length > NAME_LEN { return false }
	if length < NAME_LEN && length < len(de.Name) && de.Name[length] != 0 {
		return false
	}
	for i := 0; i < length; i++ {
		if i >= len(name) || i >= len(de.Name) { return false }
		if name[i] != de.Name[i] { return false }
	}
	return true
}

type GoDirEntry struct {
	Inode uint16
	Name  [NAME_LEN]byte
}

func readDirEntry(data []byte, offset int) *GoDirEntry {
	if offset+DIR_ENTRY_SIZE > len(data) { return nil }
	de := &GoDirEntry{}
	de.Inode = binary.LittleEndian.Uint16(data[offset:])
	copy(de.Name[:], data[offset+2:offset+DIR_ENTRY_SIZE])
	return de
}

func writeDirEntry(data []byte, offset int, de *GoDirEntry) {
	if offset+DIR_ENTRY_SIZE > len(data) { return }
	binary.LittleEndian.PutUint16(data[offset:], de.Inode)
	copy(data[offset+2:offset+DIR_ENTRY_SIZE], de.Name[:])
}

func findEntry(dir **MInode, name string, namelen int) (*BufferHead, *GoDirEntry, int) {
	if namelen > NAME_LEN { namelen = NAME_LEN }
	entries := int((*dir).ISize) / DIR_ENTRY_SIZE
	if namelen == 0 { return nil, nil, 0 }
	if namelen == 2 && name[0] == '.' && name[1] == '.' {
		if *dir == Current.Root {
			namelen = 1
		} else if (*dir).INum == ROOT_INO {
			sb := GetSuper(int((*dir).IDev))
			if sb != nil && sb.SImount != nil {
				Iput(*dir)
				*dir = sb.SImount
				(*dir).ICount++
			}
		}
	}
	if (*dir).IZone[0] == 0 { return nil, nil, 0 }
	bh := Bread(int((*dir).IDev), int((*dir).IZone[0]))
	if bh == nil { return nil, nil, 0 }
	deOff := 0
	for i := 0; i < entries; i++ {
		if deOff >= BLOCK_SIZE {
			Brelse(bh)
			bh = nil
			block := Bmap(*dir, i/DIR_ENTRIES_PER_BLOCK_V)
			if block == 0 || (func() bool { bh = Bread(int((*dir).IDev), block); return bh == nil })() {
				i += DIR_ENTRIES_PER_BLOCK_V - 1
				deOff = 0
				continue
			}
			deOff = 0
		}
		de := readDirEntry(bh.BData, deOff)
		actualName := name
		if namelen < len(name) { actualName = name[:namelen] }
		if match(namelen, actualName, de) {
			return bh, de, deOff
		}
		deOff += DIR_ENTRY_SIZE
	}
	Brelse(bh)
	return nil, nil, 0
}

func addEntry(dir *MInode, name string, namelen int) (*BufferHead, *GoDirEntry, int) {
	if namelen > NAME_LEN { namelen = NAME_LEN }
	if namelen == 0 { return nil, nil, 0 }
	if dir.IZone[0] == 0 { return nil, nil, 0 }
	bh := Bread(int(dir.IDev), int(dir.IZone[0]))
	if bh == nil { return nil, nil, 0 }
	i := 0
	deOff := 0
	for {
		if deOff >= BLOCK_SIZE {
			Brelse(bh)
			bh = nil
			block := CreateBlock(dir, i/DIR_ENTRIES_PER_BLOCK_V)
			if block == 0 { return nil, nil, 0 }
			bh = Bread(int(dir.IDev), block)
			if bh == nil { i += DIR_ENTRIES_PER_BLOCK_V; deOff = 0; continue }
			deOff = 0
		}
		if uint32(i)*DIR_ENTRY_SIZE >= dir.ISize {
			de := &GoDirEntry{Inode: 0}
			writeDirEntry(bh.BData, deOff, de)
			dir.ISize = uint32((i + 1) * DIR_ENTRY_SIZE)
			dir.IDirt = 1
			dir.ICtime = uint32(CURRENT_TIME())
		}
		de := readDirEntry(bh.BData, deOff)
		if de != nil && de.Inode == 0 {
			dir.IMtime = uint32(CURRENT_TIME())
			for j := 0; j < NAME_LEN; j++ {
				if j < namelen && j < len(name) {
					de.Name[j] = name[j]
				} else {
					de.Name[j] = 0
				}
			}
			writeDirEntry(bh.BData, deOff, de)
			bh.BDirt = 1
			return bh, de, deOff
		}
		deOff += DIR_ENTRY_SIZE
		i++
	}
}

func getDir(pathname string) (*MInode, string) {
	if Current.Root == nil || Current.Root.ICount == 0 {
		log.Printf("fs: No root inode"); return nil, ""
	}
	if Current.Pwd == nil || Current.Pwd.ICount == 0 {
		log.Printf("fs: No cwd inode"); return nil, ""
	}
	if len(pathname) == 0 { return nil, "" }
	var inode *MInode
	idx := 0
	if pathname[0] == '/' {
		inode = Current.Root
		idx++
	} else {
		inode = Current.Pwd
	}
	inode.ICount++
	for {
		thisname := idx
		if !S_ISDIR(inode.IMode) || !permission(inode, MAY_EXEC) {
			Iput(inode); return nil, ""
		}
		namelen := 0
		for idx < len(pathname) && pathname[idx] != '/' {
			idx++; namelen++
		}
		if idx >= len(pathname) || pathname[idx] != '/' {
			return inode, pathname[thisname:]
		}
		idx++
		bh, de, _ := findEntry(&inode, pathname[thisname:thisname+namelen], namelen)
		if bh == nil { Iput(inode); return nil, "" }
		inr := int(de.Inode)
		idev := int(inode.IDev)
		Brelse(bh)
		Iput(inode)
		inode = Iget(idev, inr)
		if inode == nil { return nil, "" }
	}
}

func dirNamei(pathname string) (*MInode, int, string) {
	dir, remaining := getDir(pathname)
	if dir == nil { return nil, 0, "" }
	basename := remaining
	for i := 0; i < len(remaining); i++ {
		if remaining[i] == '/' {
			basename = remaining[i+1:]
		}
	}
	return dir, len(basename), basename
}

func Namei(pathname string) *MInode {
	dir, namelen, basename := dirNamei(pathname)
	if dir == nil { return nil }
	if namelen == 0 { return dir }
	bh, de, _ := findEntry(&dir, basename, namelen)
	if bh == nil { Iput(dir); return nil }
	inr := int(de.Inode)
	dev := int(dir.IDev)
	Brelse(bh)
	Iput(dir)
	inode := Iget(dev, inr)
	if inode != nil {
		inode.IAtime = uint32(CURRENT_TIME())
		inode.IDirt = 1
	}
	return inode
}

func OpenNamei(pathname string, flag, mode int, resInode **MInode) int {
	if (flag&O_TRUNC) != 0 && (flag&O_ACCMODE) == 0 {
		flag |= O_WRONLY
	}
	mode &= 0777 & ^int(Current.Umask)
	mode |= I_REGULAR
	dir, namelen, basename := dirNamei(pathname)
	if dir == nil { return -ENOENT }
	if namelen == 0 {
		if flag&(O_ACCMODE|O_CREAT|O_TRUNC) == 0 {
			*resInode = dir; return 0
		}
		Iput(dir); return -EISDIR
	}
	bh, de, _ := findEntry(&dir, basename, namelen)
	if bh == nil {
		if flag&O_CREAT == 0 { Iput(dir); return -ENOENT }
		if !permission(dir, MAY_WRITE) { Iput(dir); return -EACCES }
		inode := NewInode(int(dir.IDev))
		if inode == nil { Iput(dir); return -ENOSPC }
		inode.IUid = Current.Euid
		inode.IMode = uint16(mode)
		inode.IDirt = 1
		bh2, de2, deOff2 := addEntry(dir, basename, namelen)
		if bh2 == nil {
			inode.INlinks--; Iput(inode); Iput(dir); return -ENOSPC
		}
		de2.Inode = inode.INum
		writeDirEntry(bh2.BData, deOff2, de2)
		bh2.BDirt = 1; Brelse(bh2); Iput(dir)
		*resInode = inode; return 0
	}
	inr := int(de.Inode)
	dev := int(dir.IDev)
	Brelse(bh); Iput(dir)
	if flag&O_EXCL != 0 { return -EEXIST }
	inode := Iget(dev, inr)
	if inode == nil { return -EACCES }
	if (S_ISDIR(inode.IMode) && (flag&O_ACCMODE) != 0) ||
		!permission(inode, accMode(flag)) {
		Iput(inode); return -EPERM
	}
	inode.IAtime = uint32(CURRENT_TIME())
	if flag&O_TRUNC != 0 { Truncate(inode) }
	*resInode = inode
	return 0
}

func SysMknod(filename string, mode, dev int) int {
	if !suser() { return -EPERM }
	dir, namelen, basename := dirNamei(filename)
	if dir == nil { return -ENOENT }
	if namelen == 0 { Iput(dir); return -ENOENT }
	if !permission(dir, MAY_WRITE) { Iput(dir); return -EPERM }
	bh, _, _ := findEntry(&dir, basename, namelen)
	if bh != nil { Brelse(bh); Iput(dir); return -EEXIST }
	inode := NewInode(int(dir.IDev))
	if inode == nil { Iput(dir); return -ENOSPC }
	inode.IMode = uint16(mode)
	if S_ISBLK(uint16(mode)) || S_ISCHR(uint16(mode)) {
		inode.IZone[0] = uint16(dev)
	}
	ct := uint32(CURRENT_TIME())
	inode.IMtime = ct; inode.IAtime = ct; inode.IDirt = 1
	bh, de, deOff := addEntry(dir, basename, namelen)
	if bh == nil { Iput(dir); inode.INlinks = 0; Iput(inode); return -ENOSPC }
	de.Inode = inode.INum
	writeDirEntry(bh.BData, deOff, de)
	bh.BDirt = 1; Iput(dir); Iput(inode); Brelse(bh)
	return 0
}

func SysMkdir(pathname string, mode int) int {
	if !suser() { return -EPERM }
	dir, namelen, basename := dirNamei(pathname)
	if dir == nil { return -ENOENT }
	if namelen == 0 { Iput(dir); return -ENOENT }
	if !permission(dir, MAY_WRITE) { Iput(dir); return -EPERM }
	bh, _, _ := findEntry(&dir, basename, namelen)
	if bh != nil { Brelse(bh); Iput(dir); return -EEXIST }
	inode := NewInode(int(dir.IDev))
	if inode == nil { Iput(dir); return -ENOSPC }
	inode.ISize = 32; inode.IDirt = 1
	ct := uint32(CURRENT_TIME())
	inode.IMtime = ct; inode.IAtime = ct
	nb := NewBlock(int(inode.IDev))
	if nb == 0 { Iput(dir); inode.INlinks--; Iput(inode); return -ENOSPC }
	inode.IZone[0] = uint16(nb); inode.IDirt = 1
	dirBlock := Bread(int(inode.IDev), nb)
	if dirBlock == nil {
		Iput(dir); FreeBlock(int(inode.IDev), nb); inode.INlinks--; Iput(inode); return -ERROR
	}
	dot := &GoDirEntry{Inode: inode.INum}
	dot.Name[0] = '.'
	writeDirEntry(dirBlock.BData, 0, dot)
	dotdot := &GoDirEntry{Inode: dir.INum}
	dotdot.Name[0] = '.'; dotdot.Name[1] = '.'
	writeDirEntry(dirBlock.BData, DIR_ENTRY_SIZE, dotdot)
	inode.INlinks = 2; dirBlock.BDirt = 1; Brelse(dirBlock)
	inode.IMode = uint16(I_DIRECTORY | (mode & 0777 & ^int(Current.Umask)))
	inode.IDirt = 1
	bh, de, deOff := addEntry(dir, basename, namelen)
	if bh == nil {
		Iput(dir); FreeBlock(int(inode.IDev), int(inode.IZone[0]))
		inode.INlinks = 0; Iput(inode); return -ENOSPC
	}
	de.Inode = inode.INum
	writeDirEntry(bh.BData, deOff, de)
	bh.BDirt = 1; dir.INlinks++; dir.IDirt = 1
	Iput(dir); Iput(inode); Brelse(bh)
	return 0
}

func emptyDir(inode *MInode) bool {
	nEntries := int(inode.ISize) / DIR_ENTRY_SIZE
	if nEntries < 2 || inode.IZone[0] == 0 { return false }
	bh := Bread(int(inode.IDev), int(inode.IZone[0]))
	if bh == nil { return false }
	de0 := readDirEntry(bh.BData, 0)
	de1 := readDirEntry(bh.BData, DIR_ENTRY_SIZE)
	if de0 == nil || de1 == nil { Brelse(bh); return false }
	if de0.Inode != inode.INum || de1.Inode == 0 { Brelse(bh); return false }
	if de0.Name[0] != '.' || (de1.Name[0] != '.' || de1.Name[1] != '.') {
		Brelse(bh); return false
	}
	deOff := 2 * DIR_ENTRY_SIZE
	for nr := 2; nr < nEntries; nr++ {
		if deOff >= BLOCK_SIZE {
			Brelse(bh)
			block := Bmap(inode, nr/DIR_ENTRIES_PER_BLOCK_V)
			if block == 0 { nr += DIR_ENTRIES_PER_BLOCK_V - 1; deOff = 0; continue }
			bh = Bread(int(inode.IDev), block)
			if bh == nil { return false }
			deOff = 0
		}
		de := readDirEntry(bh.BData, deOff)
		if de != nil && de.Inode != 0 { Brelse(bh); return false }
		deOff += DIR_ENTRY_SIZE
	}
	Brelse(bh)
	return true
}

func SysRmdir(name string) int {
	if !suser() { return -EPERM }
	dir, namelen, basename := dirNamei(name)
	if dir == nil { return -ENOENT }
	if namelen == 0 { Iput(dir); return -ENOENT }
	if !permission(dir, MAY_WRITE) { Iput(dir); return -EPERM }
	bh, de, deOff := findEntry(&dir, basename, namelen)
	if bh == nil { Iput(dir); return -ENOENT }
	inode := Iget(int(dir.IDev), int(de.Inode))
	if inode == nil { Iput(dir); Brelse(bh); return -EPERM }
	if (dir.IMode&S_ISVTX) != 0 && Current.Euid != 0 && inode.IUid != Current.Euid {
		Iput(dir); Iput(inode); Brelse(bh); return -EPERM
	}
	if inode.IDev != dir.IDev || inode.ICount > 1 {
		Iput(dir); Iput(inode); Brelse(bh); return -EPERM
	}
	if inode == dir { Iput(inode); Iput(dir); Brelse(bh); return -EPERM }
	if !S_ISDIR(inode.IMode) { Iput(inode); Iput(dir); Brelse(bh); return -ENOTDIR }
	if !emptyDir(inode) { Iput(inode); Iput(dir); Brelse(bh); return -ENOTEMPTY }
	if inode.INlinks != 2 { log.Printf("fs: empty directory has nlink!=2 (%d)", inode.INlinks) }
	de.Inode = 0
	writeDirEntry(bh.BData, deOff, de)
	bh.BDirt = 1; Brelse(bh)
	inode.INlinks = 0; inode.IDirt = 1
	dir.INlinks--
	ct := uint32(CURRENT_TIME())
	dir.ICtime = ct; dir.IMtime = ct; dir.IDirt = 1
	Iput(dir); Iput(inode)
	return 0
}

func SysUnlink(name string) int {
	dir, namelen, basename := dirNamei(name)
	if dir == nil { return -ENOENT }
	if namelen == 0 { Iput(dir); return -ENOENT }
	if !permission(dir, MAY_WRITE) { Iput(dir); return -EPERM }
	bh, de, deOff := findEntry(&dir, basename, namelen)
	if bh == nil { Iput(dir); return -ENOENT }
	inode := Iget(int(dir.IDev), int(de.Inode))
	if inode == nil { Iput(dir); Brelse(bh); return -ENOENT }
	if (dir.IMode&S_ISVTX) != 0 && !suser() &&
		Current.Euid != inode.IUid && Current.Euid != dir.IUid {
		Iput(dir); Iput(inode); Brelse(bh); return -EPERM
	}
	if S_ISDIR(inode.IMode) { Iput(inode); Iput(dir); Brelse(bh); return -EPERM }
	if inode.INlinks == 0 {
		log.Printf("fs: Deleting nonexistent file (%04x:%d), %d", inode.IDev, inode.INum, inode.INlinks)
		inode.INlinks = 1
	}
	de.Inode = 0
	writeDirEntry(bh.BData, deOff, de)
	bh.BDirt = 1; Brelse(bh)
	inode.INlinks--; inode.IDirt = 1
	inode.ICtime = uint32(CURRENT_TIME())
	Iput(inode); Iput(dir)
	return 0
}

func SysLink(oldname, newname string) int {
	oldinode := Namei(oldname)
	if oldinode == nil { return -ENOENT }
	if S_ISDIR(oldinode.IMode) { Iput(oldinode); return -EPERM }
	dir, namelen, basename := dirNamei(newname)
	if dir == nil { Iput(oldinode); return -EACCES }
	if namelen == 0 { Iput(oldinode); Iput(dir); return -EPERM }
	if dir.IDev != oldinode.IDev { Iput(dir); Iput(oldinode); return -EXDEV }
	if !permission(dir, MAY_WRITE) { Iput(dir); Iput(oldinode); return -EACCES }
	bh, _, _ := findEntry(&dir, basename, namelen)
	if bh != nil { Brelse(bh); Iput(dir); Iput(oldinode); return -EEXIST }
	bh, de, deOff := addEntry(dir, basename, namelen)
	if bh == nil { Iput(dir); Iput(oldinode); return -ENOSPC }
	de.Inode = oldinode.INum
	writeDirEntry(bh.BData, deOff, de)
	bh.BDirt = 1; Brelse(bh); Iput(dir)
	oldinode.INlinks++
	oldinode.ICtime = uint32(CURRENT_TIME())
	oldinode.IDirt = 1; Iput(oldinode)
	return 0
}
