package claudeweb

import (
	"os"
	"path/filepath"
	"testing"
)

// einsteinOS's source tree is not read here, wherever below it the kernel was started.
// 11/14 20:50 a dev conversation ran "cat /home/adk-go/auth/credential.go" here.
func TestEinsteinOSsSourceTreeIsNotReadFromBelowIt(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte(einsteinOSModule+"/v2\n\ngo 1.24\n"), 0o644)
	sub := filepath.Join(root, "examples", "claudeweb")
	os.MkdirAll(sub, 0o755)
	old, _ := os.Getwd()
	defer os.Chdir(old)
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{
		"cat " + filepath.Join(root, "auth", "credential.go") + " 2>/dev/null | head -60",
		"ls " + root + "/ 2>/dev/null | head -20",
	} {
		if touchesKernel(c, "/home/claude/baloon") == "" {
			t.Errorf("run here: %q", c)
		}
	}
	if w := touchesKernel("cat /home/claude/baloon/src/core/session.cpp", "/home/claude/baloon"); w != "" {
		t.Errorf("the unit's own mirror refused (%s)", w)
	}
}
