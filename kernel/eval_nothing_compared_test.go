package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An integration whose run was compared with nothing does not pass, and the run stops on it
// as on a machine gate: no conversation on the unit can give it something to be compared
// with. 11/09 and 11/10 the integration passed on an empty verdict — the reference shipped no
// runnable benchmark, the searched repository failed its checkout, the planner found none of
// 68 published numbers comparable.
func TestNothingComparedIsNotAPass(t *testing.T) {
	bootKernel() // the evaluation forks its builds through the kernel's process table
	work := t.TempDir()
	ref := filepath.Join(work, "Strata")
	os.MkdirAll(filepath.Join(ref, "bench/results"), 0o755)
	os.WriteFile(filepath.Join(ref, "bench/results/notes.txt"), []byte("results only, no code\n"), 0o644)
	git := func(dir string, args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(ref, "init", "-q")
	git(ref, "add", "-A")
	git(ref, "commit", "-qm", "c0")
	sha := git(ref, "rev-parse", "HEAD")
	oldFmt, oldAsk := refBareDirFmt, evalAskFn
	defer func() { refBareDirFmt, evalAskFn = oldFmt, oldAsk }()
	refBareDirFmt = filepath.Join(work, "ref_%[1]s_%[2]s.git")
	git(work, "clone", "-q", "--bare", ref, refBareDir("Niko1221/Strata"))
	evalAskFn = func(string) (string, error) { return "", nil }

	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, ".einsteinOS/verify/uint"), 0o755)
	os.WriteFile(filepath.Join(ws, ".einsteinOS/verify/uint/e2e.sh"), []byte("echo 'prefill: 57.54 tok/s'; echo 'decode: 11.20 tok/s'\n"), 0o755)
	mask := newRefMask(refMaskIdent, "Niko1221/Strata").Replace
	fn := evalIntegration(0, "Niko1221/Strata", sha, "o/nodev", "uint", nil, mask)
	k, txt := fn(ws, nil)
	if k != GateNothingCompared || !strings.Contains(txt, "first integrated commit") || !strings.Contains(txt, "0 benchmark(s) run") {
		t.Fatalf("nothing to compare with: %q %s", k, txt)
	}
	if !gateOutsideUnit(k) || gateIsMachine(k) || !preverifyMayEnd(false, k) {
		t.Fatal("the conversation ends and the run stops, not as a fact about the machine")
	}
}
