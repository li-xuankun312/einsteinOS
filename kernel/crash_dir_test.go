package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 11/24 12:14 the machine went down as test.sh was to run, and the launch log was in /tmp: with
// EINSTEINOS_CRASH_DIR set, the test stage is noted there, synced, before the test runs
func TestATestStageIsNotedWhereItOutlivesTheMachine(t *testing.T) {
	d := t.TempDir()
	t.Setenv("EINSTEINOS_CRASH_DIR", d)
	noteCrashStage("uint", ".einsteinOS/verify/uint/test.sh", filepath.Join(d, "einsteinos-launch-1.log"))
	b, err := os.ReadFile(filepath.Join(d, "einsteinos-stages.log"))
	if err != nil || !strings.Contains(string(b), "unit uint: .einsteinOS/verify/uint/test.sh starts; its kernel launches go to "+d) {
		t.Fatalf("%q %v", b, err)
	}
	t.Setenv("EINSTEINOS_CRASH_DIR", filepath.Join(d, "absent"))
	if crashDir() != "" {
		t.Fatal("a directory that is not there was used")
	}
}
