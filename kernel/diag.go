package kernel

// diag.go — what a conversation's abnormal exit left, kept where nothing else is.
//
// 11/16 12:07:39 unit u253a75befe's dev_fix_1 ended with do_exit(error) one second after a
// Continue. The log holds the exit's size, not its text, so whether the server answered
// "another response is running" or something else could not be told. The record of an
// abnormal exit — the exit's text and the request it answered — goes to a branch of its own,
// einsteinOS/diag_<8 hex>, and never to the terminal. Not to the state branch: that branch
// is checked out in the one local clone the kernel's other git work shares
// (gitUseBranchFromRemote switches that clone's branches), and an exit can happen while
// they run. The record is committed in a clone of its own, made for it and removed after.
// The exit's log line carries the branch's name; the record is fetched from it later.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
	"unicode/utf8"
)

// DiagRequest is the request an exit answered. No header, no cookie: the prompt, redacted.
type DiagRequest struct {
	Model       string `json:"model"`
	Effort      string `json:"effort"`
	Kind        string `json:"kind"` // what was sent: "new conversation", "continue", "writeback"
	PromptBytes int    `json:"prompt_bytes"`
	PromptSHA   string `json:"prompt_sha256"`
	Prompt      string `json:"prompt"` // its head and tail when long
}

// DiagRecord is one abnormal exit.
type DiagRecord struct {
	Time      string      `json:"time"`
	ConvID    string      `json:"conv"`
	Role      string      `json:"role"`
	Exit      string      `json:"exit"`
	Rounds    int         `json:"rounds"`
	Tools     int         `json:"tools"`
	Continues int         `json:"continues"`
	Text      string      `json:"text"` // the exit's text: the error event, or the HTTP status and body
	Request   DiagRequest `json:"request"`
}

const (
	diagTextHead   = 16 << 10
	diagTextTail   = 16 << 10
	diagPromptHead = 24 << 10
	diagPromptTail = 8 << 10
)

// NewDiagRequest describes a request: the size and hash are of the whole prompt, the text
// kept is its head and tail.
func NewDiagRequest(model, effort, kind, prompt string) DiagRequest {
	sum := sha256.Sum256([]byte(prompt))
	return DiagRequest{Model: model, Effort: effort, Kind: kind, PromptBytes: len(prompt),
		PromptSHA: hex.EncodeToString(sum[:]), Prompt: prompt}
}

// capHeadTail keeps the first head and the last tail bytes of s, whole characters only.
func capHeadTail(s string, head, tail int) string {
	if len(s) <= head+tail {
		return s
	}
	h, t := s[:head], s[len(s)-tail:]
	for len(h) > 0 && !utf8.ValidString(h) {
		h = h[:len(h)-1]
	}
	for len(t) > 0 && !utf8.ValidString(t) {
		t = t[1:]
	}
	return fmt.Sprintf("%s\n…[%d bytes left out]…\n%s", h, len(s)-len(h)-len(t), t)
}

// Redacted is the record as it may leave this machine: the kernel's secrets out of every
// text, the long ones cut.
func (r DiagRecord) Redacted() DiagRecord {
	r.Text = redactSecrets(capHeadTail(r.Text, diagTextHead, diagTextTail))
	r.Request.Prompt = redactSecrets(capHeadTail(r.Request.Prompt, diagPromptHead, diagPromptTail))
	return r
}

// DiagBranchName is a new branch's name: einsteinOS/diag_ and eight hex digits.
func DiagBranchName() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		diagRandFallback(b[:])
	}
	return "einsteinOS/diag_" + hex.EncodeToString(b[:])
}

// diagRandFallback fills b from the clock when the system's randomness is not there.
func diagRandFallback(b []byte) {
	n := time.Now().UnixNano()
	for i := range b {
		b[i] = byte(n >> (8 * i))
	}
}
