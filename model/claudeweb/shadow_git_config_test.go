package claudeweb

import "testing"

// 11/18 dev_fix_1 of uebd9b81357 and of u02c65ed515 read git's and gh's configuration here
// looking for a token: the commands as they ran here are not run here now. git config run by
// the conversation in its own repository stays its own.
func TestGitsAndGhsConfigurationAreNotReadHere(t *testing.T) {
	for _, c := range []string{
		"find /home/claude/.config /root/.config 2>/dev/null -type f | head -20\ncat /home/claude/.config/gh/hosts.yml 2>/dev/null",
		"ls /usr/local/corex/bin/ 2>/dev/null | head -5\ncat ~/.gitconfig",
		"find /root /home -name \".gitconfig\" 2>/dev/null | xargs grep -l \"credential\\|token\\|url\" 2>/dev/null | head -3",
	} {
		if touchesKernel(c, "/home/claude") == "" {
			t.Fatalf("ran here: %q", c)
		}
	}
	if p := touchesKernel("cd /home/claude/baloon && git config user.email x@y", "/home/claude"); p != "" {
		t.Fatalf("the conversation's own git config is not a kernel place: %q", p)
	}
}
