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
	// Built: its executable is a file of the workspace — a program the port built, not an
	// interpreter running a file of it
	Built bool `json:"built,omitempty"`
	// Seconds: how long the watcher saw it running (eval_e2e_rate.go)
	Seconds float64 `json:"seconds"`
	// TookBytes: what it took in — the bytes it read plus the most of its weight mappings
	// resident at once (ProcSeen.ReadBytes + MappedWeightBytes)
	TookBytes int64 `json:"took_bytes,omitempty"`
	// OtherCode: the code files outside the workspace it read that name the model (another
	// implementation of it, eval_e2e_other.go) — a process that has any is not the port's;
	// Scripts: the files of the workspace it was started on
	OtherCode []string `json:"other_code,omitempty"`
	Scripts   []string `json:"scripts,omitempty"`
}

const (
	gateE2EReaderNotPortFmt = "integration_e2e_loader_not_port: in %[1]s a process that is not the port's read the model's weights — the weights are read by the port's own programs (an executable of the workspace, a process mapping a library it built, or an interpreter running a file of it); the processes that read weights or launched ported kernels:\n%[2]s"
	gateE2ENoRunnerFmt      = "integration_e2e_loader_not_port: in %[1]s no process of the port both launched, itself, a ported kernel and took in the weights — the model's files, at least %[4]d%% of their bytes (what /proc/<pid>/io counts it reading, plus the most of its weight-file mappings /proc/<pid>/smaps shows resident, sampled every 50 ms), or a large file written during the run from them; the processes that read weights or launched ported kernels:\n%[2]s%[3]s"
	e2eLoaderLineFmt        = "  pid %[1]d %[2]s (%[3]s): the port's: %[4]v; model weight files %[5]d; took in %[8]s of the model's %[9]s; large files written during the run %[6]d; kernels it launched itself: %[7]s"
	// %[1]d the ids of its "generated:" lines. 11/28 03:49:59 and 04:10:54 the gate gave each
	// process's weight files, what it took in and its kernels, and nothing of the token ids it
	// printed, which the direction's "0 token(s) agree" was measured from
	e2eLoaderGeneratedFmt = "; token ids it printed on \"generated:\" lines: %[1]d"
	// %[1]d groups, %[2]d groups named, %[3]s each with its kernels
	e2eLoaderMissingGroupsFmt = "\n  of the %[2]d tensor groups the planner's op lines name, %[1]d had none of their kernels launched by the port's process that took in the most of the model: %[3]s"
	// %[1]s the group, %[2]s its kernels
	e2eLoaderMissingGroupFmt = "%[1]s (one of: %[2]s)"
	e2eLoaderMissingFmt      = "\n  units none of whose kernels the port's process that took in the most of the model launched (the run's other processes may launch them):\n%[1]s"
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
		l.Scripts = p.WorkspaceArgs
		l.Built = root != "" && strings.HasPrefix(p.Exe, root+"/")
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
		_, gen := e2eTokens(strings.Join(br.TokensBy[l.PID], "\n"))
		lines = append(lines, fmt.Sprintf(e2eLoaderLineFmt, l.PID, l.Exe, l.Cmdline, l.Port, l.Weights, len(l.Derived), ks, gib(l.TookBytes), gib(br.ModelBytes))+
			fmt.Sprintf(e2eLoaderGeneratedFmt, len(gen)))
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

// missingGroups: the tensor groups of gs none of whose kernels l launched, sorted.
func missingGroups(l E2ELoader, gs map[string][]string) []string {
	var out []string
	for g, ks := range gs {
		hit := false
		for _, k := range ks {
			hit = hit || hasString(l.PortKernels, k)
		}
		if !hit {
			out = append(out, g)
		}
	}
	sort.Strings(out)
	return out
}

// e2eRunner: the port's process that launched a kernel of every unit and read the weights
// (the longest seen running, when more than one did). Without the planner's answer it is one
// that launched a kernel of some unit; with it, of every unit the model uses: 11/22 15:07
// e2e_weights.py read the whole model and launched kernels of 7 of 7 units, and a forward
// pass written in python beside one launch would have made it the runner.
func e2eRunner(br EvalBenchRun, perUnit map[string][]string) (E2ELoader, bool) {
	var units []string
	for u := range perUnit {
		units = append(units, u)
	}
	sort.Strings(units)
	var best E2ELoader
	found := false
	for _, l := range br.Loaders {
		// the runner launches ported kernels itself (when units ported any); that every unit's kernels ran is asked of the
		// whole run (e2ePortGate) — the units' own tests, many processes, verify them beside it —
		// and how many the runner covers is its direction's measure (eval_direction.go)
		covers := len(units) == 0 || len(missingUnits(l, units, perUnit)) < len(units)
		if br.RunnerAllUnits {
			// the planner answered which units this model's forward uses (integration_model_plan.go):
			// the process that runs it launches a kernel of each of them itself, and for every
			// group of the model's tensors its op lines name — one of the kernels named for it
			covers = len(missingUnits(l, units, perUnit)) == 0 && len(missingGroups(l, br.RunnerGroups)) == 0
		}
		// a program the port built before an interpreter running a file of it, then the longest seen
		if l.Port && (tookTheModel(l, br.ModelBytes) || len(l.Derived) > 0) && covers && (!found || (l.Built && !best.Built) || (l.Built == best.Built && l.Seconds > best.Seconds)) {
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
	if br.RunnerAllUnits && len(br.RunnerGroups) > 0 {
		// the tensor groups the planner's op lines name of which the port's process taking in
		// the most of the model launched none of the kernels named for it
		top, _ := e2eTopLoader(br, false)
		if mg := missingGroups(top, br.RunnerGroups); len(mg) > 0 {
			var each []string
			for _, g := range mg {
				each = append(each, fmt.Sprintf(e2eLoaderMissingGroupFmt, g, strings.Join(br.RunnerGroups[g], " | ")))
			}
			miss += fmt.Sprintf(e2eLoaderMissingGroupsFmt, len(mg), len(br.RunnerGroups), strings.Join(each, "; "))
		}
	}
	if len(best) > 0 {
		var ml []string
		for _, u := range best {
			ml = append(ml, fmt.Sprintf(e2ePortUnitFmt, u, strings.Join(perUnit[u], " ")))
		}
		miss = fmt.Sprintf(e2eLoaderMissingFmt, strings.Join(ml, "\n")) + miss
	}
	if l, ok := portTookTheModel(br); ok {
		return "integration_e2e_loader_not_port", fmt.Sprintf(gateE2ERunnerShortFmt, br.Bench.File, l.PID, l.Cmdline, gib(l.TookBytes), gib(br.ModelBytes), e2eLoaderLines(br), miss)
	}
	return "integration_e2e_loader_not_port", fmt.Sprintf(gateE2ENoRunnerFmt, br.Bench.File, e2eLoaderLines(br), miss, modelTakenPercent)
}

// %[1]s e2e.sh, %[2]d the process, %[3]s its command, %[4]s what it took in, %[5]s the model's
// size, %[6]s the processes, %[7]s what it did not launch
const gateE2ERunnerShortFmt = "integration_e2e_loader_not_port: in %[1]s pid %[2]d (%[3]s), the port's, took in %[4]s of the model's %[5]s and launched ported kernels itself, and is not the run's runner for what it did not launch, given below; the processes that read weights or launched ported kernels:\n%[6]s%[7]s"

// portTookTheModel: the port's process that took in the model and launched ported kernels itself,
// the one taking in the most. 11/28 03:49:59 and 04:10:54 the gate said no process of the port both
// launched a ported kernel and took in the weights, while build/e2e_all_check, the port's, took in
// 66.97 GiB of the model's 66.97 GiB and launched ported kernels of 6 of 6 units itself; what it
// lacked was among the rest of the text, after both processes' kernels.
func portTookTheModel(br EvalBenchRun) (E2ELoader, bool) {
	l, ok := e2eTopLoader(br, true)
	if !ok || !tookTheModel(l, br.ModelBytes) {
		return E2ELoader{}, false
	}
	return l, true
}

// e2eTopLoader: the port's process the run is measured by when it has no runner — one that took
// in the model before one that did not, a program the port built before an interpreter running a
// file of it, then the one taking in the most; withKernels: only processes that launched ported
// kernels. 11/28 the tensor groups the gate gave as launched by no kernel were measured from the
// port's process whose reads and mappings summed highest, a python summing past
// build/e2e_all_check, which took in the same 66.97 GiB and launched the units' kernels itself.
func e2eTopLoader(br EvalBenchRun, withKernels bool) (E2ELoader, bool) {
	var best E2ELoader
	found := false
	for _, l := range br.Loaders {
		if !l.Port || (withKernels && len(l.PortKernels) == 0) {
			continue
		}
		if !found {
			best, found = l, true
			continue
		}
		tl, tb := tookTheModel(l, br.ModelBytes), tookTheModel(best, br.ModelBytes)
		switch {
		case tl != tb:
			if tl {
				best = l
			}
		case l.Built != best.Built:
			if l.Built {
				best = l
			}
		case l.TookBytes > best.TookBytes:
			best = l
		}
	}
	return best, found
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
