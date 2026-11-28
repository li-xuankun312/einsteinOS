package chr_drv

import (
	"strings"
	"sync"
	"unicode"

	. "google.golang.org/adk/v2/include"
)

const (
	ICRNL   = 0000400
	INLCR   = 0000100
	IGNCR   = 0000200
	IUCLC   = 0001000
	OPOST   = 0000001
	ONLCR   = 0000004
	OCRNL   = 0000010
	ONLRET  = 0000040
	OLCUC   = 0000002
	ICANON  = 0000002
	ISIG    = 0000001
	ECHO    = 0000010
	ECHOE   = 0000020
	ECHOK   = 0000040
	ECHOCTL = 0001000
	ECHOKE  = 0004000
	B2400   = 0000013
	CS8     = 0000060
	VTIME   = 5
	VMIN    = 6
	NR_TTY  = 3
	TTY_BUF_SIZE = 1024
)

const (
	ALRMMASK = 1 << (SIGALRM - 1)
	KILLMASK = 1 << (SIGKILL - 1)
	INTMASK  = 1 << (SIGINT - 1)
	QUITMASK = 1 << (SIGQUIT - 1)
)

type Termios struct {
	CIflag uint32
	COflag uint32
	CCflag uint32
	CLflag uint32
	CLine  uint8
	CCc    [8]uint8
}

type TtyQueue struct {
	Data     int
	Head     int
	Tail     int
	ProcList *TaskStruct
	Buf      [TTY_BUF_SIZE]byte
}

func (q *TtyQueue) Empty() bool { return q.Head == q.Tail }
func (q *TtyQueue) Full() bool  { return ((q.Head + 1) % TTY_BUF_SIZE) == q.Tail }
func (q *TtyQueue) Left() int {
	l := q.Tail - q.Head - 1
	if l < 0 { l += TTY_BUF_SIZE }
	return l
}
func (q *TtyQueue) Last() byte {
	h := q.Head - 1
	if h < 0 { h += TTY_BUF_SIZE }
	return q.Buf[h]
}
func (q *TtyQueue) Getch() byte {
	c := q.Buf[q.Tail]
	q.Tail = (q.Tail + 1) % TTY_BUF_SIZE
	return c
}
func (q *TtyQueue) Putch(c byte) {
	q.Buf[q.Head] = c
	q.Head = (q.Head + 1) % TTY_BUF_SIZE
}

type TtyStruct struct {
	Termios   Termios
	Pgrp      int
	Stopped   int
	WriteFn   func(*TtyStruct)
	ReadQ     TtyQueue
	WriteQ    TtyQueue
	Secondary TtyQueue
	mu        sync.Mutex
}

func killChar(tty *TtyStruct) byte  { return tty.Termios.CCc[2] }
func eraseChar(tty *TtyStruct) byte { return tty.Termios.CCc[1] }
func eofChar(tty *TtyStruct) byte   { return tty.Termios.CCc[0] }
func stopChar(tty *TtyStruct) byte  { return tty.Termios.CCc[4] }
func startChar(tty *TtyStruct) byte { return tty.Termios.CCc[3] }
func intrChar(tty *TtyStruct) byte  { return tty.Termios.CCc[5] }
func quitChar(tty *TtyStruct) byte  { return tty.Termios.CCc[6] }

func lFlag(tty *TtyStruct, f uint32) bool { return tty.Termios.CLflag&f != 0 }
func iFlag(tty *TtyStruct, f uint32) bool { return tty.Termios.CIflag&f != 0 }
func oFlag(tty *TtyStruct, f uint32) bool { return tty.Termios.COflag&f != 0 }

var initCCc = [8]uint8{4, 0177, 025, 034, 021, 023, 003, 034}

var TtyTable [NR_TTY]TtyStruct

var (
	interruptibleSleepOnFn func(**TaskStruct)
	wakeUpFn               func(**TaskStruct)
	scheduleFn             func()
)

func SetInterruptibleSleepOn(fn func(**TaskStruct)) { interruptibleSleepOnFn = fn }
func SetWakeUp(fn func(**TaskStruct))               { wakeUpFn = fn }
func SetSchedule(fn func())                         { scheduleFn = fn }

func TtyInit() {
	TtyTable[0].Termios = Termios{
		CIflag: ICRNL,
		COflag: OPOST | ONLCR,
		CCflag: 0,
		CLflag: ISIG | ICANON | ECHO | ECHOCTL | ECHOKE,
		CCc:    initCCc,
	}
	TtyTable[0].WriteFn = ConWrite

	TtyTable[1].Termios = Termios{
		CCflag: B2400 | CS8,
		CCc:    initCCc,
	}
	TtyTable[1].WriteFn = RsWrite

	TtyTable[2].Termios = Termios{
		CCflag: B2400 | CS8,
		CCc:    initCCc,
	}
	TtyTable[2].WriteFn = RsWrite
}

func TtyIntr(tty *TtyStruct, mask uint32) {
	if tty.Pgrp <= 0 { return }
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil && Task[i].Pgrp == int32(tty.Pgrp) {
			Task[i].Signal |= int32(mask)
		}
	}
}

func sleepIfEmpty(queue *TtyQueue) {
	for !queue.Empty() { return }
	if Current.Signal == 0 && queue.Empty() {
		if interruptibleSleepOnFn != nil {
			interruptibleSleepOnFn(&queue.ProcList)
		}
	}
}

func sleepIfFull(queue *TtyQueue) {
	if !queue.Full() { return }
	if Current.Signal == 0 && queue.Left() < 128 {
		if interruptibleSleepOnFn != nil {
			interruptibleSleepOnFn(&queue.ProcList)
		}
	}
}

func CopyToCooked(tty *TtyStruct) {
	for !tty.ReadQ.Empty() && !tty.Secondary.Full() {
		c := int8(tty.ReadQ.Getch())
		if c == 13 {
			if iFlag(tty, ICRNL) { c = 10 } else if iFlag(tty, IGNCR) { continue }
		} else if c == 10 && iFlag(tty, INLCR) {
			c = 13
		}
		if iFlag(tty, IUCLC) { c = int8(unicode.ToLower(rune(c))) }
		if lFlag(tty, ICANON) {
			if byte(c) == killChar(tty) {
				for !tty.Secondary.Empty() {
					last := int8(tty.Secondary.Last())
					if last == 10 || byte(last) == eofChar(tty) { break }
					if lFlag(tty, ECHO) {
						if last < 32 { tty.WriteQ.Putch(127) }
						tty.WriteQ.Putch(127)
						if tty.WriteFn != nil { tty.WriteFn(tty) }
					}
					tty.Secondary.Head = (tty.Secondary.Head - 1 + TTY_BUF_SIZE) % TTY_BUF_SIZE
				}
				continue
			}
			if byte(c) == eraseChar(tty) {
				if tty.Secondary.Empty() { continue }
				last := int8(tty.Secondary.Last())
				if last == 10 || byte(last) == eofChar(tty) { continue }
				if lFlag(tty, ECHO) {
					if last < 32 { tty.WriteQ.Putch(127) }
					tty.WriteQ.Putch(127)
					if tty.WriteFn != nil { tty.WriteFn(tty) }
				}
				tty.Secondary.Head = (tty.Secondary.Head - 1 + TTY_BUF_SIZE) % TTY_BUF_SIZE
				continue
			}
			if byte(c) == stopChar(tty) { tty.Stopped = 1; continue }
			if byte(c) == startChar(tty) { tty.Stopped = 0; continue }
		}
		if lFlag(tty, ISIG) {
			if byte(c) == intrChar(tty) { TtyIntr(tty, INTMASK); continue }
			if byte(c) == quitChar(tty) { TtyIntr(tty, QUITMASK); continue }
		}
		if c == 10 || byte(c) == eofChar(tty) { tty.Secondary.Data++ }
		if lFlag(tty, ECHO) {
			if c == 10 {
				tty.WriteQ.Putch(10); tty.WriteQ.Putch(13)
			} else if c < 32 {
				if lFlag(tty, ECHOCTL) {
					tty.WriteQ.Putch('^')
					tty.WriteQ.Putch(byte(c + 64))
				}
			} else {
				tty.WriteQ.Putch(byte(c))
			}
			if tty.WriteFn != nil { tty.WriteFn(tty) }
		}
		tty.Secondary.Putch(byte(c))
	}
	if wakeUpFn != nil { wakeUpFn(&tty.Secondary.ProcList) }
}

func TtyRead(channel uint16, buf []byte, nr int) int {
	if int(channel) >= NR_TTY || nr < 0 { return -1 }
	tty := &TtyTable[channel]
	b := 0
	minimum := int(tty.Termios.CCc[VMIN])
	if minimum > nr { minimum = nr }
	for nr > 0 {
		if Current.Signal != 0 { break }
		if tty.Secondary.Empty() || (lFlag(tty, ICANON) &&
			tty.Secondary.Data == 0 && tty.Secondary.Left() > 20) {
			sleepIfEmpty(&tty.Secondary)
			continue
		}
		for nr > 0 && !tty.Secondary.Empty() {
			c := tty.Secondary.Getch()
			if byte(c) == eofChar(tty) || c == 10 { tty.Secondary.Data-- }
			if byte(c) == eofChar(tty) && lFlag(tty, ICANON) { return b }
			if b < len(buf) { buf[b] = c }
			b++; nr--
		}
		if lFlag(tty, ICANON) {
			if b > 0 { break }
		} else if b >= minimum { break }
	}
	if Current.Signal != 0 && b == 0 { return -EINTR }
	return b
}

func TtyWrite(channel uint16, buf []byte, nr int) int {
	if int(channel) >= NR_TTY || nr < 0 { return -1 }
	tty := &TtyTable[channel]
	b := 0
	crFlag := false
	for nr > 0 {
		sleepIfFull(&tty.WriteQ)
		if Current.Signal != 0 { break }
		for nr > 0 && !tty.WriteQ.Full() {
			c := buf[b]
			if oFlag(tty, OPOST) {
				if c == '\r' && oFlag(tty, OCRNL) { c = '\n' }
				if c == '\n' && oFlag(tty, ONLRET) { c = '\r' }
				if c == '\n' && !crFlag && oFlag(tty, ONLCR) {
					crFlag = true; tty.WriteQ.Putch(13); continue
				}
				if oFlag(tty, OLCUC) { c = byte(strings.ToUpper(string([]byte{c}))[0]) }
			}
			b++; nr--; crFlag = false
			tty.WriteQ.Putch(c)
		}
		if tty.WriteFn != nil { tty.WriteFn(tty) }
		if nr > 0 && scheduleFn != nil { scheduleFn() }
	}
	return b
}

func DoTtyInterrupt(ttyNr int) {
	if ttyNr >= 0 && ttyNr < NR_TTY { CopyToCooked(&TtyTable[ttyNr]) }
}

func ChrDevInit() {}

func ConWrite(tty *TtyStruct) {
	for !tty.WriteQ.Empty() {
		_ = tty.WriteQ.Getch()
	}
}


