package kernel

import (
	"strings"
	"testing"
	"time"
)

// A push from a directory that is not a repository stops at once: 11/13 05:57:54 it was
// retried 49 times over SSH and HTTPS.
func TestAPushFromNoRepositoryStopsAtOnce(t *testing.T) {
	start := time.Now()
	err := GitPushRetry(t.TempDir(), "o/r", "b", 90)
	if err == nil || !strings.Contains(err.Error(), "not retrying") {
		t.Fatalf("err=%v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %s", d)
	}
}
