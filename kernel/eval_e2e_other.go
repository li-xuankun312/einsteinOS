package kernel

// eval_e2e_other.go — a process that ran another implementation of the model is not the port.
//
// 11/22 05:11-12:48 the integration's e2e.sh ran pytools/e2e_infer.py, which imported vllm's
// model code (and patched its registry for the model's architecture), and pytools/e2e_sub.py,
// which imported transformers' — each a process "of the port" because the script it was
// started on was a file of the workspace. Whose code a process ran is measured: the launch
// probe records the code files it opened (launch_probe.c, "code" lines), and a file outside the
// workspace that names the model — an architecture or model type its config.json gives — is
// an implementation of the model that is not the port's.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// modelNames: the names config.json gives the model — every value of an "architectures" or
// "model_type" key at any depth (a multimodal config nests the text model's).
func modelNames(cfg map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if len(s) >= 4 && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	var walk func(v any)
	walk = func(v any) {
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		for k, vv := range m {
			switch {
			case k == "model_type":
				if s, ok := vv.(string); ok {
					add(s)
				}
			case k == "architectures":
				if l, ok := vv.([]any); ok {
					for _, a := range l {
						if s, ok := a.(string); ok {
							add(s)
						}
					}
				}
			default:
				walk(vv)
			}
		}
	}
	walk(cfg)
	sort.Strings(out)
	return out
}

// otherCodeMax: a code file larger than this is not read for the model's names.
const otherCodeMax = 16 << 20

var (
	otherCodeMu    sync.Mutex
	otherCodeCache = map[string][]string{}
)

// codeNamesModel: the names of names that the file at path contains or its path does (a
// source or its bytecode; shared objects are not read), read once per file, size and time.
func codeNamesModel(path string, names []string) []string {
	if !strings.HasSuffix(path, ".py") && !strings.HasSuffix(path, ".pyc") {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() > otherCodeMax {
		return nil
	}
	key := fmt.Sprintf("%s|%d|%d|%s", path, st.Size(), st.ModTime().UnixNano(), strings.Join(names, ","))
	otherCodeMu.Lock()
	hit, ok := otherCodeCache[key]
	otherCodeMu.Unlock()
	if ok {
		return hit
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var found []string
	for _, n := range names {
		// the package's own layout names it too (transformers' models/<model_type>/)
		if strings.Contains(path, n) || bytes.Contains(b, []byte(n)) {
			found = append(found, n)
		}
	}
	otherCodeMu.Lock()
	otherCodeCache[key] = found
	otherCodeMu.Unlock()
	return found
}

// otherModelCode: of the code files a process read (code), those outside the workspace ws that
// name the model, each with the names it contains.
func otherModelCode(ws string, code []string, names []string) []string {
	if len(names) == 0 {
		return nil
	}
	root, _ := filepath.Abs(ws)
	seen := map[string]bool{}
	var out []string
	for _, f := range code {
		if seen[f] || (root != "" && strings.HasPrefix(f, root+"/")) {
			continue
		}
		seen[f] = true
		if hit := codeNamesModel(f, names); len(hit) > 0 {
			out = append(out, f+" ("+strings.Join(hit, ", ")+")")
		}
	}
	sort.Strings(out)
	return out
}

// e2eWithOtherCode marks the processes that read code outside the workspace naming the model
// (code: the probe's code lines per pid; names: modelNames of the model's config.json) as not
// the port's, adding the ones that neither read weights nor launched a ported kernel — an
// import of another implementation that failed before it loaded anything is still one.
func e2eWithOtherCode(ws string, loaders []E2ELoader, procs []ProcSeen, code map[int][]string, names []string) []E2ELoader {
	byPID := map[int]int{}
	for i, l := range loaders {
		byPID[l.PID] = i
	}
	seen := map[int]ProcSeen{}
	for _, p := range procs {
		seen[p.PID] = p
	}
	var pids []int
	for pid := range code {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	for _, pid := range pids {
		other := otherModelCode(ws, code[pid], names)
		if len(other) == 0 {
			continue
		}
		i, ok := byPID[pid]
		if !ok {
			p := seen[pid]
			loaders = append(loaders, E2ELoader{PID: pid, Exe: p.Exe, Cmdline: p.Cmdline, Scripts: p.WorkspaceArgs, Seconds: p.Last.Sub(p.First).Seconds()})
			i = len(loaders) - 1
			byPID[pid] = i
		}
		loaders[i].OtherCode = other
		loaders[i].Port = false
	}
	return loaders
}

const (
	// %[1]s the script, %[2]d processes, %[3]s the model's names, %[4]s one line per process
	gateE2EOtherImplFmt = "integration_e2e_other_implementation: in %[1]s %[2]d process(es) ran another implementation of the model — code outside the workspace that names it (%[3]s, as its config.json does), which the launch probe saw each read:\n%[4]s"
	// %[1]d pid, %[2]s executable, %[3]s command line, %[4]s the workspace files it was started on, %[5]s the code
	e2eOtherImplLineFmt = "  pid %[1]d %[2]s (%[3]s): started on the workspace's %[4]s; read %[5]s"
)

// e2eOtherImplGate: no process of the run ran another implementation of the model. The
// processes are named with the files of the branch they were started on: 11/22 every fix
// attempt of the integration began on a wip branch whose e2e_infer.py ran vllm, and went on
// repairing it.
func e2eOtherImplGate(br EvalBenchRun, names []string) (kind, text string) {
	var lines []string
	for _, l := range br.Loaders {
		if len(l.OtherCode) == 0 {
			continue
		}
		scripts := strings.Join(l.Scripts, " ")
		if scripts == "" {
			scripts = "(no file)"
		}
		lines = append(lines, fmt.Sprintf(e2eOtherImplLineFmt, l.PID, l.Exe, l.Cmdline, scripts, strings.Join(l.OtherCode, " ")))
	}
	if len(lines) == 0 {
		return "", ""
	}
	return "integration_e2e_other_implementation", fmt.Sprintf(gateE2EOtherImplFmt, br.Bench.File, len(lines), strings.Join(names, ", "), strings.Join(lines, "\n"))
}

// e2eModelNames: modelNames of the model on this machine.
func e2eModelNames() []string {
	ms, _ := modelShapeOf(evalModel())
	return modelNames(ms.Config)
}
