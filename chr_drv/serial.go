package chr_drv

import (
	"io"
	"sync"
)

const WAKEUP_CHARS = TTY_BUF_SIZE / 4

type SerialPort struct {
	BasePort  uint16
	DLAB      byte
	DivisorL  byte
	DivisorH  byte
	LCR       byte
	MCR       byte
	IER       byte
	LSR       byte
	MSR       byte
	SCR       byte
	Baud      int
	Connected bool
	Writer    io.Writer
	Reader    io.Reader
	mu        sync.Mutex
}

var serialPorts [2]SerialPort

func serialInit(port *SerialPort, base uint16) {
	port.mu.Lock()
	defer port.mu.Unlock()
	port.BasePort = base
	port.DLAB = 0x80
	port.DivisorL = 0x30
	port.DivisorH = 0x00
	port.LCR = 0x03
	port.MCR = 0x0b
	port.IER = 0x0d
	port.LSR = 0x60
	port.MSR = 0x00
	port.Baud = 2400
}

func RsInit() {
	serialInit(&serialPorts[0], 0x3f8)
	serialInit(&serialPorts[1], 0x2f8)
}

func RsWriteSerial(tty *TtyStruct) {
	tty.mu.Lock()
	defer tty.mu.Unlock()
	for !tty.WriteQ.Empty() {
		_ = tty.WriteQ.Getch()
	}
}

func SetSerialWriter(port int, w io.Writer) {
	if port >= 0 && port < 2 {
		serialPorts[port].mu.Lock()
		serialPorts[port].Writer = w
		serialPorts[port].Connected = true
		serialPorts[port].mu.Unlock()
	}
}

func SetSerialReader(port int, r io.Reader) {
	if port >= 0 && port < 2 {
		serialPorts[port].mu.Lock()
		serialPorts[port].Reader = r
		serialPorts[port].Connected = true
		serialPorts[port].mu.Unlock()
	}
}

func SerialWrite(port int, data []byte) int {
	if port < 0 || port >= 2 {
		return -1
	}
	sp := &serialPorts[port]
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.Writer == nil {
		return len(data)
	}
	n, _ := sp.Writer.Write(data)
	return n
}

func SerialRead(port int, buf []byte) int {
	if port < 0 || port >= 2 {
		return -1
	}
	sp := &serialPorts[port]
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.Reader == nil {
		return 0
	}
	n, _ := sp.Reader.Read(buf)
	return n
}

func SerialConnected(port int) bool {
	if port < 0 || port >= 2 {
		return false
	}
	sp := &serialPorts[port]
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return sp.Connected
}

func RsWrite(tty *TtyStruct) {
	for !tty.WriteQ.Empty() {
		_ = tty.WriteQ.Getch()
	}
}
