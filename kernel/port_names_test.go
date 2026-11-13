package kernel

import (
	"strings"
	"testing"
	"time"
)

// Every definition of a reference source is a name a port line may give as its counterpart,
// also the ones another unit ports; masked as the conversation sees them.
func TestTheReferenceFilesNamesAreMeasured(t *testing.T) {
	old := refGitFn
	defer func() { refGitFn = old }()
	refGitFn = func(dir string, args ...string) (string, error) {
		if len(args) == 2 && args[0] == "show" && args[1] == "abc:src/kernels/cuda/router_top10.cu" {
			return "static const int RT_MAX_THREADS = 512;\n__global__ void router_top10_kernel(const float* l, int* o) {}\nvoid strata_router(int n) {}\n", nil
		}
		return "", nil
	}
	req := UnitRequirements{Sources: []string{"src/kernels/cuda/router_top10.cu"}, FlagFiles: map[string][]string{}}
	finishRequirements("bare", "abc", &req)
	names := req.FileNames["src/kernels/cuda/router_top10.cu"]
	if !names["RT_MAX_THREADS"] || !names["router_top10_kernel"] || names["u776_router_probe_kernel"] {
		t.Fatalf("%v", req.FileNames)
	}
	m := maskRequirements(req, newRefMask(refMaskIdent, "Niko1221/Strata"))
	if mn := m.FileNames["src/kernels/cuda/router_top10.cu"]; !mn["router_top10_kernel"] || mn["strata_router"] {
		t.Fatalf("masked %v", m.FileNames)
	}
}

// 11/13 unit u7663fc74b8: a 98th line for a definition it does not port, pointing at a probe
// kernel, does not pass — whatever count its test.sh checks.
func TestAPortLineForADefinitionTheUnitDoesNotPortDoesNotPass(t *testing.T) {
	realRunner(t)
	ws := t.TempDir()
	unit := "u7663fc74b8"
	build, test := devVerifyScripts(unit)
	write(t, ws, build, "true")
	write(t, ws, test, "true")
	write(t, ws, "src/kernels/cuda/router_top10.cu", "__global__ void router_top10_kernel(int* o) {}\n")
	write(t, ws, "src/kernels/cuda/u776_check.cu", "static const int RT_MAX_THREADS = 512;\n__global__ void u776_router_probe_kernel(int* r) { *r = RT_MAX_THREADS; }\n")
	write(t, ws, "src/kernels/cuda/router_top10.cu", "static const int RT_MAX_THREADS = 512;\n__global__ void router_top10_kernel(int* o) {}\n")
	lines := "src/kernels/cuda/router_top10.cu\trouter_top10_kernel\tsrc/kernels/cuda/router_top10.cu\trouter_top10_kernel\n" +
		"src/kernels/cuda/router_top10.cu\tRT_MAX_THREADS\tsrc/kernels/cuda/router_top10.cu\tRT_MAX_THREADS\n"
	syms := []RefSymbol{{File: "src/kernels/cuda/router_top10.cu", Kind: "kernel", Name: "router_top10_kernel"}, {File: "src/kernels/cuda/router_top10.cu", Kind: "const", Name: "RT_MAX_THREADS"}}
	req := UnitRequirements{Symbols: syms}
	for name, extra := range map[string]string{
		"a second line for line 2's definition":    "src/kernels/cuda/router_top10.cu\tRT_MAX_THREADS\tsrc/kernels/cuda/u776_check.cu\tu776_router_probe_kernel\n",
		"a line for a definition it does not port": "src/kernels/cuda/router_top10.cu\tRT_PROBE\tsrc/kernels/cuda/u776_check.cu\tu776_router_probe_kernel\n",
	} {
		write(t, ws, PortMapPath(unit), lines+extra)
		r := runUnitVerifyWith(ws, unit, "", req, []string{PortMapPath(unit)}, time.Minute, time.Minute)
		if r.Passed() || r.Gate != "port_extra" || !strings.Contains(r.GateText, "→ src/kernels/cuda/u776_check.cu:u776_router_probe_kernel") {
			t.Fatalf("%s: gate %q %q", name, r.Gate, r.GateText)
		}
	}
	write(t, ws, PortMapPath(unit), lines)
	if r := runUnitVerifyWith(ws, unit, "", req, []string{PortMapPath(unit)}, time.Minute, time.Minute); !r.Passed() {
		t.Fatalf("the measured lines alone: gate %q %q", r.Gate, r.GateText)
	}
}

// A counterpart named u776_* — not a name router_top10.cu defines — does not pass.
func TestACounterpartTheReferenceFileDoesNotDefineDoesNotPass(t *testing.T) {
	realRunner(t)
	ws := t.TempDir()
	unit := "u7663fc74b8"
	build, test := devVerifyScripts(unit)
	write(t, ws, build, "true")
	write(t, ws, test, "true")
	write(t, ws, "src/kernels/cuda/u776_check.cu", "__global__ void u776_router_probe_kernel(int* r) { *r = 512; }\n")
	write(t, ws, PortMapPath(unit), "src/kernels/cuda/router_top10.cu\trouter_top10_kernel\tsrc/kernels/cuda/u776_check.cu\tu776_router_probe_kernel\n")
	req := UnitRequirements{
		Symbols:   []RefSymbol{{File: "src/kernels/cuda/router_top10.cu", Kind: "kernel", Name: "router_top10_kernel"}},
		FileNames: map[string]map[string]bool{"src/kernels/cuda/router_top10.cu": {"router_top10_kernel": true, "RT_MAX_THREADS": true}},
	}
	r := runUnitVerifyWith(ws, unit, "", req, []string{PortMapPath(unit)}, time.Minute, time.Minute)
	if r.Passed() || !strings.Contains(r.GateText, "port_name_foreign") || !strings.Contains(r.GateText, "u776_router_probe_kernel is not a name src/kernels/cuda/router_top10.cu defines") {
		t.Fatalf("gate %q %q", r.Gate, r.GateText)
	}
}

// 11/13 unit u7663fc74b8's test.sh counted its map's lines (n != 97, then 98): a script
// that reads the port map does not pass; the count is the kernel's.
func TestAVerifyScriptThatCountsThePortMapDoesNotPass(t *testing.T) {
	realRunner(t)
	ws := t.TempDir()
	unit := "u7663fc74b8"
	build, test := devVerifyScripts(unit)
	write(t, ws, build, "true")
	write(t, ws, "src/kernels/cuda/router_top10.cu", "__global__ void router_top10_kernel(int* o) {}\n")
	write(t, ws, PortMapPath(unit), "src/kernels/cuda/router_top10.cu\trouter_top10_kernel\tsrc/kernels/cuda/router_top10.cu\trouter_top10_kernel\n")
	req := UnitRequirements{Symbols: []RefSymbol{{File: "src/kernels/cuda/router_top10.cu", Kind: "kernel", Name: "router_top10_kernel"}}}
	write(t, ws, test, "python3 - <<'PY'\nprint(len(open('.einsteinOS/port/u7663fc74b8.tsv').readlines()))\nPY")
	r := runUnitVerifyWith(ws, unit, "", req, []string{test}, time.Minute, time.Minute)
	if r.Passed() || r.Gate != "port_count_in_script" || !strings.Contains(r.GateText, ".einsteinOS/verify/u7663fc74b8/test.sh") {
		t.Fatalf("gate %q %q", r.Gate, r.GateText)
	}
	write(t, ws, test, "true")
	if r := runUnitVerifyWith(ws, unit, "", req, []string{test}, time.Minute, time.Minute); !r.Passed() {
		t.Fatalf("a test of the code: gate %q %q", r.Gate, r.GateText)
	}
}
