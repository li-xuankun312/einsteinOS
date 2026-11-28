package claudeweb

import (
	"bytes"
	"encoding/json"
	"strings"
)

// strReplaceScript applies one str_replace edit: the first occurrence of old in the file at
// path becomes new. It holds nothing of the edit — path, old and new arrive as one JSON
// object on stdin — and no single quote, because it is passed to python3 -c in single
// quotes. Before, old and new were pasted into the script inside python's """ strings:
// python read a backslash in them as an escape (the IFS=$'\t' of a test.sh became a tab, so
// the text was not found) and a double quote at the end of one ended the string (a
// SyntaxError). 11/16 every such edit exited 1 here after it had worked in the sandbox.
const strReplaceScript = `import sys, json
d = json.loads(sys.stdin.buffer.read().decode("utf-8"))
p, old, new = d["path"], d["old"], d["new"]
with open(p, "r", encoding="utf-8") as f: c = f.read()
if old not in c:
    first = old.split("\n")[0]
    print("old_str not found in %s: the file has %d bytes and %d lines, old_str %d bytes; its first line (%r) %s in the file" % (p, len(c.encode("utf-8")), c.count("\n") + 1, len(old.encode("utf-8")), first[:60], "is" if first in c else "is not"), file=sys.stderr)
    sys.exit(1)
c = c.replace(old, new, 1)
with open(p, "w", encoding="utf-8") as f: f.write(c)
print("ok")`

// strReplaceCommand is the shell command that applies one str_replace edit on this machine
// the way the sandbox applied it. The edit is the body of a quoted heredoc, so the shell
// expands nothing in it, and a JSON line can never be the delimiter alone on a line.
// The text stays in the command, as it was: the check for what touches the kernel's
// credentials reads the command.
func strReplaceCommand(path, old, new string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(struct {
		Path string `json:"path"`
		Old  string `json:"old"`
		New  string `json:"new"`
	}{path, old, new})
	return "python3 -c '" + strReplaceScript + "' << 'SHADOWEOF'\n" + strings.TrimRight(b.String(), "\n") + "\nSHADOWEOF"
}
