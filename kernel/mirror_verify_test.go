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
