package kernel

// eval_compare.go — numbers other repositories published, compared with the port's own
// end-to-end run when their conditions correspond.
//
// Published numbers come from other machines and often other models, so they are never
// compared as they stand. The planner judges which of them were measured under conditions
// that correspond in proportion to this run's (the model, the memory capacity) and which of
// the port's measured rates each one corresponds to; the kernel then compares the numbers
// themselves, in the direction their unit gives. The planner decides comparability only,
// never a number: what it is given is the kernel's measurement (the conditions, the port's
// output) and the repositories' own files, uploaded into its sandbox.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// EvalComparison is one published number the planner found comparable, against the port's.
type EvalComparison struct {
	Clue  EvalClue `json:"clue"`
	Port  Metric   `json:"port"`
	Worse bool     `json:"worse"`
}

const (
	// %[1]s the uploaded data file
	evalComparePromptFmt = "[TASK] the kernel ran this machine's port end to end and read numbers other repositories published. " +
		"for each published number, decide whether the conditions it was measured under correspond in proportion to this run's " +
		"(the model, the memory capacity), and if so which of the port's measured rates it is the same measurement as.\n" +
		"you may NOT start other conversations.\n\n[DATA] %[1]s\n\n[OUTPUT FORMAT]\n" +
		"only lines like `comparable: <number id> <port label>`, copying the port label exactly; nothing for a number that does not correspond.\n"
	gateEvalBelowExternalFmt = "integration_below_external: %[1]q: the port %[2]g %[3]s, %[4]s %[5]s published %[6]g %[3]s under conditions the planner found corresponding (%[7]s is better)"
	evalCompareFile          = "eval_compare.md"
)

var evalComparableRe = regexp.MustCompile(`(?m)^\s*` + "`?" + `comparable:\s*(\d+)\s+(.+?)` + "`?" + `\s*$`)

// evalCompareData is the planner's data file: the measured conditions, the port's run, and
// every published number with the document it is in.
func evalCompareData(cond EvalConditions, e2e EvalBenchRun, clues []EvalClue, docs map[string]string) string {
	var b strings.Builder
	cj, _ := json.MarshalIndent(cond, "", " ")
	b.WriteString("## conditions of this run (measured by the kernel)\n```json\n" + string(cj) + "\n```\n\n")
	b.WriteString("## the port's end-to-end run on the model (its output)\n```\n" + e2e.Tail + "\n```\nport labels:\n")
	for _, m := range e2e.Metrics {
		b.WriteString(fmt.Sprintf("- %s = %g %s\n", m.Label, m.Value, m.Unit))
	}
	b.WriteString("\n## published numbers\n")
	for i, c := range clues {
		b.WriteString(fmt.Sprintf("%d. %s %s @ %s: %q = %g %s\n", i, c.Repo, c.File, shortSHA(c.SHA), c.Metric.Label, c.Metric.Value, c.Metric.Unit))
	}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteString("\n## the documents they are in\n")
	for _, k := range keys {
		b.WriteString("\n### " + k + "\n```\n" + truncDiff(docs[k], 30000) + "\n```\n")
	}
	return b.String()
}

// evalComparisons asks the planner which published numbers correspond to the port's rates and
// compares those.
func evalComparisons(pid int64, repo string, cond EvalConditions, e2e EvalBenchRun, clues []EvalClue, docs map[string]string) ([]EvalComparison, EvalCompareTrace) {
	tr := EvalCompareTrace{Published: len(clues), PortRates: len(e2e.Metrics)}
	if len(clues) == 0 || len(e2e.Metrics) == 0 {
		tr.NotAsked = "no published number or no rate of the port's run"
		return nil, tr
	}
	key := evalCompareKey(repo, cond, e2e, clues)
	evalCompareMu.Lock()
	out, asked := evalCompareAnswers[key]
	evalCompareMu.Unlock()
	if asked {
		tr.Reused = true
	} else {
		tr.Asked = true
		SetForkFiles([]ForkFile{{Name: evalCompareFile, Data: []byte(evalCompareData(cond, e2e, clues, docs))}})
		out = devPlannerRunner(pid, repo, fmt.Sprintf(evalComparePromptFmt, localUploadsPath(evalCompareFile))+DoneInstruction())
		evalCompareMu.Lock()
		evalCompareAnswers[key] = out
		evalCompareMu.Unlock()
	}
	port := map[string]Metric{}
	for _, m := range e2e.Metrics {
		port[m.Label] = m
	}
	var cmp []EvalComparison
	for _, m := range evalComparableRe.FindAllStringSubmatch(out, -1) {
		tr.Named++
		i, err := strconv.Atoi(m[1])
		pm, ok := port[evalLabel(m[2])]
		if err != nil || i < 0 || i >= len(clues) || !ok {
			tr.Ignored = append(tr.Ignored, m[0])
			continue
		}
		c := clues[i]
		if c.Metric.Unit != pm.Unit {
			log.Printf("eval: published number %d (%s) and port %q have different units — not compared", i, c.Metric.Unit, pm.Label)
			tr.Ignored = append(tr.Ignored, m[0])
			continue
		}
		cmp = append(cmp, EvalComparison{Clue: c, Port: pm, Worse: evalWorse(pm.Value, c.Metric.Value, pm.Better)})
	}
	log.Printf("eval: %s: the planner found %d of %d published number(s) comparable with the port's run", repo, len(cmp), len(clues))
	return cmp, tr
}

// evalCompareAnswers: the planner's answer for a question already asked. Which published
// number is comparable with which of the port's rates depends on the numbers, the conditions
// and the rates' labels, not on the rates' values: 11/10 23:06, 23:08 and 23:33 the planner
// was asked the same question for three verifications and answered 0 of 68 each time.
var (
	evalCompareMu      sync.Mutex
	evalCompareAnswers = map[string]string{}
)

// evalCompareKey: what the planner's answer depends on.
func evalCompareKey(repo string, cond EvalConditions, e2e EvalBenchRun, clues []EvalClue) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%d\x00", repo, cond.ModelName, len(cond.GPUs))
	for _, m := range e2e.Metrics {
		fmt.Fprintf(h, "%s|%s\x00", m.Label, m.Unit)
	}
	for _, c := range clues {
		fmt.Fprintf(h, "%s|%s|%s|%s|%g|%s\x00", c.Repo, c.SHA, c.File, c.Metric.Label, c.Metric.Value, c.Metric.Unit)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// EvalCompareTrace: what the comparison did, kept in the evaluation record — 11/09 the first
// record on the machine said "compared": null, which reads the same whether the planner was
// never asked, answered nothing, or named numbers the kernel could not use.
type EvalCompareTrace struct {
	Published int      `json:"published"`           // numbers it could have named
	PortRates int      `json:"port_rates"`          // rates the port's run printed
	Asked     bool     `json:"asked"`               // the planner was asked
	Reused    bool     `json:"reused,omitempty"`    // its answer to the same question was reused
	NotAsked  string   `json:"not_asked,omitempty"` // why not
	Named     int      `json:"named"`               // "comparable:" lines it answered
	Ignored   []string `json:"ignored"`             // those the kernel could not use
}

// evalExternalGate: the first published number the port's run is worse than.
func evalExternalGate(cmp []EvalComparison) (kind, text string) {
	for _, c := range cmp {
		if c.Worse {
			return "integration_below_external", fmt.Sprintf(gateEvalBelowExternalFmt, c.Port.Label, c.Port.Value, c.Port.Unit,
				c.Clue.Repo, c.Clue.File, c.Clue.Metric.Value, c.Port.Better)
		}
	}
	return "", ""
}
