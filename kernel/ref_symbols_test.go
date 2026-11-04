package kernel

import (
	"os"
	"testing"
)

func symNames(s []RefSymbol) map[string]string {
	m := map[string]string{}
	for _, x := range s {
		m[x.Name] = x.Kind
	}
	return m
}

// measured on files of the reference commit itself (Niko1221/Strata 01529aaa, testdata/strata)
func TestTheDefinitionsOfAReferenceFileAreMeasured(t *testing.T) {
	read := func(f string) string {
		b, err := os.ReadFile("testdata/strata/" + f)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	ng := symNames(SymbolsOf("include/strata/kernels/ngram.hpp", read("ngram.hpp")))
	for name, kind := range map[string]string{"PleTable": "type", "PleConsts": "type", "ngram_rows": "func",
		"iq4nl_dequant_row": "func", "NG_N_EMBD": "const", "PLE_N_HEADS": "const", "PLE_ROW_BYTES": "const", "NG_HIST": "const"} {
		if ng[name] != kind {
			t.Errorf("ngram.hpp: %s want %s, got %q", name, kind, ng[name])
		}
	}
	ctl := symNames(SymbolsOf("src/spec/controller.cpp", read("controller.cpp")))
	for _, n := range []string{"Controller::choose", "Controller::observe", "CostModel::step_ms"} {
		if ctl[n] != "func" {
			t.Errorf("controller.cpp: out-of-line member %s not measured", n)
		}
	}
	dev := symNames(SymbolsOf("src/core/device.cu", read("device.cu")))
	for _, n := range []string{"poison_kernel", "DeviceArena::alloc", "device_info"} {
		if dev[n] != "func" {
			t.Errorf("device.cu: %s not measured", n)
		}
	}
	py := symNames(SymbolsOf("ref/ngram.py", "import x\nclass Hash:\n    def inner(self): pass\ndef selftest():\n    pass\n"))
	if py["Hash"] != "type" || py["selftest"] != "func" || py["inner"] != "" {
		t.Errorf("python: top-level only: %v", py)
	}
}
