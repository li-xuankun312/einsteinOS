package fs

import (
	. "google.golang.org/adk/v2/include"
)


type crwPtr func(rw int, minor uint16, buf []byte, count int, pos *int64) int

var (
	ttyReadFn  func(minor uint16, buf []byte, count int) int
	ttyWriteFn func(minor uint16, buf []byte, count int) int
)

func SetTtyRead(fn func(uint16, []byte, int) int)  { ttyReadFn = fn }
func SetTtyWrite(fn func(uint16, []byte, int) int) { ttyWriteFn = fn }

func rwTtyx(rw int, minor uint16, buf []byte, count int, pos *int64) int {
	if rw == READ {
		if ttyReadFn != nil { return ttyReadFn(minor, buf, count) }
		return -EIO
	}
	if ttyWriteFn != nil { return ttyWriteFn(minor, buf, count) }
	return -EIO
}

func rwTty(rw int, minor uint16, buf []byte, count int, pos *int64) int {
	if Current.Tty < 0 { return -EPERM }
	return rwTtyx(rw, uint16(Current.Tty), buf, count, pos)
}

func rwRam(rw int, minor uint16, buf []byte, count int, pos *int64) int  { return -EIO }
func rwMem(rw int, minor uint16, buf []byte, count int, pos *int64) int  { return -EIO }
func rwKmem(rw int, minor uint16, buf []byte, count int, pos *int64) int { return -EIO }

func rwPort(rw int, minor uint16, buf []byte, count int, pos *int64) int {
	return 0
}

func rwMemory(rw int, minor uint16, buf []byte, count int, pos *int64) int {
	switch minor {
	case 0: return rwRam(rw, minor, buf, count, pos)
	case 1: return rwMem(rw, minor, buf, count, pos)
	case 2: return rwKmem(rw, minor, buf, count, pos)
	case 3:
		if rw == READ { return 0 }
		return count
	case 4: return rwPort(rw, minor, buf, count, pos)
	default: return -EIO
	}
}

var crwTable = [...]crwPtr{
	nil,
	rwMemory,
	nil,
	nil,
	rwTtyx,
	rwTty,
	nil,
	nil,
}

func RwChar(rw int, dev uint16, buf []byte, count int, pos *int64) int {
	major := MAJOR(uint32(dev))
	if int(major) >= len(crwTable) { return -ENODEV }
	callAddr := crwTable[major]
	if callAddr == nil { return -ENODEV }
	return callAddr(rw, uint16(MINOR(uint32(dev))), buf, count, pos)
}
