package claudeweb

import "testing"

// the shapes of text a conversation's edits hold; each must be applied exactly as the sandbox
// applied it, whatever the shell or python would make of it.
func TestStrReplaceReplayShapes(t *testing.T) {
	cases := []struct {
		name, file, old, new, want string
	}{
		{"literal backslash n", "x = 'a\\nb'\ny\n", "x = 'a\\nb'", "x = 1", "x = 1\ny\n"},
		{"two backslashes", "p = 'C:\\\\dir'\n", "p = 'C:\\\\dir'", "p = 1", "p = 1\n"},
		{"a backslash before a quote", "s = \"a\\\"b\"\n", "s = \"a\\\"b\"", "s = 2", "s = 2\n"},
		{"triple double quotes inside", "d = \"\"\"doc\"\"\"\nz\n", "d = \"\"\"doc\"\"\"", "d = 3", "d = 3\nz\n"},
		{"single quotes", "echo 'it'\\''s'\n", "echo 'it'\\''s'", "echo ok", "echo ok\n"},
		{"dollar, backtick and command substitution", "a=$HOME `id` $(whoami) ${x}\n", "a=$HOME `id` $(whoami) ${x}", "a=none", "a=none\n"},
		{"new text with shell metacharacters", "v=1\n", "v=1", "v=$(touch /tmp/never_made_by_the_replay); `id`", "v=$(touch /tmp/never_made_by_the_replay); `id`\n"},
		{"a line that is the heredoc delimiter", "a\nSHADOWEOF\nb\n", "a\nSHADOWEOF\nb", "a\nSHADOWEOF\nc", "a\nSHADOWEOF\nc\n"},
		{"a new text that holds the delimiter", "k\n", "k", "x\nSHADOWEOF\ny", "x\nSHADOWEOF\ny\n"},
		{"non-ASCII (Lean)", "theorem t (h : 1 ≤ n) : ⟨a, b⟩ = c ∧ d := by\n  omega\n", "(h : 1 ≤ n)", "(hn : 1 ≤ n)", "theorem t (hn : 1 ≤ n) : ⟨a, b⟩ = c ∧ d := by\n  omega\n"},
		{"html-looking characters", "a < b && c > d\n", "a < b && c > d", "a ≤ b", "a ≤ b\n"},
		{"deleting text", "keep\ndrop me\nkeep too\n", "drop me\n", "", "keep\nkeep too\n"},
		{"several lines", "l1\nl2\nl3\nl4\n", "l2\nl3\n", "L\n", "l1\nL\nl4\n"},
		{"only the first occurrence", "x\nx\nx\n", "x", "y", "y\nx\nx\n"},
		{"leading and trailing spaces", "a   b  \n", "   b  ", "|", "a|\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			after, code, stderr := replayStrReplace(t, c.file, c.old, c.new)
			if code != 0 || after != c.want {
				t.Fatalf("exit %d, stderr %q\nfile after: %q\nwant:       %q", code, stderr, after, c.want)
			}
		})
	}
}
