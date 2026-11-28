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
	// ClosedAt: when the direction used its budget with no meaningful progress; a run in a
	// closed direction is not evaluated again
	ClosedAt time.Time `json:"closed_at,omitempty"`
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

// directionKey: the run's direction — the program of the process that runs the model: the
// runner (e2eRunner), else the process that took in the most of the model (a port's first, then
// a program the port built, then the one that launched the most ported kernels, then the longest
// seen), named by
// directionProgram. 11/22 18:42 one more script that opened the weight files
// (pytools/e2e_port_check.py, 2.7 s) beside pytools/e2e_infer.py, whose direction had closed at
// 18:11, was a new direction with a new budget: the processes that read the model were the key.
// A unit's parity binary, a helper reading the files, a script renamed around the same runner
// are the same direction.
func directionKey(ws string, br EvalBenchRun, perUnit map[string][]string) string {
	if r, ok := e2eRunner(br, perUnit); ok {
		return directionProgram(ws, r)
	}
	var best E2ELoader
	found := false
	better := func(a, b E2ELoader) bool {
		// two processes that each took in the model are equal in it: 11/28 python's reads and
		// mappings of the same files summed past build/e2e_all_check's
		ta, tb := tookTheModel(a, br.ModelBytes), tookTheModel(b, br.ModelBytes)
		if ta != tb {
			return ta
		}
		if !ta && a.TookBytes != b.TookBytes {
			return a.TookBytes > b.TookBytes
		}
		if a.Port != b.Port {
			return a.Port
		}
		// a program the port built before an interpreter running a file of it: 11/28 04:41:02
		// build/e2e_all_check took in 66.97 GiB of the model's 66.97 GiB and launched ported kernels
		// itself, and the run was named python3.10 pytools/e2e_infer.py, closed since 11/22 18:11,
		// and not evaluated
		if a.Built != b.Built {
			return a.Built
		}
		if a.TookBytes != b.TookBytes {
			return a.TookBytes > b.TookBytes
		}
		if len(a.PortKernels) != len(b.PortKernels) {
			return len(a.PortKernels) > len(b.PortKernels)
		}
		if a.Seconds != b.Seconds {
			return a.Seconds > b.Seconds
		}
		return directionProgram(ws, a) < directionProgram(ws, b)
	}
	for _, l := range br.Loaders {
		if l.Weights == 0 && len(l.Derived) == 0 {
			continue
		}
		if !found || better(l, best) {
			best, found = l, true
		}
	}
	if !found {
		return "(no process read the model)"
	}
	return directionProgram(ws, best)
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
	if gate != "" && !progressed && d.Verifications >= d.Budget {
		d.ClosedAt = at
	}
	return d, progressed
}

// Closed: the direction used its budget with no meaningful progress.
func (d *DirectionStat) Closed() bool { return d != nil && !d.ClosedAt.IsZero() }

const gateDirectionClosedFmt = "integration_direction_closed: the processes of %[1]s are the direction %[2]s, closed at %[3]s after %[4]d verification(s) with no meaningful progress — at most %[5]d%% of the model taken in by its runner, kernels of at most %[6]d unit(s) launched by it, %[7]d token(s) agreeing; a run in it is not evaluated again"

// directionClosedGate: a run in a closed direction, said with what the direction measured.
func directionClosedGate(file string, d *DirectionStat) (string, string) {
	return "integration_direction_closed", fmt.Sprintf(gateDirectionClosedFmt, file, d.Key, d.ClosedAt.UTC().Format("01/02 15:04"),
		d.Verifications, d.BestTookPermille/10, d.BestUnits, d.BestTokens)
}

const (
	directionsHeadFmt = "\nthe directions this unit's end-to-end runs have taken (measured; each: verifications used of its budget, the most of the model's %[1]s its runner took in, units whose kernels that runner launched itself, tokens agreeing with the model's own implementation):"
	directionLineFmt  = "\n  %[1]s — %[2]d/%[3]d, %[4]d%%, %[5]d unit(s), %[6]d token(s)%[7]s"
	directionShutFmt  = ", closed at %[1]s"
)

// summary: the book's directions as measured, oldest first — facts only, inserted into the gate
// text the conversation and the unit's next attempt are given.
func (b *DirectionBook) summary(modelBytes int64) string {
	if b == nil || len(b.Dirs) == 0 {
		return ""
	}
	var ds []*DirectionStat
	for _, d := range b.Dirs {
		ds = append(ds, d)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].FirstAt.Before(ds[j].FirstAt) })
	out := fmt.Sprintf(directionsHeadFmt, gib(modelBytes))
	for _, d := range ds {
		shut := ""
		if d.Closed() {
			shut = fmt.Sprintf(directionShutFmt, d.ClosedAt.UTC().Format("01/02 15:04"))
		}
		out += fmt.Sprintf(directionLineFmt, d.Key, d.Verifications, d.Budget, d.BestTookPermille/10, d.BestUnits, d.BestTokens, shut)
	}
	return out
}

// directionProgramPaths: the programs a direction's key names ("python3.10 pytools/e2e_infer.py
// [port] + build/e2e_all_check [port]" → pytools/e2e_infer.py, build/e2e_all_check).
func directionProgramPaths(key string) []string {
	var out []string
	for _, piece := range strings.Split(key, " + ") {
		piece = strings.TrimSpace(piece)
		if i := strings.LastIndex(piece, " ["); i >= 0 {
			piece = piece[:i]
		}
		f := strings.Fields(piece)
		if len(f) == 0 {
			continue
		}
		out = append(out, f[len(f)-1])
	}
	return out
}

// %[1]s the script, %[2]s the direction, %[3]s when it closed
const e2eClosedDirectionFmt = "%[1]s names every program of the direction closed at %[3]s: %[2]s"

// e2eClosedDirections: the closed directions of the unit every program of which the unit's
// e2e.sh in tree names outside a comment — measured from the script before any build or run.
// 11/23 the integration's e2e.sh still ran build/e2e_all_check, pytools/e2e_infer.py and
// pytools/e2e_weights.py, whose direction had used its budget, and each verification would have
// built for minutes to be told so.
func e2eClosedDirections(tree, unitID string, book *DirectionBook) []string {
	if book == nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(tree, devE2EScript(unitID)))
	if err != nil {
		return nil
	}
	var code []string
	for _, l := range strings.Split(string(raw), "\n") {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
			code = append(code, l)
		}
	}
	text := strings.Join(code, "\n")
	var out []string
	for key, d := range book.Dirs {
		if !d.Closed() {
			continue
		}
		progs := directionProgramPaths(key)
		all := len(progs) > 0
		for _, p := range progs {
			if !strings.Contains(text, filepath.Base(p)) {
				all = false
				break
			}
		}
		if all {
			out = append(out, fmt.Sprintf(e2eClosedDirectionFmt, devE2EScript(unitID), key, d.ClosedAt.UTC().Format("01/02 15:04")))
		}
	}
	sort.Strings(out)
	return out
}
