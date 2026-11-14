package kernel

import (
	"strings"
	"testing"

	"google.golang.org/adk/v2/include"
)

// Before Init there is no current task to copy: a fork is refused with an error instead of
// dereferencing nil (11/09: the eval tests run on their own panicked in CopyProcess).
func TestAForkBeforeBootIsRefusedNotAPanic(t *testing.T) {
	saved := include.Current
	include.Current = nil
	defer func() { include.Current = saved }()
	_, err := ForkExec("true", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "copy_process failed") {
		t.Fatalf("want the fork refused, got %v", err)
	}
}
