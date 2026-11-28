package kernel

// eval_tokens.go — the token ids the integration's end-to-end run printed: the prompt it was
// given and the tokens it generated. The kernel checks them itself: its judge runs the model's
// own implementation on that prompt in an environment of its own (eval_correct.go,
// eval_judge.go); a run that does not print them has its correctness unmeasured. 11/20 the
// gate said they "could not be checked against an implementation from outside the pipeline",
// and 23:07 vllm entered e2e.sh as its "correctness inference" (cb0950d).

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// %[1]s the script, %[2]s the line, %[3]s the output
const gateCorrectnessUnmeasuredFmt = "integration_correctness_unmeasured: the process of %[1]s that ran the port on the model printed no %[2]q line of token ids; the kernel's judge runs the model's own implementation itself, in an environment of its own, on the prompt that line gives and compares the ids; output: %[3]s"

// e2eTokenLineRe: "<what>: <id> <id> …" on a line of its own (11/09: "generated: 220 248046 198 …")
var e2eTokenLineRe = regexp.MustCompile(`(?m)^\s*(prompt|generated)\s*:\s*((?:\d+[\s,]*)+)$`)

// e2eTokens reads the last prompt and generated lines of output.
func e2eTokens(output string) (prompt, generated []int) {
	for _, m := range e2eTokenLineRe.FindAllStringSubmatch(output, -1) {
		var ids []int
		for _, f := range strings.FieldsFunc(m[2], func(r rune) bool { return r == ' ' || r == ',' || r == '\t' || r == '\r' }) {
			if n, err := strconv.Atoi(f); err == nil {
				ids = append(ids, n)
			}
		}
		if m[1] == "prompt" {
			prompt = ids
		} else {
			generated = ids
		}
	}
	return prompt, generated
}

// e2eTokensGate: the run printed both its prompt's and its generated token ids. 11/09 and
// 11/10 it printed only "generated: …" and the one check of what it computed was its own
// chunked prefill against its own sequential prefill.
func e2eTokensGate(br EvalBenchRun) (kind, text string) {
	for _, l := range []struct {
		name string
		ids  []int
	}{{"prompt", br.PromptIDs}, {"generated", br.GeneratedIDs}} {
		if len(l.ids) == 0 {
			return "integration_correctness_unmeasured", fmt.Sprintf(gateCorrectnessUnmeasuredFmt, br.Bench.File, l.name+":", br.Tail)
		}
	}
	return "", ""
}
