package kernel

import (
	"strings"
	"testing"
)

// 11/25 05:06:01 kg_kernels.hpp:201 `inline int e2e_embed_main() { return 0; }` beside
// build.sh's -Dmain=e2e_embed_main: the gate names the header line and the script line
func TestAHeaderStubOfARenamedTestMainIsAGate(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/verify/uint1/build.sh": "#!/bin/bash\ncuobj tests/embed_parity.cu \"$OBJ/e2e_embed.o\" -Dmain=e2e_embed_main\n",
		"include/baloon/kg_kernels.hpp": "#pragma once\n// Sentence: provides e2e_embed_main\n" +
			"inline int e2e_embed_main() { return 0; }\ninline int other_main() { return 0; }\n" +
			"inline int e2e_real() { return run(); }\n",
		"tests/embed_parity.cu": "int main() { return 1; }\n",
	})
	got := entryStubs(ws, unitBuildScript("uint1"))
	if len(got) != 1 || got[0] != "include/baloon/kg_kernels.hpp:3: inline int e2e_embed_main() { return 0; } — while .einsteinOS/verify/uint1/build.sh:2 makes it of a test's main" {
		t.Fatalf("%q", got)
	}
	kinds, texts := unitGatesAll(GateInput{UnitID: "uint1", EntryStubs: got})
	if len(kinds) != 1 || kinds[0] != "entry_stubbed" || !strings.Contains(texts[0], "kg_kernels.hpp:3") {
		t.Fatalf("%v %q", kinds, texts)
	}
	// no rename, no stub said
	ws2 := t.TempDir()
	putTree(t, ws2, map[string]string{"include/a.hpp": "inline int e2e_embed_main() { return 0; }\n"})
	if s := entryStubs(ws2, unitBuildScript("uint1")); len(s) != 0 {
		t.Fatalf("%q", s)
	}
}
