package kernel

import (
	"strings"
	"testing"
)

// 11/03 06:45 and 08:22: cookie='eyJ…' and CSRF='YFbn…' had no prefix a pattern knew and
// went out as they were. Values the kernel holds are replaced wherever they appear.
func TestKnownCredentialsAreRedactedByValueInAnyShape(t *testing.T) {
	old := knownSecretValuesFn
	defer func() { knownSecretValuesFn = old }()
	knownSecretValuesFn = func() []string {
		return []string{"eyJicm93c2VyX2FwaV9jc3JmSECRET.sig.sig", "YFbnSECRETCSRF1234", "short"}
	}
	for _, in := range []string{
		"cookie='eyJicm93c2VyX2FwaV9jc3JmSECRET.sig.sig'",
		"CSRF='YFbnSECRETCSRF1234' ; headers={'x-browser-api-csrf': CSRF}",
		`{"c":"eyJicm93c2VyX2FwaV9jc3JmSECRET.sig.sig"}`,
	} {
		out := redactSecrets(in)
		if strings.Contains(out, "SECRET") {
			t.Fatalf("leaked: %s", out)
		}
	}
	if out := redactSecrets("a short word"); out != "a short word" {
		t.Fatalf("short values are not replaced: %s", out)
	}
}
