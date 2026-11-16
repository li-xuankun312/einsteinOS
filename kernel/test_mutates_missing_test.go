package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A unit's test that mutates a file the unit did not write fails for a reason the conversation
// cannot see in its own workspace. 11/15 unit p17652417b0's test.sh mutated pytools/locate.py,
// which the unit had not written, the verification tree lacked it, the mutation printed
// "pattern not found", and 13 rounds went into deciding the kernel's checkout was stale.
func TestATestThatMutatesAFileTheUnitDidNotWriteIsFound(t *testing.T) {
	ws := t.TempDir()
	unit := "p17652417b0"
	_, test := devVerifyScripts(unit)
	put := func(p, body string) {
		os.MkdirAll(filepath.Dir(filepath.Join(ws, p)), 0o755)
		os.WriteFile(filepath.Join(ws, p), []byte(body), 0o644)
	}
	put("pytools/locate.py", "if (c / \"gguf\" / \"__init__.py\").is_file():\n")
	put("pytools/mtp_quant.py", "def q2_0(x):\n    return x\n")
	// the unit wrote mtp_quant.py; locate.py is the branch's, from an earlier unit
	changed := []string{"pytools/mtp_quant.py", "tests/k37_pytools/test_pytools.py"}
	put(test, "mutant q2deq      mtp_quant.py   'return x' 'return 0'\n"+
		"mutant gguf_py     locate.py      'if (c / \"gguf\" / \"__init__.py\").is_file():' 'if c.is_dir():'\n")
	s, miss := testMutatesMissing(ws, unit, changed)
	if s != test || strings.Join(miss, ",") != "locate.py" {
		t.Fatalf("script %q missing %v", s, miss)
	}
	txt := testMutatesMissingText(s, miss, changed)
	if !strings.Contains(txt, "locate.py") || !strings.Contains(txt, "2 file(s) the kernel has") {
		t.Fatalf("text: %s", txt)
	}
	// once the unit writes locate.py too, nothing is missing
	if s, miss := testMutatesMissing(ws, unit, append(changed, "pytools/locate.py")); s != "" || miss != nil {
		t.Fatalf("the unit's own file: %q %v", s, miss)
	}
	// a mutation of a file nothing in the workspace has is not this gate's (the test fails plainly)
	put(test, "mutant gone  nowhere.py 'a' 'b'\n")
	if s, _ := testMutatesMissing(ws, unit, changed); s != "" {
		t.Fatalf("a path that does not exist: %q", s)
	}
}
