package claudeweb

import (
	"encoding/json"
	"os"
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

// 11/17 09:50:59 and 10:21:46: commands that read the kernel's own state ran here. The
// directories it keeps state in are kernel places; a unit's .einsteinOS in its mirror is not.
func TestTheKernelsStateDirectoriesAreKernelPlaces(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "/" {
		t.Skip("no home")
	}
	for _, cmd := range []string{
		"cat " + home + "/.einsteinos/baloon_u77b_official 2>/dev/null | head -40",
		"find " + home + "/.einsteinos " + home + "/.einsteinOS 2>/dev/null -type f | head -20",
		"cat ~/.einsteinos/conv_registry.json",
		"echo '{}' > $HOME/.einsteinOS/cursor.json",
	} {
		if what := touchesKernel(cmd, "/home/claude"); what == "" {
			t.Fatalf("not a kernel place: %q", cmd)
		}
	}
	t.Setenv("EINSTEINOS_CONV_REGISTRY", "/srv/state/conv_registry.json")
	if touchesKernel("cat /srv/state/conv_registry.json", "/home/claude") == "" {
		t.Fatal("the registry's directory set by EINSTEINOS_CONV_REGISTRY is not a kernel place")
	}
	for _, cmd := range []string{
		"cat /home/claude/baloon/.einsteinOS/port/u6561d2f27c.tsv",
		"ls /home/claude/baloon/.einsteinOS/verify/",
	} {
		if what := touchesKernel(cmd, "/home/claude"); what != "" {
			t.Fatalf("a unit's own .einsteinOS taken for the kernel's (%s): %q", what, cmd)
		}
	}
}

// 11/17 10:19:37 "cat /tmp/einsteinos_diag/einsteinOS_diag_4330757e.json" ran here; 11/15
// dev_unit_33 searched the kernel's verification trees. A conversation's own /tmp files are not
// the kernel's.
func TestTheKernelsTemporaryDirectoriesAreKernelPlaces(t *testing.T) {
	tmp := strings.TrimSuffix(os.TempDir(), "/")
	for _, cmd := range []string{
		"cat " + tmp + "/einsteinos_diag/einsteinOS_diag_4330757e.json",
		"find " + tmp + "/einsteinos_verify_284818074 -name locate.py",
		"ls " + tmp + "/einsteinos-e2e-launch-1.log",
	} {
		if touchesKernel(cmd, "/home/claude") == "" {
			t.Fatalf("not a kernel place: %q", cmd)
		}
	}
	for _, cmd := range []string{"cat " + tmp + "/FULLFIX.py", "ls " + tmp + "/wip_uc81_work/lean"} {
		if what := touchesKernel(cmd, "/home/claude"); what != "" {
			t.Fatalf("a conversation's own temp file taken for the kernel's (%s): %q", what, cmd)
		}
	}
}
