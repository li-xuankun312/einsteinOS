package claudeweb

// axle_extract.go — turn a child's bash command into Axle requests.
//
// The old extractAxlePayload took everything between the FIRST `--data-raw '`
// and the LAST `}'` in the command. When a child put several curls in one bash
// call (the prompt rule forbidding that was reverted in 2a5cb78b), that span
// covered 3 payloads plus the shell text between them (1172 bytes for three
// 361-byte requests in the Strata run), json.Valid failed, and every request
// fell into the curl fallback — whose "15s" timeout cost 2–10 minutes each.
//
// This file parses the command the way a shell would: it splits it into words
// (single quotes, double quotes, $'..', backslash escapes), then takes the word
// that follows each data flag. Each request is validated independently.

import (
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/kernel"
)

const shellOp = "\x00op"

// shellWords splits a shell command into words. Operators (; | & < > ( )) are
// emitted as the marker shellOp so a data flag can never swallow the next
// command. dynamic reports that a word contained an unexpanded $ or backtick
// inside double quotes — such a word cannot be evaluated here and is returned
// with that flag so callers can refuse to guess.
func shellWords(s string) (words []string, dynamic map[int]bool, ok bool) {
	dynamic = map[int]bool{}
	var cur strings.Builder
	inWord, curDyn := false, false
	flush := func() {
		if inWord {
			if curDyn {
				dynamic[len(words)] = true
			}
			words = append(words, cur.String())
			cur.Reset()
			inWord, curDyn = false, false
		}
	}
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, nil, false
			}
			inWord = true
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 2
		case c == '"':
			inWord = true
			i++
			for {
				if i >= len(s) {
					return nil, nil, false
				}
				d := s[i]
				if d == '"' {
					i++
					break
				}
				if d == '\\' && i+1 < len(s) {
					n := s[i+1]
					switch n {
					case '"', '\\', '$', '`':
						cur.WriteByte(n)
						i += 2
					case '\n':
						i += 2
					default:
						cur.WriteByte(d)
						i++
					}
					continue
				}
				if d == '$' || d == '`' {
					curDyn = true
				}
				cur.WriteByte(d)
				i++
			}
		case c == '$' && i+1 < len(s) && s[i+1] == '\'':
			inWord = true
			i += 2
			for {
				if i >= len(s) {
					return nil, nil, false
				}
				d := s[i]
				if d == '\'' {
					i++
					break
				}
				if d == '\\' && i+1 < len(s) {
					switch s[i+1] {
					case 'n':
						cur.WriteByte('\n')
					case 't':
						cur.WriteByte('\t')
					case 'r':
						cur.WriteByte('\r')
					default: // \\ \' \" and unknown: the escaped char itself
						cur.WriteByte(s[i+1])
					}
					i += 2
					continue
				}
				cur.WriteByte(d)
				i++
			}
		case c == '\\':
			if i+1 < len(s) {
				if s[i+1] == '\n' { // line continuation joins, does not split
					i += 2
					continue
				}
				inWord = true
				cur.WriteByte(s[i+1])
				i += 2
			} else {
				i++
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
			i++
		case c == ';' || c == '|' || c == '&' || c == '<' || c == '>' || c == '(' || c == ')':
			flush()
			words = append(words, shellOp)
			i++
		default:
			inWord = true
			cur.WriteByte(c)
			i++
		}
	}
	flush()
	return words, dynamic, true
}

var dataFlags = map[string]bool{"--data-raw": true, "--data": true, "-d": true, "--data-binary": true}

// axleRequestSet is what extractAxleRequests found in one bash command.
type axleRequestSet struct {
	Payloads []string // each one valid JSON
	Session  string
	CSRF     string
}

// extractAxleRequests parses command and returns every Axle JSON body in it.
// ok=false means "I cannot safely run this myself" (unparseable quoting, a
// dynamic payload, a payload that is not valid JSON, @file bodies, or no
// payload at all); the caller then falls back to running the raw curl.
func extractAxleRequests(command string) (set axleRequestSet, ok bool) {
	words, dynamic, parsed := shellWords(command)
	if !parsed {
		return set, false
	}
	for i := 0; i < len(words); i++ {
		w := words[i]
		var body string
		var idx int
		switch {
		case dataFlags[w]:
			if i+1 >= len(words) || words[i+1] == shellOp {
				return set, false
			}
			body, idx = words[i+1], i+1
			i++
		case strings.HasPrefix(w, "--data-raw="):
			body, idx = strings.TrimPrefix(w, "--data-raw="), i
		default:
			continue
		}
		if dynamic[idx] || strings.HasPrefix(body, "@") || !json.Valid([]byte(body)) {
			return set, false
		}
		set.Payloads = append(set.Payloads, body)
	}
	if len(set.Payloads) == 0 {
		return set, false
	}
	for i, w := range words {
		if k := strings.Index(w, "provertools_session="); k >= 0 && set.Session == "" {
			rest := w[k+len("provertools_session="):]
			if e := strings.IndexAny(rest, "; "); e >= 0 {
				rest = rest[:e]
			}
			set.Session = rest
		}
		if strings.EqualFold(strings.TrimSpace(w), "x-browser-api-csrf:") && i+1 < len(words) && set.CSRF == "" {
			set.CSRF = words[i+1]
		}
		if strings.HasPrefix(strings.ToLower(w), "x-browser-api-csrf:") && len(w) > len("x-browser-api-csrf:") && set.CSRF == "" {
			set.CSRF = strings.TrimSpace(w[len("x-browser-api-csrf:"):])
		}
	}
	return set, true
}

// runAxleRequests sends every payload through the Go HTTP path and returns the
// text a shell would have printed: one "[pid 0 exit=0] <json>" block per
// request, in order. A request that gets no answer after all retries yields an
// explicit error block instead of being silently dropped, so the child can see
// WHICH theorem needs a retry. Every usable response is folded into the kernel
// proof ledger.
func runAxleRequests(set axleRequestSet) string {
	var out []string
	for i, p := range set.Payloads {
		res := axleExecWithRetry(p, set.Session, set.CSRF)
		if res == "" {
			out = append(out, fmt.Sprintf("[pid 0 exit=28] axle request %d/%d: no response after retries (network/timeouts); resend this request",
				i+1, len(set.Payloads)))
			continue
		}
		kernel.Proofs.RecordAxle(p, res)
		out = append(out, res)
	}
	return strings.Join(out, "\n")
}
