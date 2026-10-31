package kernel

// redact.go — secrets never go into a log line.
//
// The Strata run printed `fatal: could not read Password for
// 'https://ghp_…@github.com'` five times: the clone URL carries the GitHub token,
// and git echoes the URL in its errors.

import "regexp"

var secretRes = []struct {
	re   *regexp.Regexp
	repl string
}{
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
	for _, r := range secretRes {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}
