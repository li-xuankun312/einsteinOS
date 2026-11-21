package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 11/20 20:44 a pip install --user filled /root/.local and 11/21 03:01 a script the kernel ran
// rewrote corex's vllm registry.py. A unit whose build.sh or test.sh changes this machine's
// Python packages does not pass when nothing else ran here meanwhile; when a conversation's
// command ran beside the verification, the change is logged and not laid on the unit.
func TestAUnitsVerificationThatChangesTheMachinesPackages(t *testing.T) {
	bootKernel()
	pkgs := t.TempDir()
	old := time.Now().Add(-time.Hour)
	os.Chtimes(pkgs, old, old)
	oldDirs := machinePackageDirs
	machinePackageDirs = func() []string { return []string{pkgs} }
	defer func() { machinePackageDirs = oldDirs }()

	ws := t.TempDir()
	build, test := devVerifyScripts("umach")
	write(t, ws, build, "true")
	write(t, ws, test, "true")
	withRunner(t, func(cmd string) (int, string) {
		if strings.Contains(cmd, "test.sh") {
			os.WriteFile(filepath.Join(pkgs, "tokenizers-0.23.3.dist-info"), []byte("x"), 0o644)
		}
		return 0, "ok"
	})
	r := runUnitVerify(ws, "umach", "", time.Minute, time.Minute)
	if r.Gate != "verify_modifies_machine" || !strings.Contains(r.GateText, "tokenizers-0.23.3.dist-info") {
		t.Fatalf("a change with nothing beside passed: %q %s", r.Gate, r.GateText)
	}

	// a conversation's command runs beside the verification: not the unit's to answer for
	os.Remove(filepath.Join(pkgs, "tokenizers-0.23.3.dist-info"))
	os.Chtimes(pkgs, old, old)
	pid, err := ForkExec("sleep 1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r = runUnitVerify(ws, "umach", "", time.Minute, time.Minute)
	Wait(pid)
	if r.Gate == "verify_modifies_machine" {
		t.Fatalf("a change beside a conversation's command was laid on the unit: %s", r.GateText)
	}
}
