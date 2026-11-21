package kernel

// eval_direction.go — the directions an integration unit's end-to-end runs have taken.
//
// A direction is what the run's processes measurably were: the programs that read the model's
// weights or launched ported kernels, each with whether it is the port's — named by its path
// in the workspace (or its absolute path outside it), an interpreter by the file it runs.
// What a conversation says it is doing plays no part. 11/21 08:44-14:07 the integration unit
// went from "python3 - (heredoc) + vllm" to "pytools/e2e_weights.py + build/e2e_all_check +
// each unit's parity binary": two directions, spending the same four attempts.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DirectionStat is one direction of a unit's end-to-end runs.
type DirectionStat struct {
	Key           string    `json:"key"`
	FirstAt       time.Time `json:"first_at"`
	LastAt        time.Time `json:"last_at"`
	Verifications int       `json:"verifications"`
	LastGate      string    `json:"last_gate,omitempty"`
}

// DirectionBook: every direction a unit's end-to-end runs took, by key.
type DirectionBook struct {
	Unit string                    `json:"unit"`
	Dirs map[string]*DirectionStat `json:"dirs"`
}

func directionBookPath(unitID string) string {
	return filepath.Join(".einsteinOS", "eval", "directions", unitID+".json")
}

// loadDirections reads unitID's book from the state branch's directory (empty when none).
func loadDirections(dir, unitID string) *DirectionBook {
	b := &DirectionBook{Unit: unitID, Dirs: map[string]*DirectionStat{}}
	if raw, err := os.ReadFile(filepath.Join(dir, directionBookPath(unitID))); err == nil {
		_ = json.Unmarshal(raw, b)
		if b.Dirs == nil {
			b.Dirs = map[string]*DirectionStat{}
		}
	}
	return b
}

// writeDirections writes the book into the state branch's directory.
func writeDirections(dir string, b *DirectionBook) error {
	p := filepath.Join(dir, directionBookPath(b.Unit))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(b, "", "  ")
	return os.WriteFile(p, raw, 0o644)
}

// storeDirections commits the book on the state branch and pushes it.
func storeDirections(devRepo string, b *DirectionBook) error {
	dir, err := evalStateDir(devRepo)
	if err != nil {
		return err
	}
	if err := writeDirections(dir, b); err != nil {
		return err
	}
	GitCommit(dir, fmt.Sprintf("eval: directions of %s", b.Unit))
	return devGitPush(dir, devRepo, devStateBranch, 20)
}

// interpreterRe: executables that run a file given them (the direction names the file).
var interpreterRe = regexp.MustCompile(`^(python[0-9.]*|bash|sh|dash|perl|ruby|node)$`)

// directionProgram: how a process of the run is named in a direction — the file an interpreter
// runs, else its executable; a path in the workspace relative to it; "port" or "other".
func directionProgram(ws string, l E2ELoader) string {
	root, _ := filepath.Abs(ws)
	rel := func(p string) string {
		if root != "" && strings.HasPrefix(p, root+"/") {
			return strings.TrimPrefix(p, root+"/")
		}
		return p
	}
	name := rel(l.Exe)
	if f := strings.Fields(l.Cmdline); len(f) > 1 && interpreterRe.MatchString(filepath.Base(l.Exe)) {
		name = filepath.Base(l.Exe) + " " + rel(f[1])
	}
	kind := "other"
	if l.Port {
		kind = "port"
	}
	return name + " [" + kind + "]"
}

// directionKey: the run's direction — its processes that read the model's weights or launched
// ported kernels, named by directionProgram, sorted, without repeats.
func directionKey(ws string, br EvalBenchRun) string {
	seen := map[string]bool{}
	var progs []string
	for _, l := range br.Loaders {
		if l.Weights == 0 && len(l.PortKernels) == 0 {
			continue
		}
		p := directionProgram(ws, l)
		if !seen[p] {
			seen[p] = true
			progs = append(progs, p)
		}
	}
	sort.Strings(progs)
	if len(progs) == 0 {
		return "(no process read the model or launched a ported kernel)"
	}
	return strings.Join(progs, " + ")
}

// noteDirection counts one verification of the run in direction key and returns its record.
func (b *DirectionBook) noteDirection(key, gate string, at time.Time) *DirectionStat {
	d := b.Dirs[key]
	if d == nil {
		d = &DirectionStat{Key: key, FirstAt: at}
		b.Dirs[key] = d
	}
	d.Verifications++
	d.LastAt, d.LastGate = at, gate
	return d
}
