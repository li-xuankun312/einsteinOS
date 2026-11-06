package kernel

// eval_port.go — the port's counterparts of the reference's benchmarks, run in the integration
// unit's workspace after its build: the same name (masked), found where the port's build put
// it, run the way the reference declares it.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// withBenchSources adds the files the reference builds its benchmark targets from to the
// integration unit's sources, with their definitions.
func withBenchSources(bare, sha string, sources []string, syms []RefSymbol, benches []EvalBench) ([]string, []RefSymbol) {
	have := map[string]bool{}
	for _, f := range sources {
		have[f] = true
	}
	for _, b := range benches {
		for _, f := range b.Sources {
			if have[f] || refVendoredRe.MatchString(f) {
				continue
			}
			have[f] = true
			sources = append(sources, f)
			if text, err := refGitFn(bare, "show", sha+":"+f); err == nil {
				syms = append(syms, SymbolsOf(f, text)...)
			}
		}
	}
	sort.Strings(sources)
	return sources, syms
}

// evalPortCommand: the command running the port's counterpart of b in ws ("" when the port
// did not build or write it).
func evalPortCommand(ws string, b EvalBench) string {
	find := func(name string) string {
		found := ""
		filepath.WalkDir(ws, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}
			if !d.IsDir() && d.Name() == name && found == "" {
				if st, e := d.Info(); e == nil && st.Mode()&0o111 != 0 {
					found = p
				}
			}
			return nil
		})
		return found
	}
	switch b.Kind {
	case "cmake_test":
		f := strings.Fields(b.Command)
		if len(f) == 0 {
			return ""
		}
		exe := find(filepath.Base(f[0]))
		if exe == "" {
			return ""
		}
		return fmt.Sprintf("%q %s 2>&1", exe, strings.Join(f[1:], " "))
	case "cmake_target":
		if exe := find(b.Name); exe != "" {
			return fmt.Sprintf("%q 2>&1", exe)
		}
	case "script":
		if _, err := os.Stat(filepath.Join(ws, b.File)); err == nil {
			return evalBenchCommand(ws, b)
		}
	}
	return ""
}

// evalRunPort runs the port's counterparts of benches (masked) in ws.
func evalRunPort(ws string, benches []EvalBench) EvalRun {
	run := EvalRun{Repo: "(port)", BuildExit: 0}
	for _, b := range benches {
		br := EvalBenchRun{Bench: b, Exit: -1}
		if cmd := evalPortCommand(ws, b); cmd == "" {
			br.Tail = "the port has no " + b.Kind + " " + b.Name
		} else {
			code, out, tr, err := runWatched(cmd, ws, evalBenchTimeout)
			br.Exit, br.Metrics, br.Tail, br.Devices = code, metricsIn(out), tailText(out, 1500), tr.Devices
			if err != nil {
				br.Tail = err.Error()
			}
		}
		run.Benches = append(run.Benches, br)
	}
	return run
}
