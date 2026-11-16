package claudeweb

import (
	"fmt"
	"strings"
)

// strReplaceCommand is the shell command that applies one str_replace edit on this machine
// the way the sandbox applied it: the first occurrence of old in the file at path becomes new.
func strReplaceCommand(path, old, new string) string {
	escapedOld := strings.ReplaceAll(old, "'", "'\\''")
	escapedNew := strings.ReplaceAll(new, "'", "'\\''")
	return fmt.Sprintf("python3 -c '\nimport sys\nwith open(\"%s\",\"r\") as f: c=f.read()\nold=\"\"\"%s\"\"\"\nnew=\"\"\"%s\"\"\"\nif old not in c:\n    print(\"old_str not found\",file=sys.stderr)\n    sys.exit(1)\nc=c.replace(old,new,1)\nwith open(\"%s\",\"w\") as f: f.write(c)\nprint(\"ok\")\n'", path, escapedOld, escapedNew, path)
}
