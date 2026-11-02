package kernel

import (
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
