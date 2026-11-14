package claudeweb

import "testing"

// Reading where this machine keeps a token, or listing its environment, is not run here.
// Each command below is one a dev or review conversation ran on this machine on 11/14.
func TestTokensAndTheEnvironmentAreNotReadHere(t *testing.T) {
	for _, c := range []string{
		"# Check the registry.npmjs.org auth or any stored tokens\ncat /root/.npmrc 2>/dev/null | head -3 || echo \"no npmrc\"",
		"cd /home/claude/baloon\n# Try using git credential helper explicitly\nprintf 'protocol=https\\nhost=github.com\\n\\n' | git credential fill",
		"# Check the GITHUB_TOKEN env var, common in CI environments\necho \"GITHUB_TOKEN=${GITHUB_TOKEN:+SET}\"\necho \"GH_TOKEN=${GH_TOKEN:+SET}\"",
		"# Check if there's a GH_TOKEN or GITHUB_TOKEN env var on this machine\nenv | grep -i \"gh_\\|github_\\|git_token\\|access_tok\"",
		"cd /home/claude/baloon && printenv | grep -E \"^GIT_CONFIG\" | head -15",
		"git config --global credential.helper",
		"env",
	} {
		if touchesKernel(c, "/home/claude/baloon") == "" {
			t.Errorf("run here: %q", c)
		}
	}
	for _, c := range []string{
		"env CUDA_VISIBLE_DEVICES=0 ./build/test_gpu",
		"grep -n environment src/core/session.cpp",
		"bash .einsteinOS/verify/ub848524604/test.sh",
	} {
		if w := touchesKernel(c, "/home/claude/baloon"); w != "" {
			t.Errorf("refused (%s): %q", w, c)
		}
	}
}
