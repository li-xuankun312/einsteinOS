package claudeweb

import "testing"

// 11/18 dev_fix_1 of uebd9b81357 (54005caf) read this machine's Kubernetes service account
// token: the commands as they ran here are not run here now.
func TestTheContainersMountedSecretsAreNotReadHere(t *testing.T) {
	for _, c := range []string{
		"# Check if there's a GH token available as a file\nfind /run/secrets /var/run/secrets 2>/dev/null -name \"*\" | head -10",
		"cat /run/secrets/kubernetes.io/serviceaccount/token 2>/dev/null | cut -c1-50",
		"TOKEN=$(cat /run/secrets/kubernetes.io/serviceaccount/token)\necho \"Length: ${#TOKEN}\"",
		"ls /var/run/secrets/kubernetes.io/serviceaccount/",
	} {
		if touchesKernel(c, "/home/claude") == "" {
			t.Fatalf("ran here: %q", c)
		}
	}
	if p := touchesKernel("ls /run/user/0", "/home/claude"); p != "" {
		t.Fatalf("an unrelated /run path is not a secret place: %q", p)
	}
}
