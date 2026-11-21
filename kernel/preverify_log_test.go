package kernel

import (
	"errors"
	"strings"
	"testing"
)

// 11/18 01:51:43 dev_preverify logged 'compile= smoke= gate=""' for a verification that could not
// run; the conversation was told the error. The log line says it too.
func TestAPreverificationThatCouldNotRunLogsWhy(t *testing.T) {
	o := UnitVerifyOutcome{Err: errors.New("delta: lstat /home/claude/baloon: no such file or directory")}
	if l := preVerifyLogFields(o); !strings.Contains(l, "lstat /home/claude/baloon") || !strings.Contains(l, "compile=error") {
		t.Fatalf("%q", l)
	}
	o = UnitVerifyOutcome{Result: VerifyResult{Compile: "pass", Smoke: "fail", Gate: "port_extra"}}
	if l := preVerifyLogFields(o); l != `compile=pass smoke=fail gate="port_extra"` {
		t.Fatalf("%q", l)
	}
}
