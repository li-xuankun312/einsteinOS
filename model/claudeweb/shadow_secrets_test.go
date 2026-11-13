package claudeweb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/v2/kernel"
)

// What a command prints on this machine goes back to the conversation without the
// kernel's secrets: 11/13 05:01:42 a dev conversation pushed with a token it had read from
// this machine's bash history.
func TestWhatThisMachinePrintsReachesTheConversationRedacted(t *testing.T) {
	kernelInitOnce.Do(kernel.Init)
	dir := t.TempDir()
	tok := "ghp_" + strings.Repeat("Z9y8", 9)
	hist := filepath.Join(dir, "history.txt")
	if err := os.WriteFile(hist, []byte("git push https://x-access-token:"+tok+"@github.com/o/r.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]string{"command": "cat " + hist})
	s := &ShadowExecutor{WorkDir: dir, Enabled: true}
	out := s.Execute("bash_tool", string(in))
	if out == "" {
		t.Fatal("the command ran here and printed")
	}
	if strings.Contains(out, tok) || strings.Contains(out, "Z9y8Z9y8") {
		t.Fatalf("the token reached the conversation: %q", out)
	}
}

// A command that reads the kernel's credentials or works in its directories is not run on
// this machine: 11/13 a dev conversation read ~/.ssh/einsteinos_deploy and the bash
// history, and reset the remote of the kernel's twin in /home/claude/work/baloon.
func TestACommandOnTheKernelsCredentialsIsNotRunHere(t *testing.T) {
	kernelInitOnce.Do(kernel.Init)
	dir := t.TempDir()
	mark := filepath.Join(dir, "ran")
	s := &ShadowExecutor{WorkDir: dir, Enabled: true}
	for _, c := range []string{
		"touch " + mark + "; cat ~/.ssh/einsteinos_deploy",
		"touch " + mark + "; grep ghp_ /root/.bash_history",
		"touch " + mark + " && cd /home/claude/work/baloon && \\\n  git remote set-url origin x",
	} {
		in, _ := json.Marshal(map[string]string{"command": c})
		if out := s.Execute("bash_tool", string(in)); out != "" {
			t.Fatalf("ran here: %q -> %q", c, out)
		}
		if _, err := os.Stat(mark); err == nil {
			t.Fatalf("ran here: %q", c)
		}
	}
	in, _ := json.Marshal(map[string]string{"command": "touch " + mark})
	s.Execute("bash_tool", string(in))
	if _, err := os.Stat(mark); err != nil {
		t.Fatal("a command in the unit's directory still runs here")
	}
}

// No git run on this machine for a conversation can push, however it is called: 11/13
// 04:26:55 a dev conversation's push ran here with the deploy key.
func TestNoGitRunHereCanPush(t *testing.T) {
	kernelInitOnce.Do(kernel.Init)
	dir := t.TempDir()
	cmd := "G=$(command -v git)\n$G init -q r && cd r && $G -c user.name=a -c user.email=a@b commit -q --allow-empty -m x && " +
		"$G push https://github.com/o/r.git HEAD:main 2>&1; $G push git@github.com:o/r.git HEAD:main 2>&1; true"
	in, _ := json.Marshal(map[string]string{"command": cmd})
	s := &ShadowExecutor{WorkDir: dir, Enabled: true}
	out := s.Execute("bash_tool", string(in))
	if strings.Count(out, "einsteinos-no-push") < 2 {
		t.Fatalf("a push left this machine: %q", out)
	}
}
