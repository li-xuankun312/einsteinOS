package kernel

// eval_e2e.go — the integration unit's port runs the model on this machine end to end.
//
// The kernel runs the unit's .einsteinOS/verify/<unit>/e2e.sh in the integration workspace,
// after its build, with MODEL_DIR set to the model directory (evalModelDir), and reads the
// prefill and decode rates it prints (the reference's generate prints them as
// "<what> N tokens in T ms -> R tok/s"). No script, a failed run, or no prefill or no decode
// rate in tok/s: the integration does not pass.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	gateE2EMissingFmt    = "integration_e2e_missing: %[1]s does not exist, so the port was never run on the model at %[2]s"
	gateE2EFailedFmt     = "integration_e2e_failed: %[1]s exited %[2]d running the model at %[3]s; output: %[4]s"
	gateE2EUnmeasuredFmt = "integration_e2e_unmeasured: %[1]s ran but printed no %[2]s rate in tok/s; output: %[3]s"
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
	cmd := fmt.Sprintf("MODEL_DIR=%q bash %q 2>&1", evalModelDir, script)
	code, out, tr, err := runWatched(cmd, ws, evalBuildTimeout)
	br.Exit, br.Metrics, br.Tail, br.Devices = code, metricsIn(out), tailText(out, 1500), tr.Devices
	if err != nil {
		br.Tail = err.Error()
	}
	return br
}

// e2eGate: the first gate the end-to-end run fails, if any.
func e2eGate(br EvalBenchRun) (kind, text string) {
	switch {
	case br.Tail == "missing":
		return "integration_e2e_missing", fmt.Sprintf(gateE2EMissingFmt, br.Bench.File, evalModelDir)
	case br.Exit != 0:
		return "integration_e2e_failed", fmt.Sprintf(gateE2EFailedFmt, br.Bench.File, br.Exit, evalModelDir, br.Tail)
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
