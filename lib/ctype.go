package lib

const (
	_U = 0x01
	_L = 0x02
	_D = 0x04
	_C = 0x08
	_P = 0x10
	_S = 0x20
	_X = 0x40
	_SP = 0x80
)

func IsUpper(c byte) bool  { return c >= 'A' && c <= 'Z' }
func IsLower(c byte) bool  { return c >= 'a' && c <= 'z' }
func IsDigit(c byte) bool  { return c >= '0' && c <= '9' }
func IsAlpha(c byte) bool  { return IsUpper(c) || IsLower(c) }
func IsAlnum(c byte) bool  { return IsAlpha(c) || IsDigit(c) }
func IsSpace(c byte) bool  { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v' }
func IsCntrl(c byte) bool  { return c < 0x20 || c == 0x7F }
func IsPunct(c byte) bool  { return c > 0x20 && c < 0x7F && !IsAlnum(c) }
func IsPrint(c byte) bool  { return c >= 0x20 && c < 0x7F }
func IsXdigit(c byte) bool { return IsDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') }
func ToUpper(c byte) byte  { if IsLower(c) { return c - 0x20 }; return c }
func ToLower(c byte) byte  { if IsUpper(c) { return c + 0x20 }; return c }
