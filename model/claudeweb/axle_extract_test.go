package claudeweb

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/kernel"
)

const hdrs = `-H 'content-type: application/json' -b 'provertools_session=SESS123' -H 'x-browser-api-csrf: CSRF456' -H 'x-request-source: web' -H 'origin: https://axle.axiommath.ai'`

func curlCmd(body string) string {
	return `curl -s https://axle.axiommath.ai/api/v1/verify_proof ` + hdrs + ` --data-raw '` + body + `'`
}

const body1 = `{"formal_statement":"theorem a (n : Nat) : n = n := by sorry","content":"theorem a (n : Nat) : n = n := by\n  rfl","ignore_imports":true}`
const body2 = `{"formal_statement":"theorem b (x : Nat) (h : x ≥ 1) : x > 0 := by sorry","content":"theorem b (x : Nat) (h : x ≥ 1) : x > 0 := by\n  omega","ignore_imports":true}`

// The Strata failure: three curls in ONE bash call. The old extractor took the
// first --data-raw to the LAST }' (one blob), json.Valid failed, and every
// request fell into the slow curl path.
func TestExtract_SeveralCurlsInOneCommand(t *testing.T) {
	cmd := curlCmd(body1) + "\necho ---\n" + curlCmd(body2) + " ; " + curlCmd(body1)
	set, ok := extractAxleRequests(cmd)
	if !ok || len(set.Payloads) != 3 {
		t.Fatalf("want 3 payloads, ok=%v got %d", ok, len(set.Payloads))
	}
	if set.Payloads[0] != body1 || set.Payloads[1] != body2 {
		t.Fatalf("payloads not extracted verbatim:\n%q\n%q", set.Payloads[0], set.Payloads[1])
	}
	if set.Session != "SESS123" || set.CSRF != "CSRF456" {
		t.Fatalf("creds wrong: session=%q csrf=%q", set.Session, set.CSRF)
	}
}

func TestExtract_LeanSingleQuoteInsidePayload(t *testing.T) {
	// Lean uses ' (h' : ...). In a single-quoted shell word that is written '\''.
	body := `{"content":"theorem c (h' : 1 = 1) : True := by trivial"}`
	shellForm := strings.ReplaceAll(body, `'`, `'\''`)
	set, ok := extractAxleRequests(`curl -s https://axle.axiommath.ai/x ` + hdrs + ` --data-raw '` + shellForm + `'`)
	if !ok || len(set.Payloads) != 1 || set.Payloads[0] != body {
		t.Fatalf("'\\'' not decoded: ok=%v %q", ok, set.Payloads)
	}
}

func TestExtract_DoubleQuotedAndPipedTail(t *testing.T) {
	cmd := `curl -s https://axle.axiommath.ai/x ` + hdrs + ` --data-raw "{\"content\":\"theorem d : True := by trivial\"}" | python3 -c 'import sys,json; print(json.load(sys.stdin)["okay"])'`
	set, ok := extractAxleRequests(cmd)
	if !ok || len(set.Payloads) != 1 || set.Payloads[0] != `{"content":"theorem d : True := by trivial"}` {
		t.Fatalf("double-quoted payload with piped tail: ok=%v %q", ok, set.Payloads)
	}
}

func TestExtract_RefusesWhatItCannotEvaluate(t *testing.T) {
	for name, cmd := range map[string]string{
		"variable":    `curl -s https://axle.axiommath.ai/x --data-raw "{\"content\":\"$PROOF\"}"`,
		"subshell":    "curl -s https://axle.axiommath.ai/x --data-raw \"$(cat /tmp/p.json)\"",
		"file body":   `curl -s https://axle.axiommath.ai/x --data-raw '@/tmp/p.json'`,
		"invalid":     `curl -s https://axle.axiommath.ai/x --data-raw '{"content": oops}'`,
		"no payload":  `curl -s https://axle.axiommath.ai/x`,
		"open quote":  `curl -s https://axle.axiommath.ai/x --data-raw '{"a":1}`,
		"one bad one": curlCmd(body1) + "; curl -s https://axle.axiommath.ai/x --data-raw '{bad}'",
	} {
		if set, ok := extractAxleRequests(cmd); ok {
			t.Errorf("%s: must fall back to raw curl, got payloads %q", name, set.Payloads)
		}
	}
}

func TestToolIDSet(t *testing.T) {
	s := toolIDSet{}
	s.mark("toolu_1")
	s.mark("")
	if !s.seen("toolu_1") || s.seen("toolu_2") || s.seen("") {
		t.Fatalf("toolIDSet semantics wrong")
	}
}

// The 15s guard used to take minutes because killing bash left curl holding the
// stdout pipe. A background child must now die with the group.
func TestHardKill_GuardActuallyEndsTheCommand(t *testing.T) {
	t0 := time.Now()
	out := axleExecOnce(`sleep 30 & sleep 30`, "", false, 1*time.Second)
	el := time.Since(t0)
	if out != "" {
		t.Fatalf("expected empty output on timeout, got %q", out)
	}
	if el > 5*time.Second {
		t.Fatalf("1s guard took %s — child process survived the kill", el)
	}
}

func fakeAxleServer(t *testing.T, handler func(body string) string) func() {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write([]byte(handler(string(b))))
	}))
	old := axleURL
	axleURL = srv.URL
	return func() { axleURL = old; srv.Close() }
}

const okResp = `{"failed_declarations":[],"lean_messages":{"errors":[],"warnings":[]},"okay":true}`
const badResp = `{"failed_declarations":[],"lean_messages":{"errors":["-:5:1: error: bad"],"warnings":[]},"okay":false}`

// End to end through the shadow layer: three curls in one command each get
// answered, in order, and every response lands in the kernel ledger.
func TestRunAxleRequests_RecordsEveryResponseInOrder(t *testing.T) {
	kernel.Proofs.Reset()
	defer fakeAxleServer(t, func(body string) string {
		var p struct{ Content string }
		_ = json.Unmarshal([]byte(body), &p)
		if strings.Contains(p.Content, "theorem b") {
			return badResp
		}
		return okResp
	})()
	cmd := curlCmd(body1) + "\n" + curlCmd(body2) + "\n" + curlCmd(body1)
	set, ok := extractAxleRequests(cmd)
	if !ok {
		t.Fatal("extract failed")
	}
	out := runAxleRequests(set)
	blocks := strings.Split(out, "\n[pid 0 exit=0] ")
	if len(blocks) != 3 {
		t.Fatalf("want 3 result blocks, got %d:\n%s", len(blocks), out)
	}
	if !strings.Contains(blocks[1], `"okay":false`) || !strings.Contains(blocks[0], `"okay":true`) {
		t.Fatalf("results out of order:\n%s", out)
	}
	if r, _ := kernel.Proofs.Get("a"); r.Status != kernel.ProofVerified {
		t.Fatalf("theorem a should be verified: %+v", r)
	}
	if r, _ := kernel.Proofs.Get("b"); r.Status != kernel.ProofFailed {
		t.Fatalf("theorem b should be failed: %+v", r)
	}
}

// When Axle never answers, the child must be told WHICH request to resend, and
// the ledger must not invent a status.
func TestRunAxleRequests_NoResponseIsExplicit(t *testing.T) {
	kernel.Proofs.Reset()
	defer fakeAxleServer(t, func(string) string { return "<html>gateway</html>" })()
	set, _ := extractAxleRequests(curlCmd(body1) + "\n" + curlCmd(body2))
	out := runAxleRequests(set)
	if !strings.Contains(out, "axle request 1/2: no response") || !strings.Contains(out, "axle request 2/2: no response") {
		t.Fatalf("missing explicit per-request error:\n%s", out)
	}
	if _, ok := kernel.Proofs.Get("a"); ok {
		t.Fatalf("no response must not create a ledger record")
	}
}

// Registering the verifier is what lets the kernel check spec proofs itself.
func TestBuildSystemPromptRegistersKernelVerifier(t *testing.T) {
	kernel.SetAxleVerifier(nil)
	defer kernel.SetAxleVerifier(nil)
	defer fakeAxleServer(t, func(string) string { return okResp })()
	BuildSystemPrompt("provertools_session=ZZZ", "CSRF")
	if !kernel.AxleAvailable() {
		t.Fatal("BuildSystemPrompt with creds must register the kernel Axle verifier")
	}
	kernel.Proofs.Reset()
	if st := kernel.Proofs.VerifyPair("a", "theorem a (n : Nat) : n = n := by sorry", "theorem a (n : Nat) : n = n := by\n  rfl"); st != kernel.ProofVerified {
		t.Fatalf("kernel-run verification through the registered client failed: %s", st)
	}
	BuildSystemPrompt("", "")
}
