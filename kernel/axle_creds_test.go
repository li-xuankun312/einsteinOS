package kernel

import (
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func clearAxle(t *testing.T) {
	SetAxleCredentials("", "")
	for _, k := range []string{"AXLE_SESSION", "AXLE_COOKIE", "AXLE_CSRF"} {
		t.Setenv(k, "")
	}
	t.Cleanup(func() { SetAxleCredentials("", "") })
}

func TestNoBuiltInAxleCredentials(t *testing.T) {
	clearAxle(t)
	if s, c := axleCredentials(); s != "" || c != "" {
		t.Fatalf("with nothing configured the credentials must be EMPTY, got %q / %q", s, c)
	}
	if AxleConfigured() {
		t.Fatal("AxleConfigured with nothing configured")
	}
}

func TestAxleCredentialsComeFromConfigThenEnvironment(t *testing.T) {
	clearAxle(t)
	t.Setenv("AXLE_COOKIE", "provertools_session=fromenv")
	t.Setenv("AXLE_CSRF", "csrf-env")
	if s, c := axleCredentials(); s != "fromenv" || c != "csrf-env" {
		t.Fatalf("env (cookie with its prefix): %q %q", s, c)
	}
	t.Setenv("AXLE_COOKIE", "")
	t.Setenv("AXLE_SESSION", "session-env")
	if s, _ := axleCredentials(); s != "session-env" {
		t.Fatalf("AXLE_SESSION alias: %q", s)
	}
	SetAxleCredentials(" provertools_session=fromconfig ", " csrf-config ")
	if s, c := axleCredentials(); s != "fromconfig" || c != "csrf-config" {
		t.Fatalf("SetAxleCredentials must win and strip the prefix: %q %q", s, c)
	}
	if !AxleConfigured() {
		t.Fatal("configured")
	}
}

func TestTheoryPhaseRefusesWithoutAxleCredentials(t *testing.T) {
	clearAxle(t)
	_, err := ref_bind_at(1, "o", "r", 0)
	if err == nil || !strings.Contains(err.Error(), "Axle credentials are not configured") {
		t.Fatalf("theory must refuse to start, got %v", err)
	}
}

// A secret scan over the sources that ship: a session cookie or token written into
// the code is how this repository came to hold a live Axle cookie. The patterns are
// shapes, not values, so the test does not contain what it forbids.
func TestNoCredentialShapedStringsInSource(t *testing.T) {
	shapes := []*regexp.Regexp{
		regexp.MustCompile(`eyJ[A-Za-z0-9+/=_-]{40,}`),                      // base64 JSON: a signed session cookie
		regexp.MustCompile(`ghp_[A-Za-z0-9]{30,}`),                          // GitHub personal access token
		regexp.MustCompile(`github_pat_[A-Za-z0-9_]{30,}`),                  // fine-grained token
		regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{30,}`),                     // API key
		regexp.MustCompile(`(?i)csrf[^\n]{0,30}["'][A-Za-z0-9_-]{40,}["']`), // a literal CSRF token
	}
	root := filepath.Join("..")
	skip := map[string]bool{".git": true, "distr": true, "node_modules": true}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		for _, re := range shapes {
			if m := re.FindString(string(b)); m != "" {
				t.Errorf("%s contains a credential-shaped string (%s…): configuration belongs in the environment", p, m[:8])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPRSetupRefusesBeforeForkingAnything(t *testing.T) {
	clearAxle(t)
	before := len(prTable)
	out := pr_driver_setup("build something from github.com/o/r")
	if !strings.Contains(out, "refused") || !strings.Contains(out, "AXLE_COOKIE") {
		t.Fatalf("refusal must name what to set: %q", out)
	}
	if len(prTable) != before {
		t.Fatal("a PR process was created although the setup was refused")
	}
}

// a cookie shaped like the real one, built here so no credential-shaped literal exists in the tests
func fakeCookie(csrf string) string {
	payload, _ := json.Marshal(map[string]string{"browser_api_csrf": csrf})
	return "provertools_session=" + base64.StdEncoding.EncodeToString(payload) + ".sigpart.sigpart2"
}

func TestCSRFIsReadFromInsideTheCookie(t *testing.T) {
	clearAxle(t)
	t.Setenv("AXLE_COOKIE", fakeCookie("TOKEN-FROM-COOKIE"))
	s, c := axleCredentials()
	if c != "TOKEN-FROM-COOKIE" || !strings.HasSuffix(s, ".sigpart.sigpart2") || strings.HasPrefix(s, "provertools_session=") {
		t.Fatalf("session %q csrf %q", s, c)
	}
	if !AxleConfigured() {
		t.Fatal("a cookie alone must be enough")
	}
	// an explicitly set CSRF wins over the derived one
	t.Setenv("AXLE_CSRF", "EXPLICIT")
	if _, c := axleCredentials(); c != "EXPLICIT" {
		t.Fatalf("explicit CSRF must win, got %q", c)
	}
	// SetAxleCredentials with an empty csrf derives it too, and the exported accessor shows it
	clearAxle(t)
	SetAxleCredentials(fakeCookie("VIA-SET"), "")
	if _, c := AxleCredentials(); c != "VIA-SET" {
		t.Fatalf("%q", c)
	}
}

func TestCSRFFromAMalformedCookieIsEmptyNotAGuess(t *testing.T) {
	for _, bad := range []string{"", "notbase64!!.x.y", "bm90IGpzb24=.x.y", "e30=.x.y"} { // not base64 / not JSON / JSON without the field
		if got := csrfFromCookie(bad); got != "" {
			t.Errorf("%q → %q", bad, got)
		}
	}
	clearAxle(t)
	t.Setenv("AXLE_COOKIE", "provertools_session=garbage")
	if AxleConfigured() {
		t.Fatal("a cookie that has no CSRF inside and no AXLE_CSRF is not a configuration")
	}
}
