package kernel

import (
	"os"
	"strings"
	"testing"
	"time"
)

// 11/26 09:49:16 the log kept 3000 bytes of a gate text whose kernel_unported part listed 168
// refused rows: the whole of it is written to a file the log line names
func TestTheWholeGateTextIsWrittenWhereTheLogSays(t *testing.T) {
	dir := t.TempDir()
	old := gatesTextDir
	gatesTextDir = func() string { return dir }
	t.Cleanup(func() { gatesTextDir = old })
	text := "kernel_unported: " + strings.Repeat("row — ", 2000)
	line := gatesLogLine("o/r", "uint", text, []string{"kernel_unported"}, time.Date(2026, 11, 26, 9, 49, 16, 0, time.UTC))
	i := strings.Index(line, ": /")
	if i < 0 || !strings.HasSuffix(line, ".txt)") {
		t.Fatalf("the line names no file: %q", line[len(line)-120:])
	}
	p := strings.TrimSuffix(line[i+2:], ")")
	b, err := os.ReadFile(p)
	if err != nil || string(b) != text {
		t.Fatalf("the file does not hold the whole text: %v (%d bytes)", err, len(b))
	}
	if len(line) > preVerifyGateTextLogMax+300 {
		t.Fatalf("the log line is not capped: %d bytes", len(line))
	}
}
