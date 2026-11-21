package chr_drv

import (
	"fmt"
	"io"
	"os"
	"sync"
)

const (
	NPAR        = 16
	VIDEO_COLS  = 80
	VIDEO_LINES = 25
	VIDEO_SIZE  = VIDEO_COLS * VIDEO_LINES
	VIDEO_MEM_SIZE = VIDEO_SIZE * 2
)

var (
	conMu      sync.Mutex
	conWriter  io.Writer = os.Stdout
	videoMem   [VIDEO_MEM_SIZE]byte
	originAddr int
	scrEnd     int
	pos        int
	originX    int
	originY    int
	top        int
	bottom     int = VIDEO_LINES
	state      int
	npar       int
	par        [NPAR]uint32
	ques       int
	savedX     int
	savedY     int
	attr       byte = 0x07
	videoType  byte = 0x03
)

func SetConsoleWriter(w io.Writer) {
	conMu.Lock()
	conWriter = w
	conMu.Unlock()
}

func posFromXY(x, y int) int { return (y*VIDEO_COLS + x) * 2 }

func gotoxy(newX, newY int) {
	if newX < 0 { newX = 0 }
	if newX >= VIDEO_COLS { newX = VIDEO_COLS - 1 }
	if newY < 0 { newY = 0 }
	if newY >= VIDEO_LINES { newY = VIDEO_LINES - 1 }
	originX = newX
	originY = newY
	pos = posFromXY(originX, originY)
}

func setOrigin() {
	originAddr = 0
}

func setCursor() {
	pos = posFromXY(originX, originY)
}

func scrup() {
	if top == 0 && bottom == VIDEO_LINES {
		copy(videoMem[0:], videoMem[VIDEO_COLS*2:VIDEO_LINES*VIDEO_COLS*2])
		for i := (VIDEO_LINES - 1) * VIDEO_COLS * 2; i < VIDEO_LINES*VIDEO_COLS*2; i += 2 {
			videoMem[i] = ' '
			videoMem[i+1] = attr
		}
	} else {
		src := (top + 1) * VIDEO_COLS * 2
		dst := top * VIDEO_COLS * 2
		cnt := (bottom - top - 1) * VIDEO_COLS * 2
		copy(videoMem[dst:], videoMem[src:src+cnt])
		clr := (bottom - 1) * VIDEO_COLS * 2
		for i := clr; i < clr+VIDEO_COLS*2; i += 2 {
			videoMem[i] = ' '
			videoMem[i+1] = attr
		}
	}
}

func scrdown() {
	src := top * VIDEO_COLS * 2
	dst := (top + 1) * VIDEO_COLS * 2
	cnt := (bottom - top - 1) * VIDEO_COLS * 2
	copy(videoMem[dst:dst+cnt], videoMem[src:src+cnt])
	for i := src; i < src+VIDEO_COLS*2; i += 2 {
		videoMem[i] = ' '
		videoMem[i+1] = attr
	}
}

func lf() {
	originY++
	if originY >= bottom {
		originY = bottom - 1
		scrup()
	}
	pos = posFromXY(originX, originY)
}

func ri() {
	originY--
	if originY < top {
		originY = top
		scrdown()
	}
	pos = posFromXY(originX, originY)
}

func cr() {
	originX = 0
	pos = posFromXY(originX, originY)
}

func del() {
	if originX > 0 {
		originX--
		p := posFromXY(originX, originY)
		videoMem[p] = ' '
		videoMem[p+1] = attr
		pos = p
	}
}

func csiJ(vpar int) {
	switch vpar {
	case 0:
		for i := pos; i < VIDEO_SIZE*2; i += 2 {
			videoMem[i] = ' '
			videoMem[i+1] = attr
		}
	case 1:
		for i := 0; i <= pos; i += 2 {
			videoMem[i] = ' '
			videoMem[i+1] = attr
		}
	case 2:
		for i := 0; i < VIDEO_SIZE*2; i += 2 {
			videoMem[i] = ' '
			videoMem[i+1] = attr
		}
	}
}

func csiK(vpar int) {
	lineStart := originY * VIDEO_COLS * 2
	lineEnd := lineStart + VIDEO_COLS*2
	switch vpar {
	case 0:
		for i := pos; i < lineEnd; i += 2 {
			videoMem[i] = ' '
			videoMem[i+1] = attr
		}
	case 1:
		for i := lineStart; i <= pos; i += 2 {
			videoMem[i] = ' '
			videoMem[i+1] = attr
		}
	case 2:
		for i := lineStart; i < lineEnd; i += 2 {
			videoMem[i] = ' '
			videoMem[i+1] = attr
		}
	}
}

func csiM_attr() {
	for i := 0; i <= npar; i++ {
		switch par[i] {
		case 0:
			attr = 0x07
		case 1:
			attr = 0x0F
		case 4:
			attr = 0x0F
		case 7:
			attr = 0x70
		case 27:
			attr = 0x07
		default:
			if par[i] >= 30 && par[i] <= 37 {
				attr = (attr & 0xF0) | byte(par[i]-30)
			} else if par[i] >= 40 && par[i] <= 47 {
				attr = (attr & 0x0F) | byte((par[i]-40)<<4)
			}
		}
	}
}

func respond(tty *TtyStruct) {
	resp := []byte{27, '[', '?', '6', 'c'}
	for _, b := range resp {
		tty.ReadQ.Putch(b)
	}
}

func insertChar() {
	p := posFromXY(originX, originY)
	lineEnd := posFromXY(VIDEO_COLS-1, originY)
	for i := lineEnd; i > p; i -= 2 {
		videoMem[i] = videoMem[i-2]
		videoMem[i+1] = videoMem[i-1]
	}
	videoMem[p] = ' '
	videoMem[p+1] = attr
}

func insertLine() {
	dst := posFromXY(0, bottom-1)
	for row := bottom - 1; row > originY; row-- {
		src := posFromXY(0, row-1)
		copy(videoMem[dst:dst+VIDEO_COLS*2], videoMem[src:src+VIDEO_COLS*2])
		dst = src
	}
	clr := posFromXY(0, originY)
	for i := clr; i < clr+VIDEO_COLS*2; i += 2 {
		videoMem[i] = ' '
		videoMem[i+1] = attr
	}
}

func deleteChar() {
	p := posFromXY(originX, originY)
	lineEnd := posFromXY(VIDEO_COLS-1, originY)
	for i := p; i < lineEnd; i += 2 {
		videoMem[i] = videoMem[i+2]
		videoMem[i+1] = videoMem[i+3]
	}
	videoMem[lineEnd] = ' '
	videoMem[lineEnd+1] = attr
}

func deleteLine() {
	src := posFromXY(0, originY+1)
	for row := originY; row < bottom-1; row++ {
		dst := posFromXY(0, row)
		copy(videoMem[dst:dst+VIDEO_COLS*2], videoMem[src:src+VIDEO_COLS*2])
		src += VIDEO_COLS * 2
	}
	clr := posFromXY(0, bottom-1)
	for i := clr; i < clr+VIDEO_COLS*2; i += 2 {
		videoMem[i] = ' '
		videoMem[i+1] = attr
	}
}

func csiAt(nr int) {
	if nr <= 0 { nr = 1 }
	for nr > 0 {
		insertChar()
		nr--
	}
}

func csiL(nr int) {
	if nr <= 0 { nr = 1 }
	for nr > 0 {
		insertLine()
		nr--
	}
}

func csiP(nr int) {
	if nr <= 0 { nr = 1 }
	for nr > 0 {
		deleteChar()
		nr--
	}
}

func csiMDel(nr int) {
	if nr <= 0 { nr = 1 }
	for nr > 0 {
		deleteLine()
		nr--
	}
}

func saveCur() {
	savedX = originX
	savedY = originY
}

func restoreCur() {
	gotoxy(savedX, savedY)
}

func sysbeep() {
	fmt.Fprint(conWriter, "\a")
}

func ConWriteConsole(tty *TtyStruct) {
	conMu.Lock()
	defer conMu.Unlock()

	for !tty.WriteQ.Empty() {
		c := tty.WriteQ.Getch()
		switch state {
		case 0:
			switch c {
			case 7:
				sysbeep()
			case 8:
				del()
			case 9:
				originX = (originX + 8) &^ 7
				if originX >= VIDEO_COLS {
					originX -= VIDEO_COLS
					lf()
				}
			case 10, 11, 12:
				lf()
				fmt.Fprint(conWriter, "\n")
			case 13:
				cr()
			case 27:
				state = 1
			case 127:
				del()
			default:
				if c >= 32 {
					p := posFromXY(originX, originY)
					if p+1 < len(videoMem) {
						videoMem[p] = c
						videoMem[p+1] = attr
					}
					fmt.Fprint(conWriter, string(rune(c)))
					originX++
					if originX >= VIDEO_COLS {
						originX = 0
						lf()
					}
				}
			}
		case 1:
			state = 0
			switch c {
			case '[':
				state = 2
				npar = 0
				for i := range par { par[i] = 0 }
				ques = 0
			case 'E':
				lf()
				cr()
			case 'M':
				ri()
			case 'D':
				lf()
			case 'Z':
				respond(tty)
			case '7':
				saveCur()
			case '8':
				restoreCur()
			case '(':
				state = 4
			case ')':
				state = 4
			}
		case 2:
			if c == '?' {
				ques = 1
				break
			}
			if c >= '0' && c <= '9' {
				par[npar] = par[npar]*10 + uint32(c-'0')
				break
			}
			if c == ';' {
				npar++
				if npar >= NPAR { npar = NPAR - 1 }
				break
			}
			state = 0
			switch c {
			case 'G', '`':
				if par[0] > 0 { par[0]-- }
				gotoxy(int(par[0]), originY)
			case 'A':
				n := int(par[0])
				if n == 0 { n = 1 }
				gotoxy(originX, originY-n)
			case 'B', 'e':
				n := int(par[0])
				if n == 0 { n = 1 }
				gotoxy(originX, originY+n)
			case 'C', 'a':
				n := int(par[0])
				if n == 0 { n = 1 }
				gotoxy(originX+n, originY)
			case 'D':
				n := int(par[0])
				if n == 0 { n = 1 }
				gotoxy(originX-n, originY)
			case 'H', 'f':
				gotoxy(int(par[1])-1, int(par[0])-1)
			case 'J':
				csiJ(int(par[0]))
			case 'K':
				csiK(int(par[0]))
			case 'L':
				csiL(int(par[0]))
			case 'M':
				csiMDel(int(par[0]))
			case 'P':
				csiP(int(par[0]))
			case '@':
				csiAt(int(par[0]))
			case 'm':
				csiM_attr()
			case 'r':
				if par[0] != 0 { par[0]-- }
				if par[1] == 0 { par[1] = uint32(VIDEO_LINES) }
				if par[0] < par[1] && par[1] <= uint32(VIDEO_LINES) {
					top = int(par[0])
					bottom = int(par[1])
				}
			case 's':
				saveCur()
			case 'u':
				restoreCur()
			}
		case 4:
			state = 0
		}
	}
}

func ConInit() {
	gotoxy(0, 0)
	top = 0
	bottom = VIDEO_LINES
	state = 0
	attr = 0x07
	for i := 0; i < VIDEO_SIZE*2; i += 2 {
		videoMem[i] = ' '
		videoMem[i+1] = attr
	}
}

func init() {
	TtyTable[0].WriteFn = ConWriteConsole
}
