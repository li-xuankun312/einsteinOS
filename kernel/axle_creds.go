package kernel

// axle_creds.go — the Axle session cookie and CSRF token.
//
// They used to be written into the source twice: as the fallback of
// axleCredentials() (kernel/ref_init.go) and as the default of the AXLE_COOKIE /
// AXLE_CSRF settings in examples/claudeweb/main.go — two environment names for the
// same secret, each with the live value as its default, committed to git. Anyone
// who could read the repository could verify proofs as that account, and the value
// stayed valid for as long as nobody noticed.
//
// There is no default now. The process reads the credentials from its
// environment at start (main.go calls SetAxleCredentials); with none set, the
// theory phase refuses to start instead of sending children a cookie that cannot
// work (see AxleConfigured and ref_bind_at).

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"sync"
)

const axleCookiePrefix = "provertools_session="

var (
	axleCredMu  sync.RWMutex
	axleSession string
	axleCSRF    string
)

// SetAxleCredentials records the credentials for this process. The cookie may be
// given with or without its "provertools_session=" prefix.
func SetAxleCredentials(session, csrf string) {
	axleCredMu.Lock()
	defer axleCredMu.Unlock()
	axleSession = strings.TrimPrefix(strings.TrimSpace(session), axleCookiePrefix)
	axleCSRF = strings.TrimSpace(csrf)
}

// axleCredentials returns the session value (no prefix) and the CSRF token: what
// SetAxleCredentials recorded, else the environment (AXLE_SESSION or AXLE_COOKIE,
// and AXLE_CSRF), else empty. Never a built-in value.
func axleCredentials() (session, csrf string) {
	axleCredMu.RLock()
	session, csrf = axleSession, axleCSRF
	axleCredMu.RUnlock()
	if session == "" {
		session = os.Getenv("AXLE_SESSION")
		if session == "" {
			session = os.Getenv("AXLE_COOKIE")
		}
		session = strings.TrimPrefix(strings.TrimSpace(session), axleCookiePrefix)
	}
	if csrf == "" {
		csrf = strings.TrimSpace(os.Getenv("AXLE_CSRF"))
	}
	if csrf == "" {
		csrf = csrfFromCookie(session)
	}
	return session, csrf
}

// AxleCredentials is axleCredentials for callers outside the package (main.go builds
// the system prompt from it): the session value without its prefix, and the CSRF
// token, which is derived from the cookie when it was not set separately.
func AxleCredentials() (session, csrf string) { return axleCredentials() }

// csrfFromCookie reads the CSRF token out of the session cookie. A provertools_session
// cookie is "<base64 JSON>.<sig>.<sig>", and the JSON holds {"browser_api_csrf": "..."}:
// the token the server expects in x-browser-api-csrf is the one inside the cookie, so
// a person only has to supply the cookie. Returns "" if the cookie is not that shape.
func csrfFromCookie(session string) string {
	seg := session
	if i := strings.Index(seg, "."); i >= 0 {
		seg = seg[:i]
	}
	if seg == "" {
		return ""
	}
	var raw []byte
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(seg); err == nil {
			raw = b
			break
		}
	}
	var m map[string]any
	if raw == nil || json.Unmarshal(raw, &m) != nil {
		return ""
	}
	if v, ok := m["browser_api_csrf"].(string); ok {
		return v
	}
	return ""
}

// AxleConfigured reports whether both credentials are present.
func AxleConfigured() bool {
	s, c := axleCredentials()
	return s != "" && c != ""
}
