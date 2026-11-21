package chr_drv

import (
	"sync"
)

const (
	KB_BUF_SIZE = 256
	LED_SCROLL  = 1
	LED_NUM     = 2
	LED_CAPS    = 4
)

const (
	KEY_NULL   = 0x00
	KEY_ESC    = 0x1B
	KEY_TAB    = 0x09
	KEY_ENTER  = 0x0D
	KEY_LF     = 0x0A
	KEY_BS     = 0x08
	KEY_DEL    = 0x7F
	KEY_HOME   = 0x101
	KEY_END    = 0x102
	KEY_UP     = 0x103
	KEY_DOWN   = 0x104
	KEY_LEFT   = 0x105
	KEY_RIGHT  = 0x106
	KEY_PGUP   = 0x107
	KEY_PGDN   = 0x108
	KEY_INSERT = 0x109
	KEY_F1     = 0x10A
	KEY_F2     = 0x10B
	KEY_F3     = 0x10C
	KEY_F4     = 0x10D
	KEY_F5     = 0x10E
	KEY_F6     = 0x10F
	KEY_F7     = 0x110
	KEY_F8     = 0x111
	KEY_F9     = 0x112
	KEY_F10    = 0x113
	KEY_F11    = 0x114
	KEY_F12    = 0x115
)

const (
	MOD_SHIFT = 1 << iota
	MOD_CTRL
	MOD_ALT
	MOD_CAPS
	MOD_NUM
)

var keyMap = [128]byte{
	0, 27, '1', '2', '3', '4', '5', '6', '7', '8', '9', '0', '-', '=', 8, 9,
	'q', 'w', 'e', 'r', 't', 'y', 'u', 'i', 'o', 'p', '[', ']', 13, 0, 'a', 's',
	'd', 'f', 'g', 'h', 'j', 'k', 'l', ';', '\'', '`', 0, '\\', 'z', 'x', 'c', 'v',
	'b', 'n', 'm', ',', '.', '/', 0, '*', 0, ' ', 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, '7', '8', '9', '-', '4', '5', '6', '+', '1',
	'2', '3', '0', '.', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
}

var shiftMap = [128]byte{
	0, 27, '!', '@', '#', '$', '%', '^', '&', '*', '(', ')', '_', '+', 8, 9,
	'Q', 'W', 'E', 'R', 'T', 'Y', 'U', 'I', 'O', 'P', '{', '}', 13, 0, 'A', 'S',
	'D', 'F', 'G', 'H', 'J', 'K', 'L', ':', '"', '~', 0, '|', 'Z', 'X', 'C', 'V',
	'B', 'N', 'M', '<', '>', '?', 0, '*', 0, ' ', 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, '7', '8', '9', '-', '4', '5', '6', '+', '1',
	'2', '3', '0', '.', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
}

var ctrlMap = [128]byte{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 31, 0, 127, 0,
	17, 23, 5, 18, 20, 25, 21, 9, 15, 16, 27, 29, 10, 0, 1, 19,
	4, 6, 7, 8, 10, 11, 12, 0, 0, 0, 0, 28, 26, 24, 3, 22,
	2, 14, 13, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
}

type KeyboardState struct {
	mu        sync.Mutex
	modifiers int
	ledState  byte
	buf       [KB_BUF_SIZE]byte
	head      int
	tail      int
	count     int
	e0        bool
}

var kbState KeyboardState

func KbInit() {
	kbState.modifiers = 0
	kbState.ledState = LED_NUM
	kbState.head = 0
	kbState.tail = 0
	kbState.count = 0
	kbState.e0 = false
}

func kbPut(c byte) {
	if kbState.count >= KB_BUF_SIZE {
		return
	}
	kbState.buf[kbState.head] = c
	kbState.head = (kbState.head + 1) % KB_BUF_SIZE
	kbState.count++
}

func KbGet() (byte, bool) {
	kbState.mu.Lock()
	defer kbState.mu.Unlock()
	if kbState.count == 0 {
		return 0, false
	}
	c := kbState.buf[kbState.tail]
	kbState.tail = (kbState.tail + 1) % KB_BUF_SIZE
	kbState.count--
	return c, true
}

func KeyboardInterrupt(scancode byte) {
	kbState.mu.Lock()
	defer kbState.mu.Unlock()

	if scancode == 0xE0 {
		kbState.e0 = true
		return
	}

	keyUp := scancode&0x80 != 0
	code := scancode & 0x7F

	if kbState.e0 {
		kbState.e0 = false
		handleE0Key(code, keyUp)
		return
	}

	switch code {
	case 0x2A, 0x36:
		if keyUp {
			kbState.modifiers &^= MOD_SHIFT
		} else {
			kbState.modifiers |= MOD_SHIFT
		}
		return
	case 0x1D:
		if keyUp {
			kbState.modifiers &^= MOD_CTRL
		} else {
			kbState.modifiers |= MOD_CTRL
		}
		return
	case 0x38:
		if keyUp {
			kbState.modifiers &^= MOD_ALT
		} else {
			kbState.modifiers |= MOD_ALT
		}
		return
	case 0x3A:
		if !keyUp {
			kbState.modifiers ^= MOD_CAPS
			kbState.ledState ^= LED_CAPS
		}
		return
	case 0x45:
		if !keyUp {
			kbState.modifiers ^= MOD_NUM
			kbState.ledState ^= LED_NUM
		}
		return
	case 0x46:
		if !keyUp {
			kbState.ledState ^= LED_SCROLL
		}
		return
	}

	if keyUp {
		return
	}

	var ch byte
	if kbState.modifiers&MOD_CTRL != 0 {
		ch = ctrlMap[code]
	} else if kbState.modifiers&MOD_SHIFT != 0 {
		ch = shiftMap[code]
	} else {
		ch = keyMap[code]
	}

	if ch == 0 {
		return
	}

	if kbState.modifiers&MOD_CAPS != 0 {
		if ch >= 'a' && ch <= 'z' {
			ch -= 32
		} else if ch >= 'A' && ch <= 'Z' {
			ch += 32
		}
	}

	if kbState.modifiers&MOD_ALT != 0 {
		ch |= 0x80
	}

	kbPut(ch)

	if ttyNotifyFn != nil {
		ttyNotifyFn(0)
	}
}

func handleE0Key(code byte, keyUp bool) {
	if keyUp {
		return
	}
	switch code {
	case 0x1C:
		kbPut(KEY_ENTER)
	case 0x1D:
		if !keyUp {
			kbState.modifiers |= MOD_CTRL
		}
	case 0x35:
		kbPut('/')
	case 0x38:
		if !keyUp {
			kbState.modifiers |= MOD_ALT
		}
	case 0x47:
		kbPut(byte(KEY_HOME & 0xFF))
	case 0x48:
		kbPut(byte(KEY_UP & 0xFF))
	case 0x49:
		kbPut(byte(KEY_PGUP & 0xFF))
	case 0x4B:
		kbPut(byte(KEY_LEFT & 0xFF))
	case 0x4D:
		kbPut(byte(KEY_RIGHT & 0xFF))
	case 0x4F:
		kbPut(byte(KEY_END & 0xFF))
	case 0x50:
		kbPut(byte(KEY_DOWN & 0xFF))
	case 0x51:
		kbPut(byte(KEY_PGDN & 0xFF))
	case 0x52:
		kbPut(byte(KEY_INSERT & 0xFF))
	case 0x53:
		kbPut(KEY_DEL)
	}
}

var ttyNotifyFn func(channel int)

func SetTtyNotify(fn func(int)) { ttyNotifyFn = fn }

func KbCount() int {
	kbState.mu.Lock()
	defer kbState.mu.Unlock()
	return kbState.count
}

func KbFlush() {
	kbState.mu.Lock()
	defer kbState.mu.Unlock()
	kbState.head = 0
	kbState.tail = 0
	kbState.count = 0
}

func KbGetModifiers() int {
	kbState.mu.Lock()
	defer kbState.mu.Unlock()
	return kbState.modifiers
}

func KbGetLeds() byte {
	kbState.mu.Lock()
	defer kbState.mu.Unlock()
	return kbState.ledState
}

func ScanCodeToChar(scancode byte) byte {
	code := scancode & 0x7F
	if code >= 128 {
		return 0
	}
	return keyMap[code]
}
