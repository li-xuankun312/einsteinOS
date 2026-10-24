package kernel

import (
	"fmt"
	"os"
	"runtime"

	. "google.golang.org/adk/v2/include"
)

const (
	ZEROPAD_F = 1
	SIGN_F    = 2
	PLUS_F    = 4
	SPACE_F   = 8
	LEFT_F    = 16
	SPECIAL_F = 32
	SMALL_F   = 64
)

const (
	MINUTE = 60
	HOUR   = 60 * MINUTE
	DAY    = 24 * HOUR
	YEAR   = 365 * DAY
)

var monthSecs = [12]int32{
	0,
	DAY * (31),
	DAY * (31 + 29),
	DAY * (31 + 29 + 31),
	DAY * (31 + 29 + 31 + 30),
	DAY * (31 + 29 + 31 + 30 + 31),
	DAY * (31 + 29 + 31 + 30 + 31 + 30),
	DAY * (31 + 29 + 31 + 30 + 31 + 30 + 31),
	DAY * (31 + 29 + 31 + 30 + 31 + 30 + 31 + 31),
	DAY * (31 + 29 + 31 + 30 + 31 + 30 + 31 + 31 + 30),
	DAY * (31 + 29 + 31 + 30 + 31 + 30 + 31 + 31 + 30 + 31),
	DAY * (31 + 29 + 31 + 30 + 31 + 30 + 31 + 31 + 30 + 31 + 30),
}

func KernelMktime(year, mon, day, hour, min, sec int) int32 {
	yr := year
	if yr >= 70 {
		yr = yr - 70
	} else {
		yr = yr + 100 - 70
	}
	res := int32(YEAR)*int32(yr) + int32(DAY)*int32((yr+1)/4)
	res += monthSecs[mon]
	if mon > 1 && ((yr+2)%4) != 0 {
		res -= DAY
	}
	res += int32(DAY) * int32(day-1)
	res += int32(HOUR) * int32(hour)
	res += int32(MINUTE) * int32(min)
	res += int32(sec)
	return res
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func skipAtoi(s []byte, pos *int) int {
	i := 0
	for *pos < len(s) && isDigit(s[*pos]) {
		i = i*10 + int(s[*pos]-'0')
		*pos++
	}
	return i
}

func doDiv(n *uint32, base uint32) uint32 {
	res := *n % base
	*n = *n / base
	return res
}

func numberFmt(num int, base, size, precision, flags int) string {
	digits := "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	if flags&SMALL_F != 0 {
		digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	}
	if flags&LEFT_F != 0 {
		flags &^= ZEROPAD_F
	}
	if base < 2 || base > 36 {
		return ""
	}
	c := byte(' ')
	if flags&ZEROPAD_F != 0 {
		c = '0'
	}
	sign := byte(0)
	unum := uint32(num)
	if flags&SIGN_F != 0 && num < 0 {
		sign = '-'
		unum = uint32(-num)
	} else if flags&PLUS_F != 0 {
		sign = '+'
	} else if flags&SPACE_F != 0 {
		sign = ' '
	}
	if sign != 0 {
		size--
	}
	if flags&SPECIAL_F != 0 {
		if base == 16 {
			size -= 2
		} else if base == 8 {
			size--
		}
	}
	var tmp [36]byte
	i := 0
	if unum == 0 {
		tmp[i] = '0'
		i++
	} else {
		for unum != 0 {
			tmp[i] = digits[doDiv(&unum, uint32(base))]
			i++
		}
	}
	if i > precision {
		precision = i
	}
	size -= precision
	var buf []byte
	if flags&(ZEROPAD_F|LEFT_F) == 0 {
		for size > 0 {
			buf = append(buf, ' ')
			size--
		}
	}
	if sign != 0 {
		buf = append(buf, sign)
	}
	if flags&SPECIAL_F != 0 {
		if base == 8 {
			buf = append(buf, '0')
		} else if base == 16 {
			buf = append(buf, '0', digits[33])
		}
	}
	if flags&LEFT_F == 0 {
		for size > 0 {
			buf = append(buf, c)
			size--
		}
	}
	for i < precision {
		buf = append(buf, '0')
		precision--
	}
	for i > 0 {
		i--
		buf = append(buf, tmp[i])
	}
	for size > 0 {
		buf = append(buf, ' ')
		size--
	}
	return string(buf)
}

func Vsprintf(format string, args ...interface{}) string {
	f := []byte(format)
	pos := 0
	argIdx := 0
	var buf []byte

	getInt := func() int {
		if argIdx >= len(args) {
			return 0
		}
		v := args[argIdx]
		argIdx++
		switch x := v.(type) {
		case int:
			return x
		case int32:
			return int(x)
		case uint32:
			return int(x)
		case int64:
			return int(x)
		case uint16:
			return int(x)
		case uint8:
			return int(x)
		default:
			return 0
		}
	}
	getString := func() string {
		if argIdx >= len(args) {
			return ""
		}
		v := args[argIdx]
		argIdx++
		switch x := v.(type) {
		case string:
			return x
		case []byte:
			return string(x)
		default:
			return fmt.Sprintf("%v", x)
		}
	}

	for pos < len(f) {
		if f[pos] != '%' {
			buf = append(buf, f[pos])
			pos++
			continue
		}

		flags := 0
		pos++
	flagLoop:
		for pos < len(f) {
			switch f[pos] {
			case '-':
				flags |= LEFT_F
				pos++
			case '+':
				flags |= PLUS_F
				pos++
			case ' ':
				flags |= SPACE_F
				pos++
			case '#':
				flags |= SPECIAL_F
				pos++
			case '0':
				flags |= ZEROPAD_F
				pos++
			default:
				break flagLoop
			}
		}

		fieldWidth := -1
		if pos < len(f) && isDigit(f[pos]) {
			fieldWidth = skipAtoi(f, &pos)
		} else if pos < len(f) && f[pos] == '*' {
			pos++
			fieldWidth = getInt()
			if fieldWidth < 0 {
				fieldWidth = -fieldWidth
				flags |= LEFT_F
			}
		}

		precision := -1
		if pos < len(f) && f[pos] == '.' {
			pos++
			if pos < len(f) && isDigit(f[pos]) {
				precision = skipAtoi(f, &pos)
			} else if pos < len(f) && f[pos] == '*' {
				pos++
				precision = getInt()
			}
			if precision < 0 {
				precision = 0
			}
		}

		if pos < len(f) && (f[pos] == 'h' || f[pos] == 'l' || f[pos] == 'L') {
			pos++
		}

		if pos >= len(f) {
			break
		}

		switch f[pos] {
		case 'c':
			pos++
			n := getInt()
			if flags&LEFT_F == 0 {
				for fieldWidth > 1 {
					buf = append(buf, ' ')
					fieldWidth--
				}
			}
			buf = append(buf, byte(n))
			for fieldWidth > 1 {
				buf = append(buf, ' ')
				fieldWidth--
			}
		case 's':
			pos++
			s := getString()
			l := len(s)
			if precision >= 0 && l > precision {
				l = precision
			}
			if flags&LEFT_F == 0 {
				for l < fieldWidth {
					buf = append(buf, ' ')
					fieldWidth--
				}
			}
			buf = append(buf, s[:l]...)
			for l < fieldWidth {
				buf = append(buf, ' ')
				fieldWidth--
			}
		case 'o':
			pos++
			buf = append(buf, numberFmt(getInt(), 8, fieldWidth, precision, flags)...)
		case 'p':
			pos++
			if fieldWidth == -1 {
				fieldWidth = 8
				flags |= ZEROPAD_F
			}
			buf = append(buf, numberFmt(getInt(), 16, fieldWidth, precision, flags)...)
		case 'x':
			pos++
			flags |= SMALL_F
			buf = append(buf, numberFmt(getInt(), 16, fieldWidth, precision, flags)...)
		case 'X':
			pos++
			buf = append(buf, numberFmt(getInt(), 16, fieldWidth, precision, flags)...)
		case 'd', 'i':
			pos++
			flags |= SIGN_F
			buf = append(buf, numberFmt(getInt(), 10, fieldWidth, precision, flags)...)
		case 'u':
			pos++
			buf = append(buf, numberFmt(getInt(), 10, fieldWidth, precision, flags)...)
		case 'n':
			pos++
			argIdx++
		case '%':
			pos++
			buf = append(buf, '%')
		default:
			buf = append(buf, '%')
			if f[pos] != 0 {
				buf = append(buf, f[pos])
				pos++
			}
		}
	}
	return string(buf)
}

func Sprintf(format string, args ...interface{}) string {
	return Vsprintf(format, args...)
}

func Printk(format string, args ...interface{}) {
	s := Vsprintf(format, args...)
	fmt.Fprint(os.Stderr, s)
}

func Panic(s string) {
	Printk("Kernel panic: %s\n", s)
	runtime.Goexit()
}

type TrapFrame struct {
	EIP    uint32
	CS     uint32
	EFLAGS uint32
	ESP    uint32
	SS     uint32
}

func die(str string, frame *TrapFrame, errCode int32) {
	Printk("%s: %04x\n\r", str, errCode&0xffff)
	if frame != nil {
		Printk("EIP:\t%04x:%08x\nEFLAGS:\t%08x\nESP:\t%04x:%08x\n",
			int(frame.CS), int(frame.EIP), int(frame.EFLAGS), int(frame.SS), int(frame.ESP))
	}
	Printk("Pid: %d\n\r", int(Current.Pid))
	if doExitFn != nil {
		doExitFn(11)
	}
}

var doExitFn func(int32)

func SetDoExitForTrap(fn func(int32)) { doExitFn = fn }

func DoDivideError(frame *TrapFrame, errCode int32)              { die("divide error", frame, errCode) }
func DoDebug(frame *TrapFrame, errCode int32)                    { die("debug", frame, errCode) }
func DoNmi(frame *TrapFrame, errCode int32)                      { die("nmi", frame, errCode) }
func DoInt3(frame *TrapFrame, errCode int32)                     { die("int3 breakpoint", frame, errCode) }
func DoOverflow(frame *TrapFrame, errCode int32)                 { die("overflow", frame, errCode) }
func DoBounds(frame *TrapFrame, errCode int32)                   { die("bounds", frame, errCode) }
func DoInvalidOp(frame *TrapFrame, errCode int32)                { die("invalid operand", frame, errCode) }
func DoDeviceNotAvailable(frame *TrapFrame, errCode int32)       { die("device not available", frame, errCode) }
func DoDoubleFault(frame *TrapFrame, errCode int32)              { die("double fault", frame, errCode) }
func DoCoprocessorSegmentOverrun(frame *TrapFrame, errCode int32) { die("coprocessor segment overrun", frame, errCode) }
func DoInvalidTSS(frame *TrapFrame, errCode int32)               { die("invalid TSS", frame, errCode) }
func DoSegmentNotPresent(frame *TrapFrame, errCode int32)        { die("segment not present", frame, errCode) }
func DoStackSegment(frame *TrapFrame, errCode int32)             { die("stack segment", frame, errCode) }
func DoGeneralProtection(frame *TrapFrame, errCode int32)        { die("general protection", frame, errCode) }
func DoPageFault(frame *TrapFrame, errCode int32)                { die("page fault", frame, errCode) }
func DoCoprocessorError(frame *TrapFrame, errCode int32) {
	if LastTaskUsedMath != Current {
		return
	}
	die("coprocessor error", frame, errCode)
}
func DoReserved(frame *TrapFrame, errCode int32) { die("reserved (15,17-47) error", frame, errCode) }

type TrapHandler func(*TrapFrame, int32)

var TrapTable [48]TrapHandler

func TrapInit() {
	TrapTable[0] = DoDivideError
	TrapTable[1] = DoDebug
	TrapTable[2] = DoNmi
	TrapTable[3] = DoInt3
	TrapTable[4] = DoOverflow
	TrapTable[5] = DoBounds
	TrapTable[6] = DoInvalidOp
	TrapTable[7] = DoDeviceNotAvailable
	TrapTable[8] = DoDoubleFault
	TrapTable[9] = DoCoprocessorSegmentOverrun
	TrapTable[10] = DoInvalidTSS
	TrapTable[11] = DoSegmentNotPresent
	TrapTable[12] = DoStackSegment
	TrapTable[13] = DoGeneralProtection
	TrapTable[14] = DoPageFault
	TrapTable[15] = DoReserved
	TrapTable[16] = DoCoprocessorError
	for i := 17; i < 48; i++ {
		TrapTable[i] = DoReserved
	}
	Printk("kernel: trap_init done, %d handlers\n", 48)
}
