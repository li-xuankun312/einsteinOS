package kernel

import (
	"os"
	"path/filepath"
	"testing"
)

// What TestMain holds the tests to: a file written into the state directory, or one removed
// from it, is reported; an untouched directory reports nothing. 11/05 the tests had written
// Niko1221/Strata's cursor into the real ~/.einsteinOS/cursor.json.
func TestStateChangesAreReported(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "keep.json"), []byte("{}"), 0o644)
	before := stateSnapshot(d)
	if c := stateChanges(before, stateSnapshot(d)); len(c) != 0 {
		t.Fatalf("untouched: %v", c)
	}
	os.WriteFile(filepath.Join(d, "cursor.json"), []byte(`{"Niko1221/Strata":1}`), 0o644)
	os.Remove(filepath.Join(d, "keep.json"))
	c := stateChanges(before, stateSnapshot(d))
	if len(c) != 2 || c[0] != "created "+filepath.Join(d, "cursor.json") || c[1] != "removed "+filepath.Join(d, "keep.json") {
		t.Fatalf("changes: %v", c)
	}
	if len(stateSnapshot(filepath.Join(d, "missing"))) != 0 {
		t.Fatal("a missing directory holds nothing")
	}
}
