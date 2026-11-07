package kernel

// git_route.go — which transport a git network step starts on, remembered across calls.
//
// Each fetch, clone and push used to decide SSH-or-HTTPS on its own: "2 SSH fetches failed,
// using HTTPS for the rest" held for that one call only. On 2026/11/04 between 09:47 and
// 10:05 (a GitHub Git Operations incident) every fetch started on SSH again, failed twice
// (10-20 s), went to HTTPS and got through in about 2 s — then the next call did the same.
// What the last calls measured is the best predictor of the next one, so it is kept here.

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	// gitRouteSSHFailStreak: consecutive SSH failures (any call) before calls start on HTTPS.
	gitRouteSSHFailStreak = 2
	// gitRouteHTTPSFailStreak: consecutive HTTPS failures while preferring HTTPS before SSH
	// is tried first again — HTTPS is not assumed to be better, only measured to be.
	gitRouteHTTPSFailStreak = 3
	// gitRouteRetrySSHAfter: how long calls start on HTTPS before SSH is measured again.
	gitRouteRetrySSHAfter = 10 * time.Minute
)

var gitRouteNow = time.Now // a variable for tests

type gitRouteState struct {
	mu          sync.Mutex
	sshStreak   int // consecutive SSH failures
	httpsStreak int // consecutive HTTPS failures
	httpsUntil  time.Time
}

var gitRoutes = &gitRouteState{}

// gitRouteStartSSH reports whether a git network step should start on SSH. sshAvailable is
// whether a deploy key exists at all.
func gitRouteStartSSH(sshAvailable bool) bool {
	if !sshAvailable {
		return false
	}
	gitRoutes.mu.Lock()
	defer gitRoutes.mu.Unlock()
	return !gitRouteNow().Before(gitRoutes.httpsUntil)
}

// gitRouteNote records the outcome of one attempt on one transport.
func gitRouteNote(ssh, ok bool) {
	gitRoutes.mu.Lock()
	defer gitRoutes.mu.Unlock()
	now := gitRouteNow()
	switch {
	case ssh && ok:
		if !gitRoutes.httpsUntil.IsZero() {
			log.Printf("git_route: SSH works again — calls start on SSH")
		}
		gitRoutes.sshStreak, gitRoutes.httpsUntil = 0, time.Time{}
	case ssh && !ok:
		gitRoutes.sshStreak++
		if gitRoutes.sshStreak >= gitRouteSSHFailStreak && !now.Before(gitRoutes.httpsUntil) {
			gitRoutes.httpsUntil = now.Add(gitRouteRetrySSHAfter)
			gitRoutes.httpsStreak = 0
			log.Printf("git_route: SSH failed %d times in a row — fetches, clones and pushes start on HTTPS until %s",
				gitRoutes.sshStreak, gitRoutes.httpsUntil.Format("15:04:05"))
		}
	case !ssh && ok:
		gitRoutes.httpsStreak = 0
	case !ssh && !ok:
		gitRoutes.httpsStreak++
		if gitRoutes.httpsStreak >= gitRouteHTTPSFailStreak && now.Before(gitRoutes.httpsUntil) {
			log.Printf("git_route: HTTPS failed %d times in a row too — calls start on SSH again", gitRoutes.httpsStreak)
			gitRoutes.httpsUntil, gitRoutes.sshStreak, gitRoutes.httpsStreak = time.Time{}, 0, 0
		}
	}
}

// gitRouteReset is for tests.
func gitRouteReset() {
	gitRoutes.mu.Lock()
	defer gitRoutes.mu.Unlock()
	gitRoutes.sshStreak, gitRoutes.httpsStreak, gitRoutes.httpsUntil = 0, 0, time.Time{}
}

// ---- "Permission denied (publickey)" for a key that works ---------------------------------
//
// 11/04 09:44-10:16 the log held a dozen five-line "Permission denied (publickey)" blocks for
// the deploy key that pushed at 09:35, 09:52, 10:04 and 10:16: GitHub's key check was failing,
// not the key. A refusal reads the same either way, so the kernel says which it is from what
// it measured: whether this key has been accepted in this run.

var (
	sshKeyMu       sync.Mutex
	sshKeyAccepted time.Time // last time GitHub accepted the deploy key in this run
)

// gitSSHDenied: the output is GitHub refusing the SSH key.
func gitSSHDenied(out string) bool {
	return strings.Contains(strings.ToLower(out), "permission denied (publickey)")
}

func noteSSHKeyAccepted() {
	sshKeyMu.Lock()
	sshKeyAccepted = gitRouteNow()
	sshKeyMu.Unlock()
}

// sshDenialNote is the one line logged for a refusal instead of git's five.
func sshDenialNote() string {
	sshKeyMu.Lock()
	at := sshKeyAccepted
	sshKeyMu.Unlock()
	if at.IsZero() {
		return "GitHub refused the deploy key (not accepted yet in this run: if this persists, check the key is added to the repo with write access)"
	}
	return fmt.Sprintf("GitHub refused the deploy key it accepted at %s — a GitHub-side failure, not the key", at.Format("15:04:05"))
}
