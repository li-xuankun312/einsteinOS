package claudeweb

import (
	"encoding/json"
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

// The commands of 11/17 10:21:37–10:22:01 (dev_planner, pids 65, 66, 76, 77) ran here with the
// working directory at /home/adk-go. einsteinOS's tree is a kernel place for them.
func TestEinsteinOSsTreeIsAKernelPlaceWhateverTheWorkingDirectory(t *testing.T) {
	kernelInitOnce.Do(kernel.Init)
	roots := einsteinOSRoots()
	if len(roots) == 0 {
		t.Skip("not run from inside einsteinOS's tree")
	}
	root := roots[0]
	home := t.TempDir()
	old := sandboxHome
	sandboxHome = home
	defer func() { sandboxHome = old }()
	s := &ShadowExecutor{WorkDir: root, Enabled: true}
	for _, cmd := range []string{
		"cat " + root + "/etc/einstein 2>/dev/null; echo \"---EINSTEIN_ETC---\"",
		"ls " + root + "/kernel/testdata/u617/ 2>/dev/null",
		"find " + root + " -name \"*.lean\" 2>/dev/null | head -20",
		"ls " + root + "/ 2>/dev/null; ls " + root + "/kernel/ 2>/dev/null | head -10",
	} {
		if out := s.Execute("bash_tool", jsonCommand(cmd)); out != "" {
			t.Fatalf("ran here: %q\n→ %q", cmd, out)
		}
	}
}

func jsonCommand(cmd string) string {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return string(b)
}
