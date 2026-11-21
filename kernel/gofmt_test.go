package kernel

import (
	"bytes"
	"go/format"
	"os"
	"path/filepath"
	"testing"
)

// Every Go file of the package is as gofmt writes it, so a commit that leaves a file
// unformatted fails here rather than being found by a replay — 11/09 the replay of
// e4b9c4e4..30b49343 found a065ef22 and 35680c2a had each left one.
func TestEveryFileIsGofmted(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out, err := format.Source(src)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if !bytes.Equal(src, out) {
			t.Errorf("%s is not gofmt'd", f)
		}
	}
}
