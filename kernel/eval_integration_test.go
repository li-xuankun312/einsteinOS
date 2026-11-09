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
	bootKernel() // the evaluation forks its builds through the kernel's process table
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
	RegisterIntegrationEval("uintegration", evalIntegration(0, "Niko1221/Strata", sha, "o/nodev", "uintegration", benches, mask))
	defer UnregisterIntegrationEval("uintegration")

	// the port's end-to-end run on the model
	os.MkdirAll(filepath.Join(ws, ".einsteinOS/verify/uintegration"), 0o755)
	e2e := func(body string) {
		os.WriteFile(filepath.Join(ws, ".einsteinOS/verify/uintegration/e2e.sh"), []byte(body), 0o755)
	}
	e2e("echo \"prefill   3562 tokens in 6611.2 ms  ->  538.75 tok/s\"\necho \"decode   256 tokens in 2706.4 ms  ->  94.59 tok/s\"\n")
	port("40")
	if k, txt := integrationEval("uintegration")(ws, shown); k != "integration_below_reference" || !strings.Contains(txt, "50") {
		t.Fatalf("slower than the reference: %q %s", k, txt)
	}
	port("55")
	if k, txt := integrationEval("uintegration")(ws, shown); k != "" {
		t.Fatalf("faster than the reference: %q %s", k, txt)
	}
}

// No end-to-end script, a failing one, or one that prints no decode rate fails the
// integration before any comparison.
func TestTheIntegrationRunsThePortOnTheModel(t *testing.T) {
	bootKernel() // the evaluation forks its builds through the kernel's process table
	ws := t.TempDir()
	if k, _ := e2eGate(evalRunE2E(ws, "u1")); k != "integration_e2e_missing" {
		t.Fatalf("missing: %q", k)
	}
	os.MkdirAll(filepath.Join(ws, ".einsteinOS/verify/u1"), 0o755)
	put := func(body string) {
		os.WriteFile(filepath.Join(ws, ".einsteinOS/verify/u1/e2e.sh"), []byte(body), 0o755)
	}
	old := evalModelDir
	defer func() { evalModelDir = old }()
	evalModelDir = "/tmp/some-model"
	put("echo \"model=$MODEL_DIR\"; exit 3\n")
	if k, txt := e2eGate(evalRunE2E(ws, "u1")); k != "integration_e2e_failed" || !strings.Contains(txt, "model=/tmp/some-model") {
		t.Fatalf("failed: %q %s", k, txt)
	}
	put("echo \"prefill  100 tokens in 10.0 ms  ->  10000 tok/s\"\n")
	if k, txt := e2eGate(evalRunE2E(ws, "u1")); k != "integration_e2e_unmeasured" || !strings.Contains(txt, "decode") {
		t.Fatalf("no decode: %q %s", k, txt)
	}
	put("echo \"prefill  100 tokens in 10.0 ms  ->  10000 tok/s\"\necho \"decode  10 tokens in 100.0 ms  ->  100 tok/s\"\n")
	if k, txt := e2eGate(evalRunE2E(ws, "u1")); k != "" {
		t.Fatalf("both rates: %q %s", k, txt)
	}
}
