package fs

import (
	. "google.golang.org/adk/v2/include"
)

func BlockWrite(dev int, pos *int64, buf []byte, count int) int {
	block := int(*pos >> BLOCK_SIZE_BITS)
	offset := int(*pos) & (BLOCK_SIZE - 1)
	written := 0
	bufOff := 0

	for count > 0 {
		chars := BLOCK_SIZE - offset
		if chars > count { chars = count }
		var bh *BufferHead
		if chars == BLOCK_SIZE {
			bh = Getblk(dev, block)
		} else {
			bh = Breada(dev, block, block+1, block+2, -1)
		}
		block++
		if bh == nil {
			if written != 0 { return written }
			return -EIO
		}
		p := offset
		offset = 0
		*pos += int64(chars)
		written += chars
		count -= chars
		for i := 0; i < chars; i++ {
			if p < len(bh.BData) && bufOff < len(buf) {
				bh.BData[p] = buf[bufOff]
			}
			p++; bufOff++
		}
		bh.BDirt = 1
		Brelse(bh)
	}
	return written
}

func BlockRead(dev int, pos *int64, buf []byte, count int) int {
	block := int(*pos >> BLOCK_SIZE_BITS)
	offset := int(*pos) & (BLOCK_SIZE - 1)
	readn := 0
	bufOff := 0

	for count > 0 {
		chars := BLOCK_SIZE - offset
		if chars > count { chars = count }
		bh := Breada(dev, block, block+1, block+2, -1)
		if bh == nil {
			if readn != 0 { return readn }
			return -EIO
		}
		block++
		p := offset
		offset = 0
		*pos += int64(chars)
		readn += chars
		count -= chars
		for i := 0; i < chars; i++ {
			if bufOff < len(buf) && p < len(bh.BData) {
				buf[bufOff] = bh.BData[p]
			}
			p++; bufOff++
		}
		Brelse(bh)
	}
	return readn
}
