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

// A value held under a secret's name in this process's environment is masked, whatever its
// shape: 11/13 07:07:29 a dev conversation ran env | grep TOKEN on this machine.
func TestASecretFromTheEnvironmentIsRedacted(t *testing.T) {
	t.Setenv("VIKINGDB_API_KEY", "vk-AbC123dEf456GhI789")
	t.Setenv("SOME_SERVICE_TOKEN", "plainvalue-0123456789xyz")
	t.Setenv("EINSTEIN_PLAIN_SETTING", "not-a-secret-value-123")
	out := RedactSecrets("VIKINGDB_API_KEY=vk-AbC123dEf456GhI789\nSOME_SERVICE_TOKEN=plainvalue-0123456789xyz\nEINSTEIN_PLAIN_SETTING=not-a-secret-value-123")
	if strings.Contains(out, "vk-AbC123dEf456GhI789") || strings.Contains(out, "plainvalue-0123456789xyz") {
		t.Fatalf("a secret reached the output: %q", out)
	}
	if !strings.Contains(out, "not-a-secret-value-123") {
		t.Fatalf("an ordinary setting is left as it is: %q", out)
	}
}
