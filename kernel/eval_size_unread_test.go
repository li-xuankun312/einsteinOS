package kernel

import (
	"errors"
	"strings"
	"testing"
)

// 11/21 07:06:41 ggerganov/llama.cpp was cloned and taken with its size unread. A size that
// cannot be read does not let the clone happen; a size read under the limit does.
func TestARepositoryWhoseSizeCannotBeReadIsNotCloned(t *testing.T) {
	old := evalSizeFn
	defer func() { evalSizeFn = old }()
	evalSizeFn = func(owner, name string) (int64, error) { return 0, errors.New("GitHub answered 403") }
	if why := evalTooLarge("ggerganov", "llama.cpp"); !strings.Contains(why, "could not be read") || !strings.Contains(why, "403") {
		t.Fatalf("an unread size let the clone happen: %q", why)
	}
	evalSizeFn = func(owner, name string) (int64, error) { return 487 * 1024, nil }
	if why := evalTooLarge("ggerganov", "llama.cpp"); !strings.Contains(why, "too large") {
		t.Fatalf("487 MiB: %q", why)
	}
	evalSizeFn = func(owner, name string) (int64, error) { return 30 * 1024, nil }
	if why := evalTooLarge("turboderp", "exllamav2"); why != "" {
		t.Fatalf("30 MiB was refused: %q", why)
	}
}
