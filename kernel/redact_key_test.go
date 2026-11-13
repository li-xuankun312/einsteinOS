package kernel

import (
	"strings"
	"testing"
)

// A private key printed on this machine is masked, whole or cut off: 11/13 04:52:57 a dev
// conversation ran cat ~/.ssh/einsteinos_deploy here.
func TestAPrivateKeyIsRedacted(t *testing.T) {
	key := "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQ\nAAAAMwAAAAtzc2gtZWQyNTUxOQ\n-----END OPENSSH PRIVATE KEY-----"
	for _, in := range []string{"key:\n" + key + "\ndone", "key:\n" + key[:90]} {
		out := RedactSecrets(in)
		if strings.Contains(out, "b3BlbnNzaC1rZXktdjEA") || !strings.Contains(out, "<private key redacted>") {
			t.Fatalf("%q -> %q", in, out)
		}
	}
	if got := RedactSecrets("done"); got != "done" {
		t.Fatal(got)
	}
}
