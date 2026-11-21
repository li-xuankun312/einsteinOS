package claudeweb

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Every test passes when it is the only one run. 11/09 four kernel tests passed in the
// package run only because an earlier test had booted the kernel, and each panicked on its
// own — an order the package run happened to provide, not one the tests established. Each
// test is run alone through this test binary, one after another. -short skips it.
func TestEveryTestPassesOnItsOwn(t *testing.T) {
	if testing.Short() || os.Getenv("EINSTEINOS_TEST_ALONE") != "" {
		t.Skip("one test of a run on its own")
	}
	out, err := exec.Command(os.Args[0], "-test.list", ".*").Output()
	if err != nil {
		t.Fatalf("listing the tests: %v", err)
	}
	for _, name := range strings.Fields(string(out)) {
		if !strings.HasPrefix(name, "Test") || name == "TestEveryTestPassesOnItsOwn" {
			continue
		}
		cmd := exec.Command(os.Args[0], "-test.run", "^"+name+"$", "-test.count=1")
		cmd.Env = append(os.Environ(), "EINSTEINOS_TEST_ALONE=1")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s fails on its own (%v):\n%s", name, err, aloneTail(string(b)))
		}
	}
}

// aloneTail: the end of a failing run's output, where the failure is.
func aloneTail(s string) string {
	if len(s) > 1500 {
		return "…" + s[len(s)-1500:]
	}
	return s
}
