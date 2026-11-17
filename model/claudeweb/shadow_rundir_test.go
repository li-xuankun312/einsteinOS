package claudeweb

import (
	"strings"
	"testing"

	"google.golang.org/adk/v2/kernel"
)

// 11/17 the kernel ran from /home/adk-go with no CLAUDE_WEB_WORKDIR: the shadow's working
// directory was einsteinOS's own tree, and a conversation's commands ran inside it. They run
// from the sandbox's home instead.
func TestCommandsDoNotRunInsideEinsteinOSsOwnTree(t *testing.T) {
	kernelInitOnce.Do(kernel.Init)
	roots := einsteinOSRoots()
	if len(roots) == 0 {
		t.Skip("not run from inside einsteinOS's tree")
	}
	home := t.TempDir()
	old := sandboxHome
	sandboxHome = home
	defer func() { sandboxHome = old }()

	s := &ShadowExecutor{WorkDir: roots[0], Enabled: true}
	if d := s.runDir(); d != home {
		t.Fatalf("runs from %s, want %s", d, home)
	}
	out := s.Execute("bash_tool", `{"command":"pwd"}`)
	if strings.Contains(out, roots[0]) || !strings.Contains(out, home) {
		t.Fatalf("pwd here: %q", out)
	}
	// a working directory outside einsteinOS's tree is kept
	other := t.TempDir()
	if d := (&ShadowExecutor{WorkDir: other}).runDir(); d != other {
		t.Fatalf("%s moved to %s", other, d)
	}
}
