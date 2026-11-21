package kernel

import (
	"regexp"
	"strings"
	"testing"
)

func sampleDiag(text, prompt string) DiagRecord {
	return DiagRecord{
		Time: "2026-11-16T12:07:39Z", ConvID: "f1cac064-e9b2-4c99-9711-ada41f24a612", Role: "dev_fix_1", Exit: "error",
		Rounds: 4, Tools: 85, Continues: 1, Text: text,
		Request: NewDiagRequest("claude-sonnet-4-6", "medium", "continue", prompt),
	}
}

func TestDiagBranchNameHasAnEightHexSuffix(t *testing.T) {
	re := regexp.MustCompile(`^einsteinOS/diag_[0-9a-f]{8}$`)
	a, b := DiagBranchName(), DiagBranchName()
	if !re.MatchString(a) || !re.MatchString(b) || a == b {
		t.Fatalf("%q %q", a, b)
	}
}

func TestDiagRecordIsRedactedBeforeItLeaves(t *testing.T) {
	old := knownSecretValuesFn
	defer func() { knownSecretValuesFn = old }()
	knownSecretValuesFn = func() []string { return []string{"AXLE-SESSION-VALUE-123456", "csrf-value-abcdefghij"} }
	rec := sampleDiag("[Error] boom -b 'AXLE-SESSION-VALUE-123456' ghp_abcdefghijklmnopqrstuvwxyz0123",
		"curl -b 'AXLE-SESSION-VALUE-123456' -H 'x-browser-api-csrf: csrf-value-abcdefghij'")
	red := rec.Redacted()
	for _, s := range []string{red.Text, red.Request.Prompt} {
		for _, secret := range []string{"AXLE-SESSION-VALUE-123456", "csrf-value-abcdefghij", "ghp_abcdefghijklmnopqrstuvwxyz0123"} {
			if strings.Contains(s, secret) {
				t.Fatalf("%q still holds %q", s, secret)
			}
		}
	}
	// the size and hash are of the prompt as it was
	if red.Request.PromptBytes != len(rec.Request.Prompt) || red.Request.PromptSHA != rec.Request.PromptSHA {
		t.Fatalf("%+v", red.Request)
	}
}

func TestDiagLongTextsKeepHeadAndTail(t *testing.T) {
	long := strings.Repeat("a", 100<<10) + "THE-END"
	rec := sampleDiag(long, "H"+long).Redacted()
	if len(rec.Text) > diagTextHead+diagTextTail+100 || !strings.HasSuffix(rec.Text, "THE-END") || !strings.Contains(rec.Text, "bytes left out") {
		t.Fatalf("text: %d bytes", len(rec.Text))
	}
	if len(rec.Request.Prompt) > diagPromptHead+diagPromptTail+100 || !strings.HasPrefix(rec.Request.Prompt, "H") {
		t.Fatalf("prompt: %d bytes", len(rec.Request.Prompt))
	}
	if rec.Request.PromptBytes != len(long)+1 {
		t.Fatalf("prompt bytes %d", rec.Request.PromptBytes)
	}
}

func TestDiagCapKeepsWholeCharacters(t *testing.T) {
	s := strings.Repeat("≤", 20000) // 3 bytes each
	got := capHeadTail(s, 1000, 1000)
	if strings.ToValidUTF8(got, "\uFFFD") != got {
		t.Fatal("a character was cut")
	}
}
