package kernel

import (
	"testing"
	"unicode/utf8"
)

// 11/26 10:04:32 the gates' text in the log ended "check \xe2...": the cut fell inside a "—"
func TestTruncStrCutsAtACharacter(t *testing.T) {
	s := "check — more"
	for n := 1; n < len(s); n++ {
		if got := truncStr(s, n); !utf8.ValidString(got) {
			t.Fatalf("truncStr(%q, %d) = %q is not valid UTF-8", s, n, got)
		}
	}
	if got := truncStr("abc", 5); got != "abc" {
		t.Fatalf("a short string changed: %q", got)
	}
}
