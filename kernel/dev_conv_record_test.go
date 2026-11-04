package kernel

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// 11/04 09:52:50: one TLS timeout and the reviewer read the 11/03 record. The download is
// retried; if it never succeeds, the record says this attempt's conversation is unavailable.
func TestAConvRecordThatCannotBeDownloadedSaysSoInsteadOfLeavingTheOldOne(t *testing.T) {
	oldDl, oldWait := downloadConvRawFn, devConvRetryWait
	defer func() { downloadConvRawFn, devConvRetryWait = oldDl, oldWait }()
	devConvRetryWait = func(int) time.Duration { return 0 }

	calls := 0
	downloadConvRawFn = func(string) ([]byte, error) {
		calls++
		return nil, errors.New("net/http: TLS handshake timeout")
	}
	sum, raw := devConvRecord("5e8c99e0-7eb9", "u0e60a9f272", map[string]string{"compile": "pass", "smoke": "pass"})
	if calls != devConvDownloadTries || raw != 0 {
		t.Fatalf("tries=%d raw=%d", calls, raw)
	}
	for _, want := range []string{"unavailable", "5e8c99e0-7eb9", "EARLIER attempt", "compile=pass"} {
		if !strings.Contains(sum, want) {
			t.Fatalf("missing %q in %s", want, sum)
		}
	}

	calls = 0
	downloadConvRawFn = func(string) ([]byte, error) {
		if calls++; calls < 3 {
			return nil, errors.New("TLS handshake timeout")
		}
		return []byte(`{"chat_messages":[]}`), nil
	}
	if _, raw := devConvRecord("cfba2fc8-5393", "u1", map[string]string{}); calls != 3 || raw == 0 {
		t.Fatalf("the third try succeeded: calls=%d raw=%d", calls, raw)
	}
}
