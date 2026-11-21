package kernel

import (
	"testing"
	"time"
)

// 11/04 09:47-10:05: every call paid two SSH failures before HTTPS. After two SSH failures
// in any calls, the next call starts on HTTPS; SSH is measured again after the window, at
// once if it works again, and HTTPS failing as well sends calls back to SSH.
func TestTheNextCallStartsOnTheTransportTheLastCallsMeasured(t *testing.T) {
	gitRouteReset()
	defer gitRouteReset()
	now := time.Date(2026, 11, 4, 9, 47, 0, 0, time.UTC)
	gitRouteNow = func() time.Time { return now }
	defer func() { gitRouteNow = time.Now }()

	if !gitRouteStartSSH(true) {
		t.Fatal("with no history and a deploy key, start on SSH")
	}
	if gitRouteStartSSH(false) {
		t.Fatal("without a deploy key, never SSH")
	}
	gitRouteNote(true, false)
	if !gitRouteStartSSH(true) {
		t.Fatal("one SSH failure is not yet a pattern")
	}
	gitRouteNote(true, false)
	if gitRouteStartSSH(true) {
		t.Fatal("after two SSH failures in a row the next call must start on HTTPS")
	}
	gitRouteNote(false, true)
	now = now.Add(gitRouteRetrySSHAfter - time.Second)
	if gitRouteStartSSH(true) {
		t.Fatal("still inside the window: HTTPS")
	}
	now = now.Add(2 * time.Second)
	if !gitRouteStartSSH(true) {
		t.Fatal("after the window SSH is measured again")
	}
	gitRouteNote(true, true)
	if !gitRouteStartSSH(true) {
		t.Fatal("SSH worked: stay on it")
	}

	// both failing: HTTPS is not assumed to be better
	gitRouteNote(true, false)
	gitRouteNote(true, false)
	for i := 0; i < gitRouteHTTPSFailStreak; i++ {
		gitRouteNote(false, false)
	}
	if !gitRouteStartSSH(true) {
		t.Fatal("HTTPS failed as often: calls start on SSH again")
	}
}
