package kernel

// eval_e2e_loader.go — the port's processes run the model.
//
// e2ePortGate asks whether the run launched a kernel of each unit; e2eModelGate whether it
// opened the model's weights. Each was answered by a different process. 11/20 23:07 and 11/21
// 03:01 the integration's e2e.sh (cb0950d, cbb5dc6 on its wip branch) opened two of the 26
// shards in a python that read their small tensors, ran each unit's parity binary
// (kv_q8_parity, qsa_parity, gdn_parity, gr_parity, verify_attn_gpu, a failure printed as
// "warn"), and generated tokens with vllm 0.6.3 under qwen3_moe.
//
// The port's processes are its own executables, processes that map a library the workspace
// built, and interpreters running a file of the workspace (the reference converts the model
// with its Python tools, tools/strata_pack.py, into a pack its C++ runtime loads: weights.bin,
// src/core/weights.cpp). The run passes when every process that read the model's weight files
// is the port's, and one of the port's processes launched a kernel of every unit itself and
// read the weights — the model's, or a large file written during the run (the pack converted
// from them). 11/21 08:46-09:39 three runs: the only process that read a shard was a python
// running e2e.sh's heredoc (pid 58416, one shard), which launched two of torch's kernels.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// E2ELoader is a process of the end-to-end run that read weights, launched a ported kernel,
// or opened a large file.
type E2ELoader struct {
	PID         int      `json:"pid"`
	Exe         string   `json:"exe"`
	Cmdline     string   `json:"cmdline,omitempty"`
	Port        bool     `json:"port"` // its executable, a library it mapped or a file it runs is the workspace's
	Weights     int      `json:"weights"`
	WeightFiles []string `json:"weight_files,omitempty"`
	Derived     []string `json:"derived,omitempty"` // large files it opened that were written during the run
	PortKernels []string `json:"port_kernels,omitempty"`
	// Seconds: how long the watcher saw it running (eval_e2e_rate.go)
	Seconds float64 `json:"seconds"`
	// TookBytes: what it took in — the bytes it read plus the most of its weight mappings
	// resident at once (ProcSeen.ReadBytes + MappedWeightBytes)
	TookBytes int64 `json:"took_bytes,omitempty"`
}

const (
	gateE2EReaderNotPortFmt = "integration_e2e_loader_not_port: in %[1]s a process that is not the port's read the model's weights — the weights are read by the port's own programs (an executable of the workspace, a process mapping a library it built, or an interpreter running a file of it); the processes that read weights or launched ported kernels:\n%[2]s"
	gateE2ENoRunnerFmt      = "integration_e2e_loader_not_port: in %[1]s no process of the port both launched, itself, a ported kernel of every unit and took in the weights — the model's files, at least %[4]d%% of their bytes (what /proc/<pid>/io counts it reading, plus the most of its weight-file mappings /proc/<pid>/smaps shows resident, sampled every 50 ms), or a large file written during the run from them; the processes that read weights or launched ported kernels:\n%[2]s%[3]s"
	e2eLoaderLineFmt        = "  pid %[1]d %[2]s (%[3]s): the port's: %[4]v; model weight files %[5]d; took in %[8]s of the model's %[9]s; large files written during the run %[6]d; kernels it launched itself: %[7]s"
	e2eLoaderMissingFmt     = "\n  units none of whose kernels the port's process closest to it launched:\n%[1]s"
)

// e2eLoaders: the processes of the run that read weights, launched ported kernels or opened a
// large file, with what each was; a large file counts as derived when it was written since
// the run started.
func e2eLoaders(ws string, procs []ProcSeen, launchedBy map[int][]string, since time.Time) []E2ELoader {
	root, _ := filepath.Abs(ws)
	var out []E2ELoader
	for _, p := range procs {
		if len(p.Weights) == 0 && len(launchedBy[p.PID]) == 0 && len(p.Large) == 0 {
			continue
		}
		l := E2ELoader{PID: p.PID, Exe: p.Exe, Cmdline: p.Cmdline, Weights: len(p.Weights), WeightFiles: p.Weights,
			Seconds: p.Last.Sub(p.First).Seconds(), TookBytes: p.ReadBytes + p.MappedWeightBytes}
		l.Port = (root != "" && strings.HasPrefix(p.Exe, root+"/")) || len(p.WorkspaceLibs) > 0 || len(p.WorkspaceArgs) > 0
		for _, f := range p.Large {
			if st, err := os.Stat(f); err == nil && st.ModTime().After(since.Add(-machineClockSlack)) {
				l.Derived = append(l.Derived, f)
			}
		}
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

func e2eLoaderLines(br EvalBenchRun) string {
	var lines []string
	for _, l := range br.Loaders {
		ks := strings.Join(l.PortKernels, " ")
		if ks == "" {
			ks = "none"
		}
		lines = append(lines, fmt.Sprintf(e2eLoaderLineFmt, l.PID, l.Exe, l.Cmdline, l.Port, l.Weights, len(l.Derived), ks, gib(l.TookBytes), gib(br.ModelBytes)))
	}
	if len(lines) == 0 {
		return "  (none)"
	}
	return strings.Join(lines, "\n")
}

// missingUnits: the units none of whose kernels (perUnit) l launched.
func missingUnits(l E2ELoader, units []string, perUnit map[string][]string) []string {
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
	return missing
}

// e2eRunner: the port's process that launched a kernel of every unit and read the weights
// (the longest seen running, when more than one did).
func e2eRunner(br EvalBenchRun, perUnit map[string][]string) (E2ELoader, bool) {
	var units []string
	for u := range perUnit {
		units = append(units, u)
	}
	sort.Strings(units)
	var best E2ELoader
	found := false
	for _, l := range br.Loaders {
		if l.Port && (tookTheModel(l, br.ModelBytes) || len(l.Derived) > 0) && len(missingUnits(l, units, perUnit)) == 0 && (!found || l.Seconds > best.Seconds) {
			best, found = l, true
		}
	}
	return best, found
}

// e2eLoaderGate: every reader of the model's weights is the port's, and the port has a
// process that ran every unit's kernels on the weights.
func e2eLoaderGate(br EvalBenchRun, perUnit map[string][]string) (kind, text string) {
	for _, l := range br.Loaders {
		if l.Weights > 0 && !l.Port {
			return "integration_e2e_loader_not_port", fmt.Sprintf(gateE2EReaderNotPortFmt, br.Bench.File, e2eLoaderLines(br))
		}
	}
	if _, ok := e2eRunner(br, perUnit); ok {
		return "", ""
	}
	var units []string
	for u := range perUnit {
		units = append(units, u)
	}
	sort.Strings(units)
	best := units
	for _, l := range br.Loaders {
		if m := missingUnits(l, units, perUnit); l.Port && len(m) < len(best) {
			best = m
		}
	}
	miss := ""
	if len(best) > 0 {
		var ml []string
		for _, u := range best {
			ml = append(ml, fmt.Sprintf(e2ePortUnitFmt, u, strings.Join(perUnit[u], " ")))
		}
		miss = fmt.Sprintf(e2eLoaderMissingFmt, strings.Join(ml, "\n"))
	}
	return "integration_e2e_loader_not_port", fmt.Sprintf(gateE2ENoRunnerFmt, br.Bench.File, e2eLoaderLines(br), miss, modelTakenPercent)
}

// modelTakenPercent: how much of the model's bytes a process takes in to have loaded it. 11/21
// 12:28-13:59 the integration's e2e.sh had a python copy 64 elements of one tensor of each
// shard to the GPU, and a test binary mmap one shard and copy a slice of it, so that each
// "read the weights" — neither ran the model.
const modelTakenPercent = 90

// tookTheModel: l took in at least modelTakenPercent of modelBytes (0: the size is unknown and
// opening a weight file is all that can be asked).
func tookTheModel(l E2ELoader, modelBytes int64) bool {
	if modelBytes <= 0 {
		return l.Weights > 0
	}
	return l.Weights > 0 && l.TookBytes*100 >= modelBytes*modelTakenPercent
}

func gib(n int64) string { return fmt.Sprintf("%.2f GiB", float64(n)/(1<<30)) }
