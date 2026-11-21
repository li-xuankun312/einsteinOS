package kernel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteTokenFileIsPrivateAndIgnoresEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".gh_token")
	if writeTokenFile(p, "  \n") {
		t.Fatal("an empty token must not create the file")
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("file exists for an empty token")
	}
	if !writeTokenFile(p, "  tok-123\n") {
		t.Fatal("write failed")
	}
	b, _ := os.ReadFile(p)
	if string(b) != "tok-123" {
		t.Fatalf("token must be stored trimmed, got %q", b)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v, want 0600", st.Mode().Perm())
	}
	// an existing world-readable file is tightened, not left open
	os.Chmod(p, 0o644)
	writeTokenFile(p, "tok-456")
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode after rewrite %v", st.Mode().Perm())
	}
}
