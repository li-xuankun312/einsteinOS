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
	"strconv"
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
	// Budget: the verifications the direction is given — directionLength when it first
	// appears, directionLength more at each meaningful progress (directionProgress)
	Budget int `json:"budget"`
	// the direction's best of the measures only real work raises: the most of the model's
	// bytes its runner took in (per mille), the units whose kernels that runner launched
	// itself, the generated tokens agreeing with the model's own implementation
	BestTookPermille int `json:"best_took_permille"`
	BestUnits        int `json:"best_units"`
	BestTokens       int `json:"best_tokens"`
}

// DirectionMeasure: what one run measured of the three.
type DirectionMeasure struct {
	TookPermille, Units, UnitsOf, Tokens int
}

// directionLength: the verifications a direction is given at first and at each meaningful
// progress — the .format argument EINSTEINOS_DIRECTION_LENGTH (3 when it is not set).
var directionLength = func() int {
	if n, err := strconv.Atoi(os.Getenv("EINSTEINOS_DIRECTION_LENGTH")); err == nil && n > 0 {
		return n
	}
	return 3
}

// measureDirection: the run's three measures — its runner is the port's process that took in
// the most of the model's bytes; the units are those (of perUnit) whose kernels that process
// launched itself; the tokens are those agreeing with the model's own implementation.
func measureDirection(br EvalBenchRun, perUnit map[string][]string, tokens int) DirectionMeasure {
	m := DirectionMeasure{Tokens: tokens, UnitsOf: len(perUnit)}
	var runner *E2ELoader
	for i, l := range br.Loaders {
		if l.Port && (runner == nil || l.TookBytes > runner.TookBytes) {
			runner = &br.Loaders[i]
		}
	}
	if runner == nil {
		return m
	}
	if br.ModelBytes > 0 {
		m.TookPermille = int(runner.TookBytes * 1000 / br.ModelBytes)
		if m.TookPermille > 1000 {
			m.TookPermille = 1000
		}
	}
	var units []string
	for u := range perUnit {
		units = append(units, u)
	}
	m.Units = len(units) - len(missingUnits(*runner, units, perUnit))
	return m
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

// noteDirection counts one verification of the run in direction key and returns its record
// and whether the run made meaningful progress in it: one of the three measures above the
// direction's own best so far (a gate further on is none — 11/21 12:28 loader_not_port gave
// way to partial_model by copying 64 elements of each shard). Progress gives the direction
// directionLength more verifications; a direction's first run sets its bests.
func (b *DirectionBook) noteDirection(key, gate string, at time.Time, m DirectionMeasure) (*DirectionStat, bool) {
	d := b.Dirs[key]
	first := d == nil
	if first {
		d = &DirectionStat{Key: key, FirstAt: at, Budget: directionLength()}
		b.Dirs[key] = d
	}
	d.Verifications++
	d.LastAt, d.LastGate = at, gate
	progressed := !first && (m.TookPermille > d.BestTookPermille || m.Units > d.BestUnits || m.Tokens > d.BestTokens)
	d.BestTookPermille = max(d.BestTookPermille, m.TookPermille)
	d.BestUnits = max(d.BestUnits, m.Units)
	d.BestTokens = max(d.BestTokens, m.Tokens)
	if progressed {
		d.Budget += directionLength()
	}
	return d, progressed
}
