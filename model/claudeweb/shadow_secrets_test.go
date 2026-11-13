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
