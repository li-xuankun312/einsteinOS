package kernel

import (
	"strings"
	"testing"
)

// The Axle facts a theory conversation and a proof repair are given hold the placeholder where
// the session and CSRF go; the machine that runs the command sends the kernel's own.
func TestTheAxleFactsHoldNoCredentials(t *testing.T) {
	oldS, oldC := axleSession, axleCSRF
	defer func() { SetAxleCredentials(oldS, oldC) }()
	SetAxleCredentials("eyJicm93c2VyX2FwaV9jc3JmIjogIllG.atBFEQ.XT1vAvHEhv3QOhNsvZlJwDItLcQ", "YFbnAgaPKvPu80GmJPUZNHVW5GcH_X_YbxEfxJpICuM")
	f := axleFacts()
	if strings.Contains(f, "XT1vAvHEhv3Q") || strings.Contains(f, "YFbnAgaPKvPu80") {
		t.Fatalf("the facts carry the credentials:\n%s", f)
	}
	if !strings.Contains(f, "provertools_session="+AxleCredentialPlaceholder) {
		t.Fatalf("the cookie's place is not held:\n%s", f)
	}
}
