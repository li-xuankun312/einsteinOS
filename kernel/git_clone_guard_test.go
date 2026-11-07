package kernel

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A clone is judged by progress, not by a total-time ceiling that throws away what it
// received: it runs under the stall timeout and the clone backstop (hours), not 15 minutes.
func TestACloneIsNotKilledWhileItMoves(t *testing.T) {
	old := gitGuardFn
	defer func() { gitGuardFn = old }()
	var gotMax, gotStall time.Duration
	var gotCmd string
	gitGuardFn = func(cmd, dir string, start, maxRun, stall time.Duration, tick func(time.Duration, string)) gitGuardResult {
		gotCmd, gotMax, gotStall = cmd, maxRun, stall
		return gitGuardResult{Started: true, Code: 1, Out: "stop here"}
	}
	dir := filepath.Join(t.TempDir(), "clone")
	os.RemoveAll(dir)
	GitCloneOrReuse("o/r", dir)
	if gotCmd == "" || gotMax < time.Hour || gotStall != gitPushStallTimeout {
		t.Fatalf("clone %q ran with maxRun=%s stall=%s", gotCmd, gotMax, gotStall)
	}
}
