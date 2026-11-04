package kernel

import (
	"strings"
	"testing"
	"time"
)

// 11/04 10:13-10:16: 14 SSH attempts while HTTPS answered in 2 s. After
// gitPushSwitchAfter failures on SSH the push goes over HTTPS, and the next push starts
// there because the route was measured.
func TestAPushThatKeepsFailingOnSSHGoesOverHTTPS(t *testing.T) {
	gitRouteReset()
	defer gitRouteReset()
	oldGuard, oldSSH, oldTok := gitGuardFn, gitSSHFn, gitTokenFn
	defer func() { gitGuardFn, gitSSHFn, gitTokenFn = oldGuard, oldSSH, oldTok }()
	gitSSHFn = func() bool { return true }
	gitTokenFn = func() string { return "tok" }
	var cmds []string
	gitGuardFn = func(cmd, dir string, a, b, c time.Duration, tick func(time.Duration, string)) gitGuardResult {
		cmds = append(cmds, cmd)
		if strings.Contains(cmd, "GIT_SSH_COMMAND") {
			return gitGuardResult{Started: false}
		}
		return gitGuardResult{Started: true, Out: "To github.com:o/r.git\n   a..b  br -> br\n"}
	}
	if err := GitPushRetry(t.TempDir(), "o/r", "br", 20); err != nil {
		t.Fatal(err)
	}
	if len(cmds) != gitPushSwitchAfter+1 {
		t.Fatalf("%d SSH failures then HTTPS: %d commands", gitPushSwitchAfter, len(cmds))
	}
	if strings.Contains(cmds[len(cmds)-1], "GIT_SSH_COMMAND") {
		t.Fatal("the successful attempt must have gone over HTTPS")
	}

	cmds = nil
	if err := GitPushRetry(t.TempDir(), "o/r", "br", 20); err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 || strings.Contains(cmds[0], "GIT_SSH_COMMAND") {
		t.Fatalf("the next push starts on the transport that worked: %v", cmds)
	}
}

// without a token there is no other transport: SSH keeps being tried
func TestWithoutATokenThePushStaysOnSSH(t *testing.T) {
	gitRouteReset()
	defer gitRouteReset()
	oldGuard, oldSSH, oldTok := gitGuardFn, gitSSHFn, gitTokenFn
	defer func() { gitGuardFn, gitSSHFn, gitTokenFn = oldGuard, oldSSH, oldTok }()
	gitSSHFn = func() bool { return true }
	gitTokenFn = func() string { return "" }
	n := 0
	gitGuardFn = func(cmd, dir string, a, b, c time.Duration, tick func(time.Duration, string)) gitGuardResult {
		n++
		if !strings.Contains(cmd, "GIT_SSH_COMMAND") {
			t.Fatalf("no token: never HTTPS: %s", cmd)
		}
		if n < 5 {
			return gitGuardResult{Started: false}
		}
		return gitGuardResult{Started: true, Out: "   a..b  br -> br\n"}
	}
	if err := GitPushRetry(t.TempDir(), "o/r", "br", 20); err != nil {
		t.Fatal(err)
	}
}
