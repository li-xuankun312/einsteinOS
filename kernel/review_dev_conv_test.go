package kernel

import (
	"strings"
	"testing"
)

// The reviewer is given the developer's conversation as downloaded — the sed that widened a
// tolerance is in it verbatim — as a file in its sandbox, with its path in the prompt; no
// conversation, no file.
func TestTheReviewerGetsTheDevConversationAsAFile(t *testing.T) {
	old := downloadConvFn
	defer func() { downloadConvFn = old }()
	var asked string
	downloadConvFn = func(id string) (string, error) {
		asked = id
		return "## assistant\\n[bash] sed -i '380,404s/, 5e-3, 2e-3, /, 5e-3, 0.5, /' tests/k47_u4de/test_u4de.cu\\n", nil
	}
	f, ok := reviewDevConvFile("Niko1221/Strata", map[string]string{"dev_conv": "295e1eba-e24d-4072", "unit_id": "u4decb4e657"})
	if !ok || asked != "295e1eba-e24d-4072" || f.Name != "dev_conversation_u4decb4e657.md" || !strings.Contains(string(f.Data), "0.5, /") {
		t.Fatalf("%v %q %+v", ok, asked, f)
	}
	if !strings.Contains(fmtReview(f), "/mnt/user-data/uploads/dev_conversation_u4decb4e657.md") {
		t.Fatal("path not in the prompt line")
	}
	if _, ok := reviewDevConvFile("o/r", map[string]string{"unit_id": "u1"}); ok {
		t.Fatal("no conversation, no file")
	}
	SetForkFiles([]ForkFile{f})
	if got := ConsumeForkFiles(); len(got) != 1 || ConsumeForkFiles() != nil {
		t.Fatal("the next fork takes the files once")
	}
}

func fmtReview(f ForkFile) string { return strings.ReplaceAll(reviewDevConvFmt, "%[1]s", localUploadsPath(f.Name)) }
