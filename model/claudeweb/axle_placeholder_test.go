package claudeweb

import (
	"strings"
	"testing"

	"google.golang.org/adk/v2/kernel"
)

// 11/18 7ad91890 L14-15: the [AXLE] block of every conversation's first message carried the
// session cookie and CSRF in full, to the proxy that serves the conversation. The block now
// holds the placeholder, and the curl a conversation copies from it is sent here with the
// kernel's own credentials.
func TestAConversationIsToldHowToReachAxleWithoutTheCredentials(t *testing.T) {
	const sess = "eyJicm93c2VyX2FwaV9jc3JmIjogIllGYm5BZ2FQS3ZQdTgwR21KUFVaTkhWVzVHY0hfWF9ZYnhFZnhKcElDdU0ifQ==.atBFEQ.XT1vAvHEhv3QOhNsvZlJwDItLcQ"
	const csrf = "YFbnAgaPKvPu80GmJPUZNHVW5GcH_X_YbxEfxJpICuM"
	old := axleKernelCreds
	axleKernelCreds = func() (string, string) { return sess, csrf }
	defer func() { axleKernelCreds = old }()

	block := axleBlockFor()
	if strings.Contains(block, sess) || strings.Contains(block, csrf) || strings.Contains(block, "XT1vAvHEhv3Q") {
		t.Fatalf("the block carries the credentials:\n%s", block)
	}
	if !strings.Contains(block, kernel.AxleCredentialPlaceholder) {
		t.Fatalf("the credentials' places are not held:\n%s", block)
	}
	// the curl as the block gives it, copied by a conversation
	i := strings.Index(block, "curl ")
	j := strings.Index(block, "\nit answers")
	set, ok := extractAxleRequests(strings.ReplaceAll(block[i:j], "<theorem with sorry>", "theorem t : 1 = 1 := sorry"))
	if !ok {
		t.Fatalf("the block's curl is not read as an Axle request:\n%s", block[i:j])
	}
	fillAxleCredentials(&set)
	if set.Session != sess || set.CSRF != csrf {
		t.Fatalf("the copied request goes out with %q / %q, not the kernel's credentials", set.Session, set.CSRF)
	}
}
