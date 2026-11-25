package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 11/23 20:43-23:05 the parts rewrote the mirror for 38 rounds and no verification ran: the
// integration conversation gave no done line. A command that changes the mirror starts its
// verification, a change during one has another follow it, and each conversation's next command
// carries the report once
func TestTheMirrorIsVerifiedWhenItChanges(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	noteMirrorConv("uint", dir, "8c680f2c", "")
	noteMirrorConv("uint", dir, "25f1be30", "gdn: x")
	var calls int32
	release := make(chan bool, 4)
	registerMirrorVerify("uint", func() (bool, string) {
		n := atomic.AddInt32(&calls, 1)
		<-release
		return false, "compile=fail (call " + string(rune('0'+n)) + ")"
	})
	defer registerMirrorVerify("uint", nil)
	if out := MirrorCommand("25f1be30", func() string { return "read" }); out != "read" {
		t.Fatalf("a command changing nothing: %q", out)
	}
	MirrorCommand("25f1be30", func() string { os.WriteFile(filepath.Join(dir, "a.cu"), []byte("1"), 0o644); return "" })
	MirrorCommand("25f1be30", func() string { os.WriteFile(filepath.Join(dir, "a.cu"), []byte("22"), 0o644); return "" })
	release <- true
	release <- true
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&calls) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	for mirrorVerifierOf("uint").seq < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("%d verifications for two changes, the second during the first", calls)
	}
	out := MirrorCommand("8c680f2c", func() string { return "" })
	if !strings.Contains(out, "[THE KERNEL'S VERIFICATION OF THE UNIT'S MIRROR") || !strings.Contains(out, "compile=fail (call 2)") {
		t.Fatalf("%q", out)
	}
	if out := MirrorCommand("8c680f2c", func() string { return "" }); strings.Contains(out, "VERIFICATION") {
		t.Fatalf("told twice: %q", out)
	}
}

// 11/25 04:23:56 a done line's verification held test.sh on the router part's file and its
// text reached the integration conversation alone: it goes to the unit's other conversations
// with their next command's result, and not again to the unit's own
func TestADoneLinesVerificationGoesToTheUnitsParts(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	defer registerMirrorVerify("uint", nil)
	dir := t.TempDir()
	registerMirrorVerify("uint", func() (bool, string) { return false, "" })
	noteMirrorConv("uint", dir, "8c680f2c", "")
	noteMirrorConv("uint", dir, "a6d47c28", "router: z")
	mirrorVerifyShare("uint", "machine_down_during_test: x")
	got := mirrorVerifyNews("uint", "a6d47c28")
	if !strings.Contains(got, "at the done line of the unit's own conversation]\nmachine_down_during_test: x") {
		t.Fatalf("part: %q", got)
	}
	if again := mirrorVerifyNews("uint", "a6d47c28"); again != "" {
		t.Fatalf("given twice: %q", again)
	}
	if own := mirrorVerifyNews("uint", "8c680f2c"); own != "" {
		t.Fatalf("the unit's own conversation had it as its done line's answer: %q", own)
	}
}

// 11/25 12:23:44 the verification begun about 12:16 passed its compile, and embed (b10706e5)
// had written embed_parity.cu's second e2e_embed_main since: what was written while a
// verification ran is named with its report, whose and when, as not in it
func TestWhatWasWrittenWhileAVerificationRanIsNamedWithIt(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	noteMirrorConv("uint", dir, "a3e69c50", "")
	noteMirrorConv("uint", dir, "b10706e5", "embed: the token embedding")
	began := make(chan bool, 1)
	release := make(chan bool, 4)
	var calls int32
	registerMirrorVerify("uint", func() (bool, string) {
		if atomic.AddInt32(&calls, 1) == 1 {
			began <- true
		}
		<-release
		return false, "compile=pass smoke=pass gate=kernel_unported"
	})
	defer registerMirrorVerify("uint", nil)
	MirrorCommand("a3e69c50", func() string { os.WriteFile(filepath.Join(dir, "a.cu"), []byte("1"), 0o644); return "" })
	<-began
	MirrorCommand("b10706e5", func() string {
		os.MkdirAll(filepath.Join(dir, "tests"), 0o755)
		os.WriteFile(filepath.Join(dir, "tests/embed_parity.cu"), []byte("int e2e_embed_main() { return 0; }\n"), 0o644)
		return ""
	})
	release <- true
	deadline := time.Now().Add(5 * time.Second)
	for mirrorVerifierOf("uint").seq < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	v := mirrorVerifierOf("uint")
	v.mu.Lock()
	first := v.report
	v.mu.Unlock()
	release <- true
	if !strings.Contains(first, "[WRITTEN AFTER THIS VERIFICATION BEGAN AT ") ||
		!strings.Contains(first, "1 write(s): tests/embed_parity.cu by embed (conversation b10706e5) at ") {
		t.Fatalf("%q", first)
	}
	for mirrorVerifierOf("uint").seq < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	v.mu.Lock()
	second := v.report
	v.mu.Unlock()
	if strings.Contains(second, "WRITTEN AFTER") {
		t.Fatalf("the verification that ran on them still said them: %q", second)
	}
}
