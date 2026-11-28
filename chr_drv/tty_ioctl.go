package chr_drv

import (
	. "google.golang.org/adk/v2/include"
)

const (
	TCGETS    = 0x5401
	TCSETS    = 0x5402
	TCSETSW   = 0x5403
	TCSETSF   = 0x5404
	TCGETA    = 0x5405
	TCSETA    = 0x5406
	TCSETAW   = 0x5407
	TCSETAF   = 0x5408
	TCSBRK    = 0x5409
	TCXONC    = 0x540A
	TCFLSH    = 0x540B
	TIOCEXCL  = 0x540C
	TIOCNXCL  = 0x540D
	TIOCSCTTY = 0x540E
	TIOCGPGRP = 0x540F
	TIOCSPGRP = 0x5410
	TIOCOUTQ  = 0x5411
	TIOCINQ   = 0x541B
	TIOCSTI   = 0x5412
	TIOCGWINSZ = 0x5413
	TIOCSWINSZ = 0x5414
	TIOCMGET  = 0x5415
	TIOCMBIS  = 0x5416
	TIOCMBIC  = 0x5417
	TIOCMSET  = 0x5418
	TIOCGSOFTCAR = 0x5419
	TIOCSSOFTCAR = 0x541A
	CBAUD     = 0000017
	EINVAL_TTY = 22
	NCC       = 8
)

var quotient = [16]uint16{
	0, 2304, 1536, 1047, 857,
	768, 576, 384, 192, 96,
	64, 48, 24, 12, 6, 3,
}

func changeSpeed(tty *TtyStruct) {}

func flushQueue(queue *TtyQueue) { queue.Head = queue.Tail }

func waitUntilSent(tty *TtyStruct) {}
func sendBreak(tty *TtyStruct)     {}

func getTermios(tty *TtyStruct, out *Termios) int {
	*out = tty.Termios
	return 0
}

func setTermios(tty *TtyStruct, in *Termios) int {
	tty.Termios = *in
	changeSpeed(tty)
	return 0
}

func TtyIoctl(dev, cmd, arg int) int {
	var ttyNr int
	if int(MAJOR(uint32(dev))) == 5 {
		ttyNr = int(Current.Tty)
		if ttyNr < 0 { return -1 }
	} else {
		ttyNr = int(MINOR(uint32(dev)))
	}
	if ttyNr >= NR_TTY { return -EINVAL_TTY }
	tty := &TtyTable[ttyNr]
	switch cmd {
	case TCGETS:
		return getTermios(tty, &Termios{})
	case TCSETSF:
		flushQueue(&tty.ReadQ)
		fallthrough
	case TCSETSW:
		waitUntilSent(tty)
		fallthrough
	case TCSETS:
		return 0
	case TCGETA:
		return 0
	case TCSETAF:
		flushQueue(&tty.ReadQ)
		fallthrough
	case TCSETAW:
		waitUntilSent(tty)
		fallthrough
	case TCSETA:
		return 0
	case TCSBRK:
		if arg == 0 { waitUntilSent(tty); sendBreak(tty) }
		return 0
	case TCXONC:
		return -EINVAL_TTY
	case TCFLSH:
		if arg == 0 { flushQueue(&tty.ReadQ) } else if arg == 1 { flushQueue(&tty.WriteQ) } else if arg == 2 { flushQueue(&tty.ReadQ); flushQueue(&tty.WriteQ) } else { return -EINVAL_TTY }
		return 0
	case TIOCGPGRP:
		return tty.Pgrp
	case TIOCSPGRP:
		tty.Pgrp = arg
		return 0
	case TIOCOUTQ:
		return TTY_BUF_SIZE - tty.WriteQ.Left()
	case TIOCINQ:
		return TTY_BUF_SIZE - tty.Secondary.Left()
	default:
		return -EINVAL_TTY
	}
}
