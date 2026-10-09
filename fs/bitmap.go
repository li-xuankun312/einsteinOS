package fs

import (
	"log"

	. "google.golang.org/adk/v2/include"
)

func FreeBlock(dev, block int) {
	sb := GetSuper(dev)
	if sb == nil {
		log.Printf("fs: trying to free block on nonexistent device")
		return
	}
	if block < int(sb.SFirstdatazone) || block >= int(sb.SNzones) {
		log.Printf("fs: trying to free block not in datazone")
		return
	}
	bh := GetHashTable(dev, block)
	if bh != nil {
		if bh.BCount != 1 {
			log.Printf("fs: trying to free block (%04x:%d), count=%d",
				dev, block, bh.BCount)
			return
		}
		bh.BDirt = 0
		bh.BUptodate = 0
		Brelse(bh)
	}
	block -= int(sb.SFirstdatazone) - 1
	zmapIdx := block / 8192
	if zmapIdx < Z_MAP_SLOTS && sb.SZmap[zmapIdx] != nil {
		if clearBit(block&8191, sb.SZmap[zmapIdx].BData) {
			log.Printf("fs: free_block: bit already cleared")
		}
		sb.SZmap[zmapIdx].BDirt = 1
	}
}

func NewBlock(dev int) int {
	sb := GetSuper(dev)
	if sb == nil {
		log.Printf("fs: trying to get new block from nonexistent device")
		return 0
	}
	j := 8192
	var bh *BufferHead
	var i int
	for i = 0; i < 8; i++ {
		bh = sb.SZmap[i]
		if bh != nil {
			j = findFirstZero(bh.BData)
			if j < 8192 { break }
		}
	}
	if i >= 8 || bh == nil || j >= 8192 { return 0 }
	if setBit(j, bh.BData) {
		log.Printf("fs: new_block: bit already set")
	}
	bh.BDirt = 1
	j += i*8192 + int(sb.SFirstdatazone) - 1
	if j >= int(sb.SNzones) { return 0 }
	newbh := Getblk(dev, j)
	if newbh == nil {
		log.Printf("fs: new_block: cannot get block")
		return 0
	}
	if newbh.BCount != 1 {
		log.Printf("fs: new block: count is != 1")
	}
	clearBlock(newbh.BData)
	newbh.BUptodate = 1
	newbh.BDirt = 1
	Brelse(newbh)
	return j
}

func FreeInode(inode *MInode) {
	if inode == nil { return }
	if inode.IDev == 0 {
		*inode = MInode{}
		return
	}
	if inode.ICount > 1 {
		log.Printf("fs: trying to free inode with count=%d", inode.ICount)
		return
	}
	if inode.INlinks != 0 {
		log.Printf("fs: trying to free inode with links")
		return
	}
	sb := GetSuper(int(inode.IDev))
	if sb == nil {
		log.Printf("fs: trying to free inode on nonexistent device")
		return
	}
	if inode.INum < 1 || inode.INum > sb.SNinodes {
		log.Printf("fs: trying to free inode 0 or nonexistent inode")
		return
	}
	imapIdx := int(inode.INum) >> 13
	if imapIdx >= I_MAP_SLOTS || sb.SImap[imapIdx] == nil {
		log.Printf("fs: nonexistent imap in superblock")
		return
	}
	if clearBit(int(inode.INum)&8191, sb.SImap[imapIdx].BData) {
		log.Printf("fs: free_inode: bit already cleared")
	}
	sb.SImap[imapIdx].BDirt = 1
	*inode = MInode{}
}

func NewInode(dev int) *MInode {
	inode := GetEmptyInode()
	if inode == nil { return nil }
	sb := GetSuper(dev)
	if sb == nil {
		log.Printf("fs: new_inode with unknown device")
		return nil
	}
	j := 8192
	var bh *BufferHead
	var i int
	for i = 0; i < 8; i++ {
		bh = sb.SImap[i]
		if bh != nil {
			j = findFirstZero(bh.BData)
			if j < 8192 { break }
		}
	}
	if bh == nil || j >= 8192 || j+i*8192 > int(sb.SNinodes) {
		Iput(inode)
		return nil
	}
	if setBit(j, bh.BData) {
		log.Printf("fs: new_inode: bit already set")
	}
	bh.BDirt = 1
	inode.ICount = 1
	inode.INlinks = 1
	inode.IDev = uint16(dev)
	inode.IUid = Current.Euid
	inode.IGid = uint8(Current.Egid)
	inode.IDirt = 1
	inode.INum = uint16(j + i*8192)
	ct := uint32(CURRENT_TIME())
	inode.IMtime = ct
	inode.IAtime = ct
	inode.ICtime = ct
	return inode
}
