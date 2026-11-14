package kernel

// eval_e2e.go — the integration unit's port runs the model on this machine end to end.
//
// The kernel runs the unit's .einsteinOS/verify/<unit>/e2e.sh in the integration workspace,
// after its build, with MODEL_DIR set to the model directory (evalModel()), and reads the
// prefill and decode rates it prints (the reference's generate prints them as
// "<what> N tokens in T ms -> R tok/s"). No script, a failed run, or no prefill or no decode
// rate in tok/s: the integration does not pass.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	gateE2EMissingFmt    = "integration_e2e_missing: %[1]s does not exist, so the port was never run on the model at %[2]s"
	gateE2EFailedFmt     = "integration_e2e_failed: %[1]s exited %[2]d running the model at %[3]s; output: %[4]s"
	gateE2EUnmeasuredFmt = "integration_e2e_unmeasured: %[1]s ran but printed no %[2]s rate in tok/s; output: %[3]s"
	// the run must be the port: the kernels the other units ported are what it launches
	gateE2ENotPortFmt        = "integration_e2e_not_port: %[1]s launched %[2]d kernel(s) (%[3]s) and none of the kernels these units ported:\n%[4]s"
	gateE2EPortUnmeasuredFmt = "integration_e2e_port_unmeasured: the launch probe was not in %[1]s's processes (%[2]s), so whether it ran the kernels the units ported is not known"
	e2ePortUnitFmt           = "  %[1]s: %[2]s"
)

// devE2EScript: the end-to-end script of a unit.
func devE2EScript(unitID string) string { return ".einsteinOS/verify/" + unitID + "/e2e.sh" }

// evalRunE2E runs the unit's e2e.sh in ws on the model.
func evalRunE2E(ws, unitID string) EvalBenchRun {
	script := devE2EScript(unitID)
	br := EvalBenchRun{Bench: EvalBench{File: script, Kind: "e2e", Name: "e2e.sh"}, Exit: -1}
	if _, err := os.Stat(filepath.Join(ws, script)); err != nil {
		br.Tail = "missing"
		return br
	}
	// the run is watched by the launch probe, as a GPU unit's test.sh is: which kernels it
	// launched is how the kernel knows whether it ran the port (e2ePortGate)
	probeEnv, logPath := "", ""
	if lib, err := e2eProbeLibFn(); err != nil {
		br.ProbeErr = err.Error()
	} else if f, err := os.CreateTemp("", "einsteinos-e2e-launch-*.log"); err != nil {
		br.ProbeErr = err.Error()
	} else {
		logPath = f.Name()
		f.Close()
		defer os.Remove(logPath)
		probeEnv = launchProbeEnv(lib, logPath)
	}
	cmd := probeEnv + fmt.Sprintf("MODEL_DIR=%q bash %q 2>&1", evalModel(), script)
	code, out, tr, err := runWatched(cmd, ws, evalBuildTimeout)
	br.Exit, br.Metrics, br.Tail, br.Devices = code, metricsIn(out), tailText(out, 1500), tr.Devices
	if err != nil {
		br.Tail = err.Error()
	}
	if logPath != "" {
		names, loaded := e2eLaunchesFn(logPath)
		br.Kernels = names
		if !loaded && br.ProbeErr == "" {
			br.ProbeErr = "no process of the run loaded it"
		}
	}
	return br
}

var (
	// e2eProbeLibFn builds (once) the launch probe; e2eLaunchesFn reads the kernels a probe
	// log records and whether any process loaded the probe (variables for tests)
	e2eProbeLibFn = launchProbeLib
	e2eLaunchesFn = func(logPath string) ([]string, bool) {
		pl := readProbeLog(logPath)
		var names []string
		for n := range launchedNames(resolveLaunches(pl)) {
			names = append(names, n)
		}
		sort.Strings(names)
		return names, len(probeLoadedExes(pl)) > 0
	}
)

// e2ePortKernels: per unit other than the integration unit, the kernels its port map gives as
// counterparts and its files define with a body — the GPU code the units ported, read from
// the port maps in the integration workspace ws.
func e2ePortKernels(ws, integrationUnit string) map[string][]string {
	out := map[string][]string{}
	maps, _ := filepath.Glob(filepath.Join(ws, filepath.FromSlash(PortMapPath("*"))))
	texts := map[string]string{}
	read := func(f string) string {
		t, ok := texts[f]
		if !ok {
			b, _ := os.ReadFile(filepath.Join(ws, filepath.FromSlash(f)))
			t = string(b)
			texts[f] = t
		}
		return t
	}
	for _, m := range maps {
		unit := strings.TrimSuffix(filepath.Base(m), ".tsv")
		if unit == integrationUnit {
			continue
		}
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		for _, line := range strings.Split(string(b), "\n") {
			fs := strings.Split(strings.TrimRight(line, "\r"), "\t")
			if len(fs) < 4 || seen[fs[3]] {
				continue
			}
			file, sym := fs[2], fs[3]
			if ext := filepath.Ext(file); ext != ".cu" && ext != ".cuh" {
				continue
			}
			src := read(file)
			for _, k := range kernelNamesIn(src) {
				if k == sym && definesWithBody(src, k) {
					seen[sym] = true
					out[unit] = append(out[unit], sym)
				}
			}
		}
		sort.Strings(out[unit])
	}
	return out
}

// e2ePortGate: the end-to-end run is the port only when, for every unit that ported kernels,
// it launched one of them. 11/09 and 11/10 e2e.sh ran build/generate_model, built from
// src/model/generate_model.cu: 657 lines the integration unit wrote with its own 13 kernels,
// including nothing any unit had ported. The integration's tests launched 196 kernels; the
// program whose prefill and decode rates were judged launched none of the units' kernels.
func e2ePortGate(br EvalBenchRun, perUnit map[string][]string) (kind, text string) {
	if len(perUnit) == 0 {
		return "", ""
	}
	if br.ProbeErr != "" {
		return "integration_e2e_port_unmeasured", fmt.Sprintf(gateE2EPortUnmeasuredFmt, br.Bench.File, br.ProbeErr)
	}
	launched := map[string]bool{}
	for _, k := range br.Kernels {
		launched[k] = true
	}
	var units []string
	for u := range perUnit {
		units = append(units, u)
	}
	sort.Strings(units)
	var unused []string
	for _, u := range units {
		used := false
		for _, k := range perUnit[u] {
			if launched[k] {
				used = true
				break
			}
		}
		if !used {
			unused = append(unused, fmt.Sprintf(e2ePortUnitFmt, u, strings.Join(perUnit[u], " ")))
		}
	}
	if len(unused) == 0 {
		return "", ""
	}
	return "integration_e2e_not_port", fmt.Sprintf(gateE2ENotPortFmt, br.Bench.File, len(br.Kernels), strings.Join(br.Kernels, " "), strings.Join(unused, "\n"))
}

// e2eGate: the first gate the end-to-end run fails, if any.
func e2eGate(br EvalBenchRun) (kind, text string) {
	switch {
	case br.Tail == "missing":
		return "integration_e2e_missing", fmt.Sprintf(gateE2EMissingFmt, br.Bench.File, evalModel())
	case br.Exit != 0:
		return "integration_e2e_failed", fmt.Sprintf(gateE2EFailedFmt, br.Bench.File, br.Exit, evalModel(), br.Tail)
	}
	for _, want := range []string{"prefill", "decode"} {
		found := false
		for _, m := range br.Metrics {
			if m.Unit == "tok/s" && strings.Contains(m.Label, want) {
				found = true
			}
		}
		if !found {
			return "integration_e2e_unmeasured", fmt.Sprintf(gateE2EUnmeasuredFmt, br.Bench.File, want, br.Tail)
		}
	}
	return "", ""
}
