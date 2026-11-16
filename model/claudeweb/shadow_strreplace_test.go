package claudeweb

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// replayStrReplace runs the command the shadow builds for one str_replace edit on a file
// holding content, in a shell, and returns the file afterwards, the exit code and stderr.
func replayStrReplace(t *testing.T, content, old, new string) (after string, code int, stderr string) {
	t.Helper()
	for _, tool := range []string{"bash", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]string{"path": path, "old_str": old, "new_str": new})
	s := &ShadowExecutor{WorkDir: t.TempDir(), Enabled: true}
	cmd := s.strReplaceShellCommand(t, string(in))
	c := exec.Command("bash", "-c", cmd)
	var errb strings.Builder
	c.Stderr = &errb
	err := c.Run()
	code = 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	b, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	return string(b), code, errb.String()
}

// strReplaceShellCommand is the shell command shadow.execute would run for the input.
func (s *ShadowExecutor) strReplaceShellCommand(t *testing.T, inputJSON string) string {
	t.Helper()
	var in toolInput
	if err := json.Unmarshal([]byte(inputJSON), &in); err != nil {
		t.Fatal(err)
	}
	return strReplaceCommand(in.Path, in.OldStr, in.NewStr)
}

func TestStrReplaceReplayControl(t *testing.T) {
	after, code, stderr := replayStrReplace(t, "a\nfoo bar\nb\n", "foo bar", "baz")
	if code != 0 || after != "a\nbaz\nb\n" {
		t.Fatalf("exit %d, file %q, stderr %q", code, after, stderr)
	}
}

// 11/16 10:14 unit u0e60a9f272, 10:57 unit u7991572426, 12:33 unit u253a75befe: the edit that
// removes the port check from test.sh — whose first line is IFS=$'\t', a backslash and a t —
// failed here with exit 1 (old_str not found) while it had worked in the sandbox.
func TestStrReplaceReplayBackslashT(t *testing.T) {
	line := "while IFS=$'\\t' read -r rf rn lf ln; do"
	file := "echo start\n" + line + "\n  : \ndone < \"$TSV\"\necho end\n"
	after, code, stderr := replayStrReplace(t, file, line+"\n  : \ndone < \"$TSV\"\n", "")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if after != "echo start\necho end\n" {
		t.Fatalf("file after: %q", after)
	}
}

// the same edit when old_str ends with a double quote: the replay was a SyntaxError (stderr 252).
func TestStrReplaceReplayEndsWithDoubleQuote(t *testing.T) {
	after, code, stderr := replayStrReplace(t, "echo \"== port table ==\"\nnext\n", "echo \"== port table ==\"", "echo gone")
	if code != 0 || after != "echo gone\nnext\n" {
		t.Fatalf("exit %d, file %q, stderr %q", code, after, stderr)
	}
}

func TestStrReplaceReplayNotFoundLeavesTheFile(t *testing.T) {
	after, code, stderr := replayStrReplace(t, "one\ntwo\n", "three", "x")
	if code != 1 || after != "one\ntwo\n" || !strings.Contains(stderr, "old_str not found") {
		t.Fatalf("exit %d, file %q, stderr %q", code, after, stderr)
	}
}
