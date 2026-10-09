package fs

import (
	. "google.golang.org/adk/v2/include"
)

var PipeBuffers = make(map[*MInode][]byte)

func pipeSize(inode *MInode) int {
	return int((inode.IZone[0] - inode.IZone[1]) & (PAGE_SIZE - 1))
}

func ReadPipe(inode *MInode, buf []byte, count int) int {
	readn := 0
	pipeBuf := PipeBuffers[inode]
	if pipeBuf == nil { return 0 }

	for count > 0 {
		for {
			size := pipeSize(inode)
			if size != 0 { break }
			if wakeUpFn != nil { wakeUpFn(&inode.IWait) }
			if inode.ICount != 2 { return readn }
			if sleepOnFn != nil { sleepOnFn(&inode.IWait) }
		}
		size := pipeSize(inode)
		chars := PAGE_SIZE - int(inode.IZone[1])
		if chars > count { chars = count }
		if chars > size { chars = size }
		count -= chars
		readn += chars
		tail := int(inode.IZone[1])
		inode.IZone[1] = uint16((int(inode.IZone[1]) + chars) & (PAGE_SIZE - 1))
		for i := 0; i < chars; i++ {
			if readn-chars+i < len(buf) {
				buf[readn-chars+i] = pipeBuf[tail]
			}
			tail++
		}
	}
	if wakeUpFn != nil { wakeUpFn(&inode.IWait) }
	return readn
}

func WritePipe(inode *MInode, buf []byte, count int) int {
	written := 0
	pipeBuf := PipeBuffers[inode]
	if pipeBuf == nil { return -1 }

	for count > 0 {
		for {
			size := (PAGE_SIZE - 1) - pipeSize(inode)
			if size != 0 { break }
			if wakeUpFn != nil { wakeUpFn(&inode.IWait) }
			if inode.ICount != 2 {
				Current.Signal |= 1 << (SIGPIPE - 1)
				if written != 0 { return written }
				return -1
			}
			if sleepOnFn != nil { sleepOnFn(&inode.IWait) }
		}
		size := (PAGE_SIZE - 1) - pipeSize(inode)
		chars := PAGE_SIZE - int(inode.IZone[0])
		if chars > count { chars = count }
		if chars > size { chars = size }
		count -= chars
		written += chars
		head := int(inode.IZone[0])
		inode.IZone[0] = uint16((int(inode.IZone[0]) + chars) & (PAGE_SIZE - 1))
		for i := 0; i < chars; i++ {
			if written-chars+i < len(buf) {
				pipeBuf[head] = buf[written-chars+i]
			}
			head++
		}
	}
	if wakeUpFn != nil { wakeUpFn(&inode.IWait) }
	return written
}

func SysPipe(fildes []int) int {
	var f [2]*File
	var fd [2]int

	j := 0
	for i := 0; j < 2 && i < NR_FILE; i++ {
		if FileTable[i].FCount == 0 {
			f[j] = &FileTable[i]
			f[j].FCount++
			j++
		}
	}
	if j == 1 { f[0].FCount = 0 }
	if j < 2 { return -1 }

	j = 0
	for i := 0; j < 2 && i < NR_OPEN; i++ {
		if Current.Filp[i] == nil {
			fd[j] = i
			Current.Filp[i] = f[j]
			j++
		}
	}
	if j == 1 { Current.Filp[fd[0]] = nil }
	if j < 2 {
		f[0].FCount = 0; f[1].FCount = 0
		return -1
	}

	inode := GetPipeInode()
	if inode == nil {
		Current.Filp[fd[0]] = nil
		Current.Filp[fd[1]] = nil
		f[0].FCount = 0; f[1].FCount = 0
		return -1
	}
	PipeBuffers[inode] = make([]byte, PAGE_SIZE)

	f[0].FInode = inode; f[1].FInode = inode
	f[0].FPos = 0; f[1].FPos = 0
	f[0].FMode = 1
	f[1].FMode = 2

	if len(fildes) >= 2 {
		fildes[0] = fd[0]
		fildes[1] = fd[1]
	}
	return 0
}
