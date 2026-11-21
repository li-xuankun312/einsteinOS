package kernel

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 11/20 01:30:38–01:51:53 the integration's evaluation built the reference for 21 minutes and
// wrote nothing; from 01:51:53 to the Ctrl-C at 02:49 the search ran and no line said that the
// evaluation was waiting on it. Each step of the evaluation — the reference's build, the
// search, the end-to-end run — is logged when it starts and when it ends, with how long it took.
func TestEachStepOfTheEvaluationIsLogged(t *testing.T) {
	bootKernel()
	work := t.TempDir()
	ref := filepath.Join(work, "Strata")
	os.MkdirAll(ref, 0o755)
	os.WriteFile(filepath.Join(ref, "README.md"), []byte("strata\n"), 0o644)
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
	oldFmt, oldAsk, oldModel := refBareDirFmt, evalAskFn, evalModelDir
	defer func() { refBareDirFmt, evalAskFn, evalModelDir = oldFmt, oldAsk, oldModel }()
	refBareDirFmt = filepath.Join(work, "ref_%[1]s_%[2]s.git")
	git(work, "clone", "-q", "--bare", ref, refBareDir("o/Strata"))
	evalAskFn = func(string) (string, error) { return "", nil }
	evalModelDir = t.TempDir()

	var buf syncBuf
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, ".einsteinOS/verify/uint2"), 0o755)
	os.WriteFile(filepath.Join(ws, ".einsteinOS/verify/uint2/e2e.sh"), []byte("exit 4\n"), 0o755)
	fn := evalIntegration(0, "o/Strata", sha, "o/nodev", "uint2", nil, func(s string) string { return s })
	if k, _ := fn(ws, nil); k != "integration_e2e_failed" {
		t.Fatalf("gate %q", k)
	}
	out := buf.String()
	short := shortSHA(sha)
	// a run that is not yet the port's: the reference is not built and nothing is searched
	for _, want := range []string{
		"eval: o/Strata " + short + ": running .einsteinOS/verify/uint2/e2e.sh on the model",
		"eval: o/Strata " + short + ": .einsteinOS/verify/uint2/e2e.sh exited 4 in ",
		"eval: o/Strata " + short + `: .einsteinOS/verify/uint2/e2e.sh is not yet the port's run on the model (gate="integration_e2e_failed") — the reference is not built and nothing is searched`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "building the reference") || strings.Contains(out, "searching for repositories") {
		t.Fatalf("the reference was built or the search run for a failed end-to-end run:\n%s", out)
	}
	// the reference's build and the search, for a run that is the port's: each step logged
	buf = syncBuf{}
	log.SetOutput(&buf)
	evalBaseFor("o/Strata", sha, nil, nil)
	for _, want := range []string{
		"eval: o/Strata " + short + ": building the reference",
		"eval: o/Strata " + short + ": the reference built and run in ",
		"eval: o/Strata " + short + ": searching for repositories to compare with",
		"eval: o/Strata " + short + ": the search ended in ",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("no %q in\n%s", want, buf.String())
		}
	}
	buf = syncBuf{}
	log.SetOutput(&buf)
	evalBaseFor("o/Strata", sha, nil, nil)
	if !strings.Contains(buf.String(), "eval: o/Strata "+short+": the reference and the search were measured before — reused") {
		t.Fatalf("second verification:\n%s", buf.String())
	}
}
