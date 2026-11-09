package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests build the launch probe under their own temporary directory, not under the home
// directory of the machine they run on.
func TestTheTestsBuildTheProbeOutsideHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if d := launchProbeDir(); strings.HasPrefix(d, filepath.Join(home, ".einsteinOS")) {
		t.Fatalf("the probe is built in %s", d)
	}
}
