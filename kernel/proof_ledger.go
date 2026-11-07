package kernel

// proof_ledger.go — per-theorem Axle status as KERNEL STATE.
//
// Why this exists
// ---------------
// Before this file, "is this proof verified?" lived only in LLM prose: the dev
// child said "submitted", the reviewer said "all pass", and nothing in the
// kernel could tell the two apart. In the Strata run a theory proof that Axle
// rejects (s2_decode_range) was delivered inside the spec, never re-checked by
// the dev child, and then waved through by a second reviewer that never saw an
// Axle response for it.
//
// The ledger inverts that: every Axle request/response pair that passes through
// the shadow layer is parsed here, and the *response* decides the status.
// Narration by any conversation is not an input.
//
//	verified    Axle returned okay=true, no Lean errors, and the checked
//	            content contains no `sorry`.
//	failed      Axle returned an error for the theorem (or its statement).
//	unverified  no usable Axle evidence either way (no creds, network down,
//	            content still contains sorry). NEVER counts as verified.
//
// The ledger is keyed by theorem name. It is process-global because Axle calls
// come from many child conversations and the kernel is the only thing they all
// share.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ProofVerified   = "verified"
	ProofFailed     = "failed"
	ProofUnverified = "unverified"
)

// ProofRecord is the latest known Axle outcome for one theorem.
type ProofRecord struct {
	Name            string
	Status          string
	Statement       string // formal_statement as last sent (the `by sorry` form)
	Content         string // proof text as last sent
	VerifiedContent string // last proof text Axle accepted ("" if never)
	Error           string // first Axle error, truncated (status=failed)
	Note            string // why unverified / why degraded
	Attempts        int
	RespSHA         string // sha256 of the raw Axle response, so claims are auditable
	Degraded        bool   // repair budget exhausted; failure is accepted but loud
	At              time.Time
}

// ProofLedger is safe for concurrent use.
type ProofLedger struct {
	mu    sync.Mutex
	recs  map[string]*ProofRecord
	order []string
}

func NewProofLedger() *ProofLedger {
	return &ProofLedger{recs: map[string]*ProofRecord{}}
}

// Proofs is the process-wide ledger.
var Proofs = NewProofLedger()

// ---------------------------------------------------------------------------
// parsing helpers
// ---------------------------------------------------------------------------

var theoremNameRe = regexp.MustCompile(`(?m)^[ \t]*(?:private[ \t]+|protected[ \t]+)?(?:theorem|lemma)[ \t]+([A-Za-z_][A-Za-z0-9_'.]*)`)

// sorryRe matches the `sorry` tactic/term but not identifiers that merely
// contain the letters.
var sorryRe = regexp.MustCompile(`(^|[^A-Za-z0-9_'.])sorry([^A-Za-z0-9_']|$)`)

// stripLeanComments removes `-- line` and `/- block -/` comments so a comment
// that mentions "sorry" does not poison the check (and vice versa).
func stripLeanComments(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if strings.HasPrefix(s[i:], "/-") {
			j := strings.Index(s[i+2:], "-/")
			if j < 0 {
				break
			}
			i += 2 + j + 2
			continue
		}
		if strings.HasPrefix(s[i:], "--") {
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				break
			}
			i += j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// ContainsSorry reports whether Lean source uses `sorry` outside comments.
func ContainsSorry(lean string) bool {
	return sorryRe.MatchString(stripLeanComments(lean))
}

// TheoremNames returns theorem/lemma names declared in Lean source, in order,
// de-duplicated.
func TheoremNames(lean string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range theoremNameRe.FindAllStringSubmatch(stripLeanComments(lean), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// normalizeSpec undoes JSON-style escaping that sometimes survives in theory
// text (the planner/ref_split path can leave literal `\n` sequences instead of
// newlines; the Strata logs show whole sections on one line). Used for parsing
// only — never written back.
func normalizeSpec(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	realNL := strings.Count(s, "\n")
	litNL := strings.Count(s, `\n`)
	if litNL > realNL {
		// order matters: `\\` must win over `\n` so LaTeX `\\n...` decodes
		// to backslash+n, exactly as JSON would.
		r := strings.NewReplacer(`\\`, `\`, `\n`, "\n", `\t`, "\t", `\"`, `"`)
		s = r.Replace(s)
	}
	return s
}

// SpecProof is one theorem as written in the theory spec: the `by sorry`
// statement and the proof, exactly the pair Axle's verify_proof wants.
type SpecProof struct {
	Name      string
	Statement string
	Content   string
}

const (
	markStatement = "-- axle_verify: formal_statement"
	markContent   = "-- axle_verify: content"
)

// leanBlocks returns the bodies of ```lean fenced blocks.
func leanBlocks(text string) []string {
	var out []string
	rest := text
	for {
		i := strings.Index(rest, "```lean")
		if i < 0 {
			return out
		}
		rest = rest[i+len("```lean"):]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		j := strings.Index(rest, "```")
		if j < 0 {
			out = append(out, rest)
			return out
		}
		out = append(out, rest[:j])
		rest = rest[j+3:]
	}
}

// ParseSpecProofs extracts (name, statement, content) triples from a theory
// spec. A block is only usable for kernel-side verification when it carries the
// two axle_verify markers; unmarked theorems still count toward SpecTheorems.
func ParseSpecProofs(theory string) []SpecProof {
	var out []SpecProof
	seen := map[string]bool{}
	for _, blk := range leanBlocks(normalizeSpec(theory)) {
		si := strings.Index(blk, markStatement)
		ci := strings.Index(blk, markContent)
		if si < 0 || ci < 0 || ci < si {
			continue
		}
		stmt := strings.TrimSpace(blk[si+len(markStatement) : ci])
		cont := strings.TrimSpace(blk[ci+len(markContent):])
		names := TheoremNames(cont)
		if len(names) == 0 || stmt == "" || cont == "" {
			continue
		}
		// a block may contain several marker pairs; only the first pair is
		// parsed here (the planner emits one theorem per block).
		if !seen[names[0]] {
			seen[names[0]] = true
			out = append(out, SpecProof{Name: names[0], Statement: stmt, Content: cont})
		}
	}
	return out
}

// SpecTheorems lists every theorem name the spec declares in lean blocks,
// marked or not. This is the set a reviewer/gate may demand evidence for.
func SpecTheorems(theory string) []string {
	var out []string
	seen := map[string]bool{}
	for _, blk := range leanBlocks(normalizeSpec(theory)) {
		for _, n := range TheoremNames(blk) {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// recording
// ---------------------------------------------------------------------------

func proofTrunc(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func firstMessage(v any) string {
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	for _, e := range arr {
		switch t := e.(type) {
		case string:
			if strings.TrimSpace(t) != "" {
				return t
			}
		case map[string]any:
			if m, ok := t["message"].(string); ok && m != "" {
				return m
			}
			b, _ := json.Marshal(t)
			return string(b)
		}
	}
	return ""
}

// AxleVerdict interprets a raw verify_proof response.
//
// ok=true only for an unambiguous success. usable=false means the response was
// not an Axle JSON document at all (timeout page, curl usage text, ...) and
// must not change any status.
func AxleVerdict(response string) (ok bool, errMsg string, usable bool) {
	resp := strings.TrimSpace(response)
	// the shadow layer prefixes "[pid 0 exit=0] "
	if strings.HasPrefix(resp, "[pid") {
		if i := strings.Index(resp, "] "); i >= 0 {
			resp = strings.TrimSpace(resp[i+2:])
		}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(resp), &m); err != nil {
		return false, "", false
	}
	if ue, ok := m["user_error"].(string); ok && ue != "" {
		return false, proofTrunc(ue, 400), true
	}
	if _, has := m["okay"]; !has {
		return false, "", false
	}
	okay, _ := m["okay"].(bool)
	var firstErr string
	if lm, ok := m["lean_messages"].(map[string]any); ok {
		firstErr = firstMessage(lm["errors"])
	}
	if firstErr == "" {
		if tm, ok := m["tool_messages"].(map[string]any); ok {
			firstErr = firstMessage(tm["errors"])
		}
	}
	if okay && firstErr == "" {
		return true, "", true
	}
	if firstErr == "" {
		firstErr = "okay=false"
	}
	return false, proofTrunc(firstErr, 400), true
}

// RecordAxle folds one Axle request/response pair into the ledger. payload is
// the JSON body that was POSTed; response is whatever came back. Responses that
// are not Axle JSON are ignored (infrastructure noise is not evidence).
func (l *ProofLedger) RecordAxle(payload, response string) {
	var p struct {
		Statement string `json:"formal_statement"`
		Content   string `json:"content"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return
	}
	names := TheoremNames(p.Content)
	if len(names) == 0 {
		names = TheoremNames(p.Statement)
	}
	if len(names) == 0 {
		return
	}
	ok, errMsg, usable := AxleVerdict(response)
	if !usable {
		return
	}
	sum := sha256.Sum256([]byte(response))
	sha := hex.EncodeToString(sum[:8])

	l.mu.Lock()
	defer l.mu.Unlock()
	for _, n := range names {
		r := l.recs[n]
		if r == nil {
			r = &ProofRecord{Name: n}
			l.recs[n] = r
			l.order = append(l.order, n)
		}
		r.Attempts++
		r.At = time.Now()
		r.RespSHA = sha
		r.Statement = p.Statement
		r.Content = p.Content
		r.Degraded = false // fresh evidence resets any earlier degrade
		switch {
		case ok && ContainsSorry(p.Content):
			// Axle accepts `sorry` with a warning; that is not a proof.
			r.Status = ProofUnverified
			r.Note = "content still contains sorry"
			r.Error = ""
		case ok:
			r.Status = ProofVerified
			r.VerifiedContent = p.Content
			r.Error = ""
			r.Note = ""
		default:
			r.Status = ProofFailed
			r.Error = errMsg
			r.Note = ""
		}
	}
}

// MarkUnverified records that a theorem was expected but no Axle evidence could
// be obtained (e.g. no credentials, network down). It never downgrades a
// verified or failed record.
func (l *ProofLedger) MarkUnverified(name, note string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.recs[name]
	if r == nil {
		r = &ProofRecord{Name: name, Status: ProofUnverified}
		l.recs[name] = r
		l.order = append(l.order, name)
	}
	if r.Status == ProofUnverified {
		r.Note = note
		r.At = time.Now()
	}
}

// MarkDegraded accepts a failed theorem as "unproven" after the repair budget
// is spent. The record stays failed — it is only no longer a hard blocker.
func (l *ProofLedger) MarkDegraded(name, note string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r := l.recs[name]; r != nil && r.Status == ProofFailed {
		r.Degraded = true
		r.Note = note
	}
}

// Get returns a copy of the record for name.
func (l *ProofLedger) Get(name string) (ProofRecord, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.recs[name]
	if !ok {
		return ProofRecord{}, false
	}
	return *r, true
}

// Reset clears the ledger (tests, /new).
func (l *ProofLedger) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recs = map[string]*ProofRecord{}
	l.order = nil
}

// ---------------------------------------------------------------------------
// gate + reporting
// ---------------------------------------------------------------------------

// ProofGate is the outcome of checking a set of expected theorems.
type ProofGate struct {
	Verified   []string
	Failed     []string // hard blockers: Axle said no, repair budget not spent
	Degraded   []string // failed, repair budget spent, accepted as unproven
	Unverified []string // no evidence either way
}

// Blocked reports whether the gate must refuse a PASS.
func (g ProofGate) Blocked() bool { return len(g.Failed) > 0 }

// Check classifies every expected name. A name with no record is Unverified.
func (l *ProofLedger) Check(names []string) ProofGate {
	l.mu.Lock()
	defer l.mu.Unlock()
	var g ProofGate
	for _, n := range names {
		r := l.recs[n]
		switch {
		case r == nil:
			g.Unverified = append(g.Unverified, n)
		case r.Status == ProofVerified:
			g.Verified = append(g.Verified, n)
		case r.Status == ProofFailed && r.Degraded:
			g.Degraded = append(g.Degraded, n)
		case r.Status == ProofFailed:
			g.Failed = append(g.Failed, n)
		default:
			g.Unverified = append(g.Unverified, n)
		}
	}
	return g
}

// FailVerdict renders a typed review verdict for a blocked gate. The
// "proof_failed:" prefix is parsed by classify_dev_failure.
func (l *ProofLedger) FailVerdict(g ProofGate) string {
	var parts []string
	for _, n := range g.Failed {
		r, _ := l.Get(n)
		parts = append(parts, fmt.Sprintf("%s (%s)", n, proofTrunc(r.Error, 120)))
	}
	return "FAIL: proof_failed: " + strings.Join(parts, "; ")
}

// Table renders the ledger for the given names as a prompt block. It is a
// statement of kernel fact, written so a model cannot mistake UNVERIFIED for
// verified.
func (l *ProofLedger) Table(names []string) string {
	if len(names) == 0 {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	var b strings.Builder
	for _, n := range sorted {
		r := l.recs[n]
		switch {
		case r == nil:
			b.WriteString(fmt.Sprintf("  %-34s UNVERIFIED  (kernel has no Axle result)\n", n))
		case r.Status == ProofVerified:
			b.WriteString(fmt.Sprintf("  %-34s VERIFIED    (axle okay=true, resp=%s)\n", n, r.RespSHA))
		case r.Status == ProofFailed && r.Degraded:
			b.WriteString(fmt.Sprintf("  %-34s UNPROVEN    (axle: %s)\n", n, proofTrunc(r.Error, 100)))
		case r.Status == ProofFailed:
			b.WriteString(fmt.Sprintf("  %-34s FAILED      (axle: %s)\n", n, proofTrunc(r.Error, 100)))
		default:
			b.WriteString(fmt.Sprintf("  %-34s UNVERIFIED  (%s)\n", n, r.Note))
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// kernel-side verification hooks
// ---------------------------------------------------------------------------
//
// The kernel cannot import model/claudeweb, where the Axle HTTP client lives,
// so the model registers it at boot. Nothing here talks to the network.

var (
	axleMu       sync.Mutex
	axleVerifier func(payload string) string // returns raw response or ""
)

// SetAxleVerifier registers the function the kernel uses to call Axle itself.
func SetAxleVerifier(f func(payload string) string) {
	axleMu.Lock()
	axleVerifier = f
	axleMu.Unlock()
}

// AxleAvailable reports whether the kernel can verify proofs on its own.
func AxleAvailable() bool {
	axleMu.Lock()
	defer axleMu.Unlock()
	return axleVerifier != nil
}

// axlePayload builds the verify_proof body for a (statement, content) pair.
func axlePayload(statement, content string) string {
	b, _ := json.Marshal(map[string]any{
		"formal_statement": statement,
		"content":          content,
		"ignore_imports":   true,
		"environment":      "lean-4.35.0",
		"timeout_seconds":  120,
	})
	return string(b)
}

// VerifyPair runs one (statement, content) pair through Axle and records the
// result. It returns the resulting ledger status. With no verifier or no
// usable response the status is unverified and the ledger is not downgraded.
func (l *ProofLedger) VerifyPair(name, statement, content string) string {
	axleMu.Lock()
	f := axleVerifier
	axleMu.Unlock()
	if f == nil {
		l.MarkUnverified(name, "kernel has no Axle verifier configured")
		return ProofUnverified
	}
	payload := axlePayload(statement, content)
	var resp string
	for try := 0; try < 2 && strings.TrimSpace(resp) == ""; try++ {
		resp = f(payload)
	}
	if strings.TrimSpace(resp) == "" {
		l.MarkUnverified(name, "axle gave no response (network/timeouts)")
		return ProofUnverified
	}
	l.RecordAxle(payload, resp)
	r, ok := l.Get(name)
	if !ok {
		l.MarkUnverified(name, "axle response not attributable to theorem")
		return ProofUnverified
	}
	return r.Status
}

// EnsureSpecVerified makes the kernel itself verify every spec theorem that
// does not already have a `verified` record. It returns the names that are not
// verified afterwards. Nothing here depends on what any conversation claimed.
func (l *ProofLedger) EnsureSpecVerified(theory string) (notVerified []string) {
	for _, sp := range ParseSpecProofs(theory) {
		if r, ok := l.Get(sp.Name); ok && r.Status == ProofVerified &&
			normalizeWS(r.Statement) == normalizeWS(sp.Statement) &&
			normalizeWS(r.VerifiedContent) == normalizeWS(sp.Content) {
			continue
		}
		if l.VerifyPair(sp.Name, sp.Statement, sp.Content) != ProofVerified {
			notVerified = append(notVerified, sp.Name)
		}
	}
	return notVerified
}

func normalizeWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// PatchSpecProof replaces the proof of `name` inside theory with newContent,
// but only if the theorem *statement* in the spec is unchanged and Axle has
// verified exactly (spec statement, newContent). It returns the patched theory.
func (l *ProofLedger) PatchSpecProof(theory, name, newContent string) (string, bool) {
	var old *SpecProof
	for _, sp := range ParseSpecProofs(theory) {
		if sp.Name == name {
			s := sp
			old = &s
			break
		}
	}
	if old == nil {
		return theory, false
	}
	if strings.TrimSpace(newContent) == "" || ContainsSorry(newContent) {
		return theory, false
	}
	if st := l.VerifyPair(name, old.Statement, newContent); st != ProofVerified {
		return theory, false
	}
	// replace in the original (possibly JSON-escaped) text
	for _, variant := range [][2]string{
		{old.Content, newContent},
		{escapeLikeSpec(old.Content), escapeLikeSpec(newContent)},
	} {
		if variant[0] != "" && strings.Contains(theory, variant[0]) {
			return strings.Replace(theory, variant[0], variant[1], 1), true
		}
	}
	return theory, false
}

// escapeLikeSpec mimics the literal-`\n` encoding found in some theory text.
func escapeLikeSpec(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\t", `\t`, `"`, `\"`)
	return r.Replace(s)
}
