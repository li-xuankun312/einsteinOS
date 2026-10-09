package lib

func Memcpy(dest, src []byte, n int) []byte {
	if n > len(src) { n = len(src) }
	if n > len(dest) { n = len(dest) }
	copy(dest[:n], src[:n])
	return dest
}

func Memmove(dest, src []byte, n int) []byte {
	if n > len(src) { n = len(src) }
	if n > len(dest) { n = len(dest) }
	copy(dest[:n], src[:n])
	return dest
}

func Memcmp(s1, s2 []byte, n int) int {
	for i := 0; i < n; i++ {
		if i >= len(s1) || i >= len(s2) { break }
		if s1[i] < s2[i] { return -1 }
		if s1[i] > s2[i] { return 1 }
	}
	return 0
}

func Memset(s []byte, c byte, n int) []byte {
	if n > len(s) { n = len(s) }
	for i := 0; i < n; i++ { s[i] = c }
	return s
}

func Strlen(s string) int { return len(s) }

func Strcpy(dest []byte, src string) []byte {
	n := copy(dest, src)
	if n < len(dest) { dest[n] = 0 }
	return dest
}

func Strncpy(dest []byte, src string, n int) []byte {
	i := 0
	for ; i < n && i < len(src); i++ {
		if i < len(dest) { dest[i] = src[i] }
	}
	for ; i < n && i < len(dest); i++ {
		dest[i] = 0
	}
	return dest
}

func Strcmp(s1, s2 string) int {
	if s1 < s2 { return -1 }
	if s1 > s2 { return 1 }
	return 0
}

func Strncmp(s1, s2 string, n int) int {
	if n > len(s1) { s1 += "\x00" }
	if n > len(s2) { s2 += "\x00" }
	a, b := s1, s2
	if n < len(a) { a = a[:n] }
	if n < len(b) { b = b[:n] }
	if a < b { return -1 }
	if a > b { return 1 }
	return 0
}

func Strchr(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c { return i }
	}
	return -1
}

func Strrchr(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c { return i }
	}
	return -1
}

func Strcat(dest []byte, src string) []byte {
	end := 0
	for end < len(dest) && dest[end] != 0 { end++ }
	for i := 0; i < len(src) && end < len(dest)-1; i++ {
		dest[end] = src[i]; end++
	}
	if end < len(dest) { dest[end] = 0 }
	return dest
}

func Strncat(dest []byte, src string, n int) []byte {
	end := 0
	for end < len(dest) && dest[end] != 0 { end++ }
	for i := 0; i < n && i < len(src) && end < len(dest)-1; i++ {
		dest[end] = src[i]; end++
	}
	if end < len(dest) { dest[end] = 0 }
	return dest
}
