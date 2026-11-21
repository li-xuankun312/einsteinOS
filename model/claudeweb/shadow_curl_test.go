package claudeweb

import "testing"

// curl or wget anywhere in a script is a network call for the sandbox, never run here.
// 11/14 08:51 and 15:55 a dev conversation's GitHub API calls began with "cd …" and a comment
// line, matched none of the forms isCurlOrWget knew, and ran on this machine.
func TestCurlOnAnyLineOfAScriptIsNotRunHere(t *testing.T) {
	for _, c := range []string{
		"cd /home/claude/baloon\n# Try GitHub API with GITHUB_TOKEN env var\ncurl -s -H \"Authorization: token $GITHUB_TOKEN\" \\\n  \"https://api.github.com/repos/zeng-bin3123/baloon\"",
		"# Check GitHub rate limit and API access\ncurl -s -o /dev/null -w \"%{http_code}\" \"https://api.github.com/repos/zeng-bin3123/baloon\" 2>/dev/null\necho \"\"",
		"ls && (curl -s https://example.com)",
		"x=1 || wget -q https://example.com",
		"curl https://example.com",
	} {
		if !isCurlOrWget(c) {
			t.Errorf("not recognized: %q", c)
		}
	}
	for _, c := range []string{
		"grep -n curl_easy src/net.cpp",
		"cat tests/curlish.txt",
		"echo 'no network here'",
	} {
		if isCurlOrWget(c) {
			t.Errorf("not a curl call: %q", c)
		}
	}
}
