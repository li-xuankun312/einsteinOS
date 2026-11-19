package kernel

import (
	"strings"
	"testing"
)

// Every git step the guard watches asks for progress: git prints nothing while it works when
// its output is not a terminal, and the guard kills a step that printed nothing for
// gitStartWindow. 11/19 14:54–15:12 the dev repo's fetches were killed at exactly 10 s,
// fifteen times in a row on both transports, while the same fetch by hand went through.
func TestEveryWatchedGitStepAsksForProgress(t *testing.T) {
	for _, ssh := range []bool{true, false} {
		if cmd := gitFetchCmd("/tmp/x", "o/r", ssh); !strings.Contains(cmd, "fetch --progress") {
			t.Fatalf("ssh=%v: %s", ssh, cmd)
		}
	}
	if !strings.Contains(gitFetchRefsFmt, "fetch --progress") {
		t.Fatalf("gitFetchRefs: %s", gitFetchRefsFmt)
	}
}
