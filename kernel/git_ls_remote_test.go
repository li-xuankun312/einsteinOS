package kernel

import (
	"strings"
	"testing"
	"time"
)

// the listing goes through the same guard as a fetch and parses only "<sha> <ref>" lines
func TestLsRemoteHeadsListsBranchesThroughTheGuard(t *testing.T) {
	gitRouteReset()
	defer gitRouteReset()
	bare, clone := setupRemote(t)
	oldRemote, oldSSH := gitRemoteFn, gitSSHFn
	defer func() { gitRemoteFn, gitSSHFn = oldRemote, oldSSH }()
	gitSSHFn = func() bool { return false }
	gitRemoteFn = func(string, bool) (string, string) { return bare, "" }
	out, err := gitLsRemoteHeads(clone, "o/r", "refs/heads/einsteinOS/dev-*", "refs/heads/einsteinOS/stable")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "refs/heads/einsteinOS/dev-7") || strings.Contains(out, "packet") || strings.Contains(out, "refs/heads/main") {
		t.Fatalf("want exactly the dev branch, no trace lines: %q", out)
	}

	// a listing that never shows a sign of life is replaced, not waited on for 60 s
	oldGuard := gitGuardFn
	defer func() { gitGuardFn = oldGuard }()
	n := 0
	gitGuardFn = func(cmd, dir string, a, b, c time.Duration, tick func(time.Duration, string)) gitGuardResult {
		if n++; n < 3 {
			return gitGuardResult{Started: false, Code: -1}
		}
		return oldGuard(cmd, dir, a, b, c, tick)
	}
	if out, err := gitLsRemoteHeads(clone, "o/r", "refs/heads/einsteinOS/dev-*"); err != nil || !strings.Contains(out, "dev-7") {
		t.Fatalf("third attempt lists: %q %v", out, err)
	}
}
