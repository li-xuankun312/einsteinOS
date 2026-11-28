package claudeweb

import (
	"strings"
	"testing"
)

// 11/17 13:01:18 and 13:01:42 the log printed review_conv's curl with its Axle cookie in full
// ("-b 'eyJicm93c2VyX2FwaV9jc3Jm…'"): only "provertools_session=" and the CSRF header were
// redacted. A cookie given whole is redacted too.
func TestAnAxleCookieGivenWholeIsRedacted(t *testing.T) {
	const secret = "eyJicm93c2VyX2FwaV9jc3JmIjogIllGYm5BZ2FQS3ZQdTgwR21KUFVaTkhWVzVHY0hfWF9ZYnhFZnhKcElDdU0ifQ==.atBFEQ.XT1vAvHEhv3QOhNsvZlJwDItLcQ"
	for _, cmd := range []string{
		"curl -s https://axle.axiommath.ai/api/v1/verify_proof -H 'content-type: application/json' -b '" + secret + "' -H 'x-browser-api-csrf: abc'",
		"curl -s https://axle.axiommath.ai/api/v1/verify_proof --cookie \"" + secret + "\"",
		"curl -s https://axle.axiommath.ai/api/v1/verify_proof -b " + secret + " -d @p.json",
		"curl -s https://axle.axiommath.ai/api/v1/verify_proof -H 'Cookie: " + secret + "'",
	} {
		if got := redactAxle(cmd); strings.Contains(got, "XT1vAvHEhv3QOhNsvZlJwDItLcQ") || !strings.Contains(got, "<redacted>") {
			t.Fatalf("%q", got)
		}
	}
	if got := redactAxle("curl -s https://axle.axiommath.ai/api/v1/verify_proof -d @p.json"); got != "curl -s https://axle.axiommath.ai/api/v1/verify_proof -d @p.json" {
		t.Fatalf("a command with no secret changed: %q", got)
	}
}
