package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The end-to-end run at a commit must not be byte for byte the program the previous
// integration ran when the reference changed files the port maps cover. 11/10 commit
// e5509707's run printed the same 64 generated tokens as 6801c77f's on 11/09: its
// src/model/generate_model.cu had not changed, and "compared with the integration before"
// set the program against itself.
func TestTheEndToEndProgramMustFollowTheReference(t *testing.T) {
	bootKernel() // the run is forked through the kernel's process table
	work := t.TempDir()
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
	// the reference: one commit changes a ported kernel, the next only its README
	ref := filepath.Join(work, "Strata")
	os.MkdirAll(filepath.Join(ref, "src/kernels/cuda"), 0o755)
	os.WriteFile(filepath.Join(ref, "src/kernels/cuda/ple.cu"), []byte("// v1\n"), 0o644)
	os.WriteFile(filepath.Join(ref, "README.md"), []byte("v1\n"), 0o644)
	git(ref, "init", "-q")
	git(ref, "add", "-A")
	git(ref, "commit", "-qm", "c1")
	c1 := git(ref, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(ref, "src/kernels/cuda/ple.cu"), []byte("// v2\n"), 0o644)
	git(ref, "commit", "-qam", "c2")
	c2 := git(ref, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(ref, "README.md"), []byte("v2\n"), 0o644)
	git(ref, "commit", "-qam", "c3")
	c3 := git(ref, "rev-parse", "HEAD")
	oldFmt := refBareDirFmt
	defer func() { refBareDirFmt = oldFmt }()
	refBareDirFmt = filepath.Join(work, "ref_%[1]s_%[2]s.git")
	git(work, "clone", "-q", "--bare", ref, refBareDir("Niko1221/Strata"))

	// the port: a port map covering ple.cu, and the end-to-end program, an executable of the
	// workspace that runs long enough to be seen in /proc
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, ".einsteinOS/port"), 0o755)
	os.WriteFile(filepath.Join(ws, PortMapPath("u41a85163f4")), []byte("src/kernels/cuda/ple.cu\tconv_kernel\tsrc/kernels/cuda/ple.cu\tconv_kernel\n"), 0o644)
	os.MkdirAll(filepath.Join(ws, "build"), 0o755)
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	b, _ := os.ReadFile(sleep)
	os.WriteFile(filepath.Join(ws, "build/generate_model"), b, 0o755)
	os.MkdirAll(filepath.Join(ws, ".einsteinOS/verify/uint"), 0o755)
	os.WriteFile(filepath.Join(ws, ".einsteinOS/verify/uint/e2e.sh"), []byte("build/generate_model 0.4\necho 'prefill: 57.54 tok/s'; echo 'decode: 11.20 tok/s'\n"), 0o755)
	oldLib := e2eProbeLibFn
	defer func() { e2eProbeLibFn = oldLib }()
	e2eProbeLibFn = func() (string, error) { return "", os.ErrNotExist }

	at1 := evalRunE2E(ws, "uint")
	if at1.Programs["build/generate_model"] == "" {
		t.Fatalf("the program the run executed is not recorded: %v", at1.Programs)
	}
	at2 := evalRunE2E(ws, "uint")
	prev := &EvalRecord{RefSHA: c1, E2E: at1}
	if k, txt := e2eUnchangedSince("Niko1221/Strata", c2, ws, at2, prev); k != "integration_e2e_unchanged" || !strings.Contains(txt, "src/kernels/cuda/ple.cu") || !strings.Contains(txt, "build/generate_model") {
		t.Fatalf("same program, the reference changed a ported file: %q %s", k, txt)
	}
	if k, txt := e2eUnchangedSince("Niko1221/Strata", c3, ws, at2, &EvalRecord{RefSHA: c2, E2E: at1}); k != "" {
		t.Fatalf("only the README changed: %q %s", k, txt)
	}
	b = append(b, 0) // another build of the program
	os.WriteFile(filepath.Join(ws, "build/generate_model"), b, 0o755)
	if k, txt := e2eUnchangedSince("Niko1221/Strata", c2, ws, evalRunE2E(ws, "uint"), prev); k != "" {
		t.Fatalf("the program changed: %q %s", k, txt)
	}
}
