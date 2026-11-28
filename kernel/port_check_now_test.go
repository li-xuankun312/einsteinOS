package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 11/26 09:41 to 11:34 each change of the integration port map was judged only at the end of a
// 7-minute verification: a command that changes the map is answered with the map's gates at once
func TestACommandChangingThePortMapIsAnsweredByItsGates(t *testing.T) {
	dir := t.TempDir()
	unit := "uportnow"
	conv := "convportnow01"
	noteMirrorConv(unit, dir, conv, "")
	t.Cleanup(func() {
		mirrorUnitsMu.Lock()
		delete(mirrorUnits, unit)
		for c, u := range mirrorConvs {
			if u == unit {
				delete(mirrorConvs, c)
			}
		}
		mirrorUnitsMu.Unlock()
	})
	registerPortCheck(unit, func() string {
		in := GateInput{UnitID: unit, PortFiles: map[string]bool{}, PortMissing: []string{"  a.cpp func main: no line"},
			RowKernels: map[string]map[string]bool{"src/a.cu": {"k": true}},
			Req:        UnitRequirements{Symbols: []RefSymbol{{File: "a.cpp", Name: "main"}}}}
		return portGatesNow(in, PortMapPath(unit))
	})
	t.Cleanup(func() { registerPortCheck(unit, nil) })
	p := filepath.Join(dir, PortMapPath(unit))
	out := MirrorCommand(conv, func() string {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x\ty\tz\tw\n"), 0o644)
		return "ran"
	})
	if !strings.Contains(out, "the map holds 1 row(s)") || !strings.Contains(out, "[THE PORT MAP AS THIS COMMAND LEFT IT") || !strings.Contains(out, "port_incomplete: 1 of the 1") || !strings.Contains(out, "kernel_unported, port_ref_file_unknown, port_extra, port_name_foreign: pass") {
		t.Fatalf("got %q", out)
	}
	// a command not changing the map is answered as before
	out = MirrorCommand(conv, func() string {
		os.WriteFile(filepath.Join(dir, "other.txt"), []byte("1"), 0o644)
		return "ran"
	})
	if strings.Contains(out, "THE PORT MAP AS") {
		t.Fatalf("the map's gates answered a command that did not change it: %q", out)
	}
}
