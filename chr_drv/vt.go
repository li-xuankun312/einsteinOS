package chr_drv

import (
	"sync"
)

const (
	NR_CONSOLES  = 8
	VT_ROWS      = 25
	VT_COLS      = 80
	VT_TAB_STOP  = 8
	VT_BUF_SIZE  = VT_ROWS * VT_COLS * 2
)

const (
	VT_ATTR_NORMAL    = 0x07
	VT_ATTR_BOLD      = 0x0F
	VT_ATTR_UNDERLINE = 0x01
	VT_ATTR_REVERSE   = 0x70
	VT_ATTR_BLINK     = 0x87
)

const (
	VT_STATE_NORMAL  = 0
	VT_STATE_ESC     = 1
	VT_STATE_CSI     = 2
	VT_STATE_CHARSET = 3
)

type VirtualConsole struct {
	mu          sync.Mutex
	active      bool
	screenBuf   [VT_BUF_SIZE]byte
	cursorX     int
	cursorY     int
	savedX      int
	savedY      int
	attr        byte
	defaultAttr byte
	state       int
	escParams   [16]int
	escNpar     int
	scrollTop   int
	scrollBot   int
	wrapMode    bool
	insertMode  bool
	originMode  bool
	charsetG0   byte
	charsetG1   byte
}

var (
	consoles      [NR_CONSOLES]VirtualConsole
	activeConsole int
	consoleMu     sync.Mutex
)

func VtInit() {
	for i := 0; i < NR_CONSOLES; i++ {
		c := &consoles[i]
		c.active = i == 0
		c.attr = VT_ATTR_NORMAL
		c.defaultAttr = VT_ATTR_NORMAL
		c.scrollBot = VT_ROWS - 1
		c.wrapMode = true
		c.charsetG0 = 'B'
		c.charsetG1 = '0'
		vtClear(c)
	}
	activeConsole = 0
}

func vtClear(c *VirtualConsole) {
	for i := 0; i < VT_BUF_SIZE; i += 2 {
		c.screenBuf[i] = ' '
		c.screenBuf[i+1] = c.attr
	}
	c.cursorX = 0
	c.cursorY = 0
}

func VtWrite(console int, data []byte) int {
	if console < 0 || console >= NR_CONSOLES {
		return -1
	}
	c := &consoles[console]
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, b := range data {
		vtPutChar(c, b)
	}
	return len(data)
}

func vtPutChar(c *VirtualConsole, ch byte) {
	switch c.state {
	case VT_STATE_NORMAL:
		switch ch {
		case 0x07:
			return
		case 0x08:
			if c.cursorX > 0 {
				c.cursorX--
			}
		case 0x09:
			c.cursorX = (c.cursorX + VT_TAB_STOP) &^ (VT_TAB_STOP - 1)
			if c.cursorX >= VT_COLS {
				c.cursorX = VT_COLS - 1
			}
		case 0x0A, 0x0B, 0x0C:
			vtLineFeed(c)
		case 0x0D:
			c.cursorX = 0
		case 0x1B:
			c.state = VT_STATE_ESC
			c.escNpar = 0
			for i := range c.escParams {
				c.escParams[i] = 0
			}
		default:
			if ch >= 0x20 {
				vtSetChar(c, c.cursorX, c.cursorY, ch, c.attr)
				c.cursorX++
				if c.cursorX >= VT_COLS {
					if c.wrapMode {
						c.cursorX = 0
						vtLineFeed(c)
					} else {
						c.cursorX = VT_COLS - 1
					}
				}
			}
		}

	case VT_STATE_ESC:
		switch ch {
		case '[':
			c.state = VT_STATE_CSI
		case '7':
			c.savedX = c.cursorX
			c.savedY = c.cursorY
			c.state = VT_STATE_NORMAL
		case '8':
			c.cursorX = c.savedX
			c.cursorY = c.savedY
			c.state = VT_STATE_NORMAL
		case 'D':
			vtLineFeed(c)
			c.state = VT_STATE_NORMAL
		case 'M':
			vtReverseLineFeed(c)
			c.state = VT_STATE_NORMAL
		case 'E':
			c.cursorX = 0
			vtLineFeed(c)
			c.state = VT_STATE_NORMAL
		case 'c':
			vtClear(c)
			c.attr = c.defaultAttr
			c.state = VT_STATE_NORMAL
		default:
			c.state = VT_STATE_NORMAL
		}

	case VT_STATE_CSI:
		if ch >= '0' && ch <= '9' {
			if c.escNpar < len(c.escParams) {
				c.escParams[c.escNpar] = c.escParams[c.escNpar]*10 + int(ch-'0')
			}
		} else if ch == ';' {
			c.escNpar++
		} else {
			c.escNpar++
			vtHandleCSI(c, ch)
			c.state = VT_STATE_NORMAL
		}
	}
}

func vtHandleCSI(c *VirtualConsole, cmd byte) {
	p := c.escParams

	switch cmd {
	case 'A':
		n := max1(p[0])
		c.cursorY -= n
		if c.cursorY < 0 {
			c.cursorY = 0
		}
	case 'B':
		n := max1(p[0])
		c.cursorY += n
		if c.cursorY >= VT_ROWS {
			c.cursorY = VT_ROWS - 1
		}
	case 'C':
		n := max1(p[0])
		c.cursorX += n
		if c.cursorX >= VT_COLS {
			c.cursorX = VT_COLS - 1
		}
	case 'D':
		n := max1(p[0])
		c.cursorX -= n
		if c.cursorX < 0 {
			c.cursorX = 0
		}
	case 'H', 'f':
		row := max1(p[0]) - 1
		col := 0
		if c.escNpar > 1 {
			col = max1(p[1]) - 1
		}
		if row < 0 {
			row = 0
		}
		if row >= VT_ROWS {
			row = VT_ROWS - 1
		}
		if col < 0 {
			col = 0
		}
		if col >= VT_COLS {
			col = VT_COLS - 1
		}
		c.cursorX = col
		c.cursorY = row
	case 'J':
		switch p[0] {
		case 0:
			vtClearToEnd(c)
		case 1:
			vtClearFromStart(c)
		case 2:
			vtClear(c)
		}
	case 'K':
		switch p[0] {
		case 0:
			vtClearLineToEnd(c)
		case 1:
			vtClearLineFromStart(c)
		case 2:
			vtClearLine(c, c.cursorY)
		}
	case 'm':
		for i := 0; i < c.escNpar; i++ {
			vtSetAttr(c, p[i])
		}
	case 'r':
		top := 0
		bot := VT_ROWS - 1
		if p[0] > 0 {
			top = p[0] - 1
		}
		if c.escNpar > 1 && p[1] > 0 {
			bot = p[1] - 1
		}
		if top < bot && bot < VT_ROWS {
			c.scrollTop = top
			c.scrollBot = bot
		}
	case 's':
		c.savedX = c.cursorX
		c.savedY = c.cursorY
	case 'u':
		c.cursorX = c.savedX
		c.cursorY = c.savedY
	}
}

func vtSetAttr(c *VirtualConsole, attr int) {
	switch {
	case attr == 0:
		c.attr = c.defaultAttr
	case attr == 1:
		c.attr |= 0x08
	case attr == 4:
		c.attr = (c.attr & 0xF0) | 0x01
	case attr == 7:
		c.attr = ((c.attr & 0x0F) << 4) | ((c.attr & 0xF0) >> 4)
	case attr >= 30 && attr <= 37:
		c.attr = (c.attr & 0xF8) | byte(attr-30)
	case attr >= 40 && attr <= 47:
		c.attr = (c.attr & 0x8F) | (byte(attr-40) << 4)
	}
}

func vtLineFeed(c *VirtualConsole) {
	c.cursorY++
	if c.cursorY > c.scrollBot {
		c.cursorY = c.scrollBot
		vtScrollUp(c)
	}
}

func vtReverseLineFeed(c *VirtualConsole) {
	c.cursorY--
	if c.cursorY < c.scrollTop {
		c.cursorY = c.scrollTop
		vtScrollDown(c)
	}
}

func vtScrollUp(c *VirtualConsole) {
	for row := c.scrollTop; row < c.scrollBot; row++ {
		srcOff := row * VT_COLS * 2
		dstOff := (row + 1) * VT_COLS * 2
		copy(c.screenBuf[srcOff:srcOff+VT_COLS*2], c.screenBuf[dstOff:dstOff+VT_COLS*2])
	}
	vtClearLine(c, c.scrollBot)
}

func vtScrollDown(c *VirtualConsole) {
	for row := c.scrollBot; row > c.scrollTop; row-- {
		dstOff := row * VT_COLS * 2
		srcOff := (row - 1) * VT_COLS * 2
		copy(c.screenBuf[dstOff:dstOff+VT_COLS*2], c.screenBuf[srcOff:srcOff+VT_COLS*2])
	}
	vtClearLine(c, c.scrollTop)
}

func vtSetChar(c *VirtualConsole, x, y int, ch, attr byte) {
	if x < 0 || x >= VT_COLS || y < 0 || y >= VT_ROWS {
		return
	}
	off := (y*VT_COLS + x) * 2
	c.screenBuf[off] = ch
	c.screenBuf[off+1] = attr
}

func vtClearToEnd(c *VirtualConsole) {
	vtClearLineToEnd(c)
	for row := c.cursorY + 1; row < VT_ROWS; row++ {
		vtClearLine(c, row)
	}
}

func vtClearFromStart(c *VirtualConsole) {
	vtClearLineFromStart(c)
	for row := 0; row < c.cursorY; row++ {
		vtClearLine(c, row)
	}
}

func vtClearLine(c *VirtualConsole, row int) {
	if row < 0 || row >= VT_ROWS {
		return
	}
	off := row * VT_COLS * 2
	for i := 0; i < VT_COLS*2; i += 2 {
		c.screenBuf[off+i] = ' '
		c.screenBuf[off+i+1] = c.attr
	}
}

func vtClearLineToEnd(c *VirtualConsole) {
	off := (c.cursorY*VT_COLS + c.cursorX) * 2
	for x := c.cursorX; x < VT_COLS; x++ {
		c.screenBuf[off] = ' '
		c.screenBuf[off+1] = c.attr
		off += 2
	}
}

func vtClearLineFromStart(c *VirtualConsole) {
	off := c.cursorY * VT_COLS * 2
	for x := 0; x <= c.cursorX; x++ {
		c.screenBuf[off] = ' '
		c.screenBuf[off+1] = c.attr
		off += 2
	}
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

func VtSwitchConsole(n int) int {
	consoleMu.Lock()
	defer consoleMu.Unlock()

	if n < 0 || n >= NR_CONSOLES {
		return -1
	}
	consoles[activeConsole].active = false
	activeConsole = n
	consoles[n].active = true
	return 0
}

func VtGetActiveConsole() int {
	consoleMu.Lock()
	defer consoleMu.Unlock()
	return activeConsole
}

func VtGetCursor(console int) (x, y int) {
	if console < 0 || console >= NR_CONSOLES {
		return 0, 0
	}
	c := &consoles[console]
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cursorX, c.cursorY
}

func VtGetScreenData(console int) []byte {
	if console < 0 || console >= NR_CONSOLES {
		return nil
	}
	c := &consoles[console]
	c.mu.Lock()
	defer c.mu.Unlock()
	data := make([]byte, VT_BUF_SIZE)
	copy(data, c.screenBuf[:])
	return data
}
