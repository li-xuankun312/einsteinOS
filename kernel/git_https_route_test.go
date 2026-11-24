package kernel

import (
	"strings"
	"testing"
)

// 11/24 08:39:15 every HTTPS attempt of the rebuilt machine took its proxy, which hung: with a
// proxy set, an HTTPS failure turns the next attempt to the other route, and a working route stays
func TestHTTPSGitTurnsToTheOtherRouteAfterAFailure(t *testing.T) {
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1080")
	gitHTTPS.Lock()
	gitHTTPS.direct = false
	gitHTTPS.Unlock()
	if gitHTTPSEnv() != "" || gitTransport(false) != "HTTPS through the proxy" {
		t.Fatal("starts through the proxy")
	}
	gitRouteNote(false, false)
	if !strings.HasPrefix(gitHTTPSEnv(), "env -u ALL_PROXY") || gitTransport(false) != "HTTPS directly" {
		t.Fatalf("%q", gitHTTPSEnv())
	}
	gitRouteNote(false, true)
	if gitTransport(false) != "HTTPS directly" {
		t.Fatal("a working route changed")
	}
	gitRouteNote(false, false)
	if gitHTTPSEnv() != "" {
		t.Fatal("back through the proxy after a direct failure")
	}
	t.Setenv("ALL_PROXY", "")
	if gitTransport(false) != "HTTPS" || gitHTTPSEnv() != "" {
		t.Fatal("no proxy, no route")
	}
}
