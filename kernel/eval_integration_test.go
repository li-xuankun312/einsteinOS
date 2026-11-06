package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// End to end: the reference's own benchmark is run from its clone at the commit, the port's
// counterpart in the workspace, and a slower port fails the integration — through the same
// hook verification calls.
func TestTheIntegrationIsJudgedAgainstTheReferenceRunHere(t *testing.T) {
	work := t.TempDir()
	ref := filepath.Join(work, "Strata")
	os.MkdirAll(filepath.Join(ref, "bench"), 0o755)
	os.WriteFile(filepath.Join(ref, "bench/decode.sh"), []byte("#!/bin/bash\necho 'strata decode: 50 tok/s'\n"), 0o755)
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

	benches, _ := evalBenchmarks(refBareDir("Niko1221/Strata"), sha)
	mask := newRefMask(refMaskIdent, "Niko1221/Strata").Replace
	var shown []EvalBench
	for _, b := range benches {
		shown = append(shown, EvalBench{File: mask(b.File), Kind: b.Kind, Name: mask(b.Name)})
	}
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, "bench"), 0o755)
	port := func(v string) {
		os.WriteFile(filepath.Join(ws, "bench/decode.sh"), []byte("#!/bin/bash\necho '"+mask("strata")+" decode: "+v+" tok/s'\n"), 0o755)
	}
	RegisterIntegrationEval("uintegration", evalIntegration("Niko1221/Strata", sha, "o/nodev", benches, mask))
	defer UnregisterIntegrationEval("uintegration")

	port("40")
	if k, txt := integrationEval("uintegration")(ws, shown); k != "integration_below_reference" || !strings.Contains(txt, "50") {
		t.Fatalf("slower than the reference: %q %s", k, txt)
	}
	port("55")
	if k, txt := integrationEval("uintegration")(ws, shown); k != "" {
		t.Fatalf("faster than the reference: %q %s", k, txt)
	}
}
