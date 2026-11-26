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

// 11/26 14:27:15 four gates came to 21728 bytes and were shaped as one text: a gate in the middle
// was cut out whole. Each gate keeps its share, its first line with it.
func TestEachGateKeepsItsShareOfWhatTheConversationIsSent(t *testing.T) {
	text := "port_ref_file_unknown: 2 rows\n" + strings.Repeat("r\n", 4000) +
		"kernel_unported: 1 file(s)\n" + strings.Repeat("k\n", 4000) +
		"port_incomplete: 150 of 168\n" + strings.Repeat("p\n", 4000)
	got := gatesSentText(text, []string{"port_ref_file_unknown", "kernel_unported", "port_incomplete"}, 8000)
	for _, w := range []string{"port_ref_file_unknown: 2 rows", "kernel_unported: 1 file(s)", "port_incomplete: 150 of 168"} {
		if !strings.Contains(got, w) {
			t.Fatalf("lacks %q", w)
		}
	}
	if len(got) > 8000+600 {
		t.Fatalf("over the limit: %d", len(got))
	}
}

// 11/26 15:44 the embed part took port_incomplete to pass below a threshold of "about 144": the
// gate says it passes when every definition has a counterpart
func TestPortIncompleteSaysWhenItPasses(t *testing.T) {
	_, texts := unitGatesAll(GateInput{PortMissing: []string{"  a.cpp func main: no line in m"}, Req: UnitRequirements{Symbols: make([]RefSymbol, 168)}})
	if all := strings.Join(texts, "\n"); !strings.Contains(all, "port_incomplete: 1 of the 168 definitions of the unit's reference files have no counterpart, and the gate passes when every one has:") {
		t.Fatal(all)
	}
}
