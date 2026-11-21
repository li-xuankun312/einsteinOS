package kernel

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func procGone(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	f := strings.Fields(string(b))
	return len(f) > 2 && f[2] == "Z"
}

// 11/21 06:44-06:47 the evaluation's search ran benchmarks here; what a command left running
// after it returned is found, and killed before the integration's end-to-end run; a command
// that left nothing is not listed.
func TestWhatACommandLeftRunningIsKilled(t *testing.T) {
	bootKernel()
	KillLeftoverProcessGroups()
	clean, err := ForkExec("echo done", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	Wait(clean)
	pid, err := ForkExec("sleep 300 >/dev/null 2>&1 & echo $!", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	Wait(pid)
	bg, err := strconv.Atoi(strings.TrimSpace(GetResult(pid)))
	if err != nil {
		t.Fatalf("no pid printed: %q", GetResult(pid))
	}
	if procGone(bg) {
		t.Fatal("the background sleep was not running after its command returned")
	}
	killed := KillLeftoverProcessGroups()
	if len(killed) != 1 || !strings.Contains(killed[0], "sleep 300") {
		t.Fatalf("killed %q, want the one group the sleep is in", killed)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !procGone(bg) {
		if time.Now().After(deadline) {
			t.Fatalf("the background sleep %d still runs", bg)
		}
		<-time.After(10 * time.Millisecond)
	}
	if again := KillLeftoverProcessGroups(); len(again) != 0 {
		t.Fatalf("killed twice: %q", again)
	}
}
