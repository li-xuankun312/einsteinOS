package kernel

// eval_e2e_loader.go — the process that loads the model is the port.
//
// e2ePortGate asks whether the run launched a kernel of each unit; e2eModelGate whether it
// opened the model's weights. Each was answered by a different process. 11/20 23:07 and 11/21
// 03:01 the integration's e2e.sh (cb0950d, cbb5dc6 on its wip branch) opened two of the 26
// shards in a python that read their small tensors, ran each unit's parity binary
// (kv_q8_parity, qsa_parity, gdn_parity, gr_parity, verify_attn_gpu, a failure printed as
// "warn"), and generated tokens with vllm 0.6.3 under qwen3_moe: every gate had a process to
// show, and no process ran the port on the model. The run passes only when a process that
// loaded the weights is the port's — an executable of the workspace or a process that mapped a
// shared object the workspace built — and itself launched a ported kernel of every unit.

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// E2ELoader is a process of the end-to-end run that opened or mapped weight files.
type E2ELoader struct {
	PID         int      `json:"pid"`
	Exe         string   `json:"exe"`
	Port        bool     `json:"port"` // its executable or a library it mapped is the workspace's
	Weights     int      `json:"weights"`
	WeightFiles []string `json:"weight_files,omitempty"`
	PortKernels []string `json:"port_kernels,omitempty"` // kernels it launched itself
}

const (
	gateE2ELoaderNotPortFmt = "integration_e2e_loader_not_port: in %[1]s no process that loaded the model's weights is the port's program launching the kernels the units ported — a process that loaded them must be an executable of the workspace, or map a library the workspace built, and launch itself a ported kernel of every unit; the processes that loaded weights:\n%[2]s%[3]s"
	e2eLoaderLineFmt        = "  pid %[1]d %[2]s: %[3]d weight file(s), the workspace's program: %[4]v, kernels it launched itself: %[5]s"
	e2eLoaderMissingFmt     = "\n  units none of whose kernels the port's loading process launched:\n%[1]s"
)

// e2eLoaders: the processes of the run that loaded weights, with what each was and launched.
func e2eLoaders(ws string, procs []ProcSeen, launchedBy map[int][]string) []E2ELoader {
	root, _ := filepath.Abs(ws)
	var out []E2ELoader
	for _, p := range procs {
		if len(p.Weights) == 0 {
			continue
		}
		l := E2ELoader{PID: p.PID, Exe: p.Exe, Weights: len(p.Weights), WeightFiles: p.Weights}
		l.Port = (root != "" && strings.HasPrefix(p.Exe, root+"/")) || len(p.WorkspaceLibs) > 0
		seen := map[string]bool{}
		for _, k := range launchedBy[p.PID] {
			if !seen[k] {
				seen[k] = true
				l.PortKernels = append(l.PortKernels, k)
			}
		}
		sort.Strings(l.PortKernels)
		out = append(out, l)
	}
	return out
}

// e2eLoaderGate: a process that loaded the weights is the port's and launched, itself, a
// ported kernel of every unit (perUnit, as e2ePortGate reads it).
func e2eLoaderGate(br EvalBenchRun, perUnit map[string][]string) (kind, text string) {
	var units []string
	for u := range perUnit {
		units = append(units, u)
	}
	sort.Strings(units)
	bestMissing := units
	for _, l := range br.Loaders {
		if !l.Port {
			continue
		}
		launched := map[string]bool{}
		for _, k := range l.PortKernels {
			launched[k] = true
		}
		var missing []string
		for _, u := range units {
			used := false
			for _, k := range perUnit[u] {
				used = used || launched[k]
			}
			if !used {
				missing = append(missing, u)
			}
		}
		if len(missing) == 0 {
			return "", ""
		}
		if len(missing) < len(bestMissing) {
			bestMissing = missing
		}
	}
	var lines []string
	for _, l := range br.Loaders {
		ks := strings.Join(l.PortKernels, " ")
		if ks == "" {
			ks = "none"
		}
		lines = append(lines, fmt.Sprintf(e2eLoaderLineFmt, l.PID, l.Exe, l.Weights, l.Port, ks))
	}
	if len(lines) == 0 {
		lines = []string{"  (none)"}
	}
	miss := ""
	if len(bestMissing) > 0 {
		var ml []string
		for _, u := range bestMissing {
			ml = append(ml, fmt.Sprintf(e2ePortUnitFmt, u, strings.Join(perUnit[u], " ")))
		}
		miss = fmt.Sprintf(e2eLoaderMissingFmt, strings.Join(ml, "\n"))
	}
	return "integration_e2e_loader_not_port", fmt.Sprintf(gateE2ELoaderNotPortFmt, br.Bench.File, strings.Join(lines, "\n"), miss)
}
