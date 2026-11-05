package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 11/05 02:47 a fix ended and then failed the kernel's port gate; at 02:55 one ended on an
// exit code a pipe had taken from tail. The kernel's verdict is given at the done line.
func TestTheDoneLineIsCheckedByTheKernelsOwnVerification(t *testing.T) {
	if ran, _, _ := PreVerify("dev_unit_x"); ran {
		t.Fatal("nothing registered: nothing runs")
	}
	fail := UnitVerifyOutcome{Verified: true, Result: VerifyResult{Compile: "pass", Smoke: "pass", Gate: "port_incomplete",
		GateText: "port_incomplete: 2 of the 86 definitions have no counterpart:\n  src/x.cu k1 …"}}
	RegisterPreVerify("dev_unit_x", func() (bool, string) { return false, preVerifyReport(fail) })
	ran, ok, rep := PreVerify("dev_unit_x")
	if !ran || ok || !strings.Contains(rep, "gate=port_incomplete") || !strings.Contains(rep, "2 of the 86") || !strings.Contains(rep, "not done until this passes") {
		t.Fatalf("%v %v %s", ran, ok, rep)
	}
	UnregisterPreVerify("dev_unit_x")
	if ran, _, _ := PreVerify("dev_unit_x"); ran {
		t.Fatal("unregistered")
	}

	m := t.TempDir()
	os.WriteFile(filepath.Join(m, "a.cu"), []byte("x"), 0o644)
	d1 := deltaDigest(m, []string{"a.cu"}, nil)
	if d1 != deltaDigest(m, []string{"a.cu"}, nil) {
		t.Fatal("stable")
	}
	os.WriteFile(filepath.Join(m, "a.cu"), []byte("y"), 0o644)
	if d1 == deltaDigest(m, []string{"a.cu"}, nil) {
		t.Fatal("a changed file changes the digest: the verification is not reused")
	}
}

// the check is registered for as long as the conversation runs, not until the fork returns
func TestThePreVerifyCheckOutlivesTheForkCall(t *testing.T) {
	src, err := os.ReadFile("dev_run.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	fork := strings.Index(s, "child, ferr = ForkWithTermination(prompt")
	if fork < 0 || !strings.Contains(s[fork:], "Wait(child)\n\t\tUnregisterPreVerify(role)") {
		t.Fatal("UnregisterPreVerify must come after Wait(child)")
	}
}
