package kernel

// redact.go — secrets never go into a log line.
//
// The Strata run printed `fatal: could not read Password for
// 'https://ghp_…@github.com'` five times: the clone URL carries the GitHub token,
// and git echoes the URL in its errors.

import (
	"os"
	"regexp"
	"sort"
	"strings"
)

var secretRes = []struct {
	re   *regexp.Regexp
	repl string
}{
	// a private key, cut or whole: 11/13 04:52:57 a dev conversation printed
	// ~/.ssh/einsteinos_deploy on this machine and the output went back to it
	{regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----(?s:.*?)(-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`), "<private key redacted>"},
	{regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`), "gh*_<redacted>"},
	{regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`), "github_pat_<redacted>"},
	// user:password@ or token@ inside a URL
	{regexp.MustCompile(`(https?://)[^/@\s'"]+@`), "${1}<redacted>@"},
	{regexp.MustCompile(`(?i)(authorization:\s*(?:token|bearer)\s+)[^\s'"]+`), "${1}<redacted>"},
	{regexp.MustCompile(`provertools_session=[^;'"\s]+`), "provertools_session=<redacted>"},
	{regexp.MustCompile(`(?i)(x-browser-api-csrf:\s*)[^'"\s]+`), "${1}<redacted>"},
}

// redactSecrets removes tokens, URL credentials and the Axle session from s.
func redactSecrets(s string) string {
	// by value first: the patterns only catch the shapes they were written for. 11/03 a
	// child wrote cookie='eyJ…' (06:45, pushed in a chunk's conv.json) and CSRF='YFbn…'
	// (08:22, in the log); neither has a prefix a pattern knows. The kernel knows the values.
	for _, v := range knownSecretValues() {
		s = strings.ReplaceAll(s, v, "<redacted>")
	}
	for _, r := range secretRes {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

// RedactSecrets is redactSecrets for the model layer: what this machine printed, before a
// conversation sees it.
func RedactSecrets(s string) string { return redactSecrets(s) }

// minSecretLen: shorter values are not replaced (they could match ordinary text).
const minSecretLen = 12

// knownSecretValuesFn is a variable for tests.
var knownSecretValuesFn = func() []string {
	sess, csrf := axleCredentials()
	vals := []string{sess, csrf, GetGHToken(), os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN"), os.Getenv("AXLE_COOKIE"), os.Getenv("AXLE_CSRF")}
	// the claude.ai cookie: the whole header value and each of its values
	if ck := os.Getenv("CLAUDE_WEB_COOKIE"); ck != "" {
		vals = append(vals, ck)
		for _, part := range strings.Split(ck, ";") {
			if i := strings.Index(part, "="); i >= 0 {
				vals = append(vals, strings.TrimSpace(part[i+1:]))
			}
		}
	}
	return vals
}

// knownSecretValues: the credentials this kernel holds, longest first (so a value that
// contains another is replaced whole), without short or duplicate ones.
func knownSecretValues() []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range knownSecretValuesFn() {
		v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "provertools_session="))
		if len(v) < minSecretLen || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}
