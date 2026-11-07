package kernel

// local_result.go — what a conversation is told about a command the local machine ran.
//
// The shadow runs the child's commands on the local machine and writes the result
// back to the child as "the real output". It cut that result to its FIRST 2000
// characters (500 in the SSE-recovery path), with nothing but "..." to show it had
// cut anything. Two things followed:
//
//   - the part that matters is often at the end: a build prints its errors last,
//     a loop over files prints the last file's verdict last;
//   - the child could not tell what was missing, so it did the only thing it could:
//     ask again. 2026/11/02, theory chunk 1: `for f in A B C D E F G; verify $f`
//     printed A's verdict and was cut; the next round verified B..G, then C..G, then
//     D..G, E..G, F..G — one file per round, five rounds, each a call in the
//     9-per-10-minutes window, for one verdict each.
//
// Now: the cap is much larger, a cut keeps the head AND the tail, and the cut is
// announced with the sizes, so the child knows what it has and can ask for exactly
// the rest. One round's results share a budget, so a round of many commands cannot
// flood the conversation.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultLocalResultCap   = 16000
	defaultLocalRoundBudget = 40000
)

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

// LocalResultCap is the most of one command's output a conversation is given.
// EINSTEIN_LOCAL_RESULT_CAP overrides it.
func LocalResultCap() int { return envInt("EINSTEIN_LOCAL_RESULT_CAP", defaultLocalResultCap) }

// LocalRoundBudget is the most of ALL of one round's command outputs.
// EINSTEIN_LOCAL_ROUND_BUDGET overrides it.
func LocalRoundBudget() int { return envInt("EINSTEIN_LOCAL_ROUND_BUDGET", defaultLocalRoundBudget) }

const cutHint = "use grep/sed/head/tail to read the part you need"

// cutMarker is the line that says output was cut; other code looks for it.
func cutMarker(omitted, shown, total int) string {
	return fmt.Sprintf("\n[… %d bytes omitted (%d of %d shown) — %s …]\n", omitted, shown, total, cutHint)
}

// WasCut reports whether s carries a cut marker.
func WasCut(s string) bool {
	return strings.Contains(s, " bytes omitted (") && strings.Contains(s, cutHint)
}

// ShapeOutput returns s unchanged if it fits in limit bytes; otherwise its head
// (60%) and tail (40%) around a marker that says how much was left out. The result
// is never longer than limit plus the marker, and is cut on rune boundaries.
func ShapeOutput(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	head := limit * 6 / 10
	tail := limit - head
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	start := len(s) - tail
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	omitted := start - head
	return s[:head] + cutMarker(omitted, head+(len(s)-start), len(s)) + s[start:]
}

// ShapeRound shapes each result to perLimit, then, if together they still exceed
// budget, shrinks the largest ones until they fit: results shorter than their fair
// share are left alone, the rest split what remains.
func ShapeRound(results []string, perLimit, budget int) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = ShapeOutput(r, perLimit)
	}
	total := 0
	for _, r := range out {
		total += len(r)
	}
	if budget <= 0 || total <= budget {
		return out
	}
	limit := make([]int, len(out))
	for i := range limit {
		limit[i] = -1 // -1: not yet fixed
	}
	remaining, open := budget, len(out)
	for changed := true; changed && open > 0; {
		changed = false
		share := remaining / open
		for i, r := range out {
			if limit[i] == -1 && len(r) <= share {
				limit[i] = len(r)
				remaining -= len(r)
				open--
				changed = true
			}
		}
	}
	if open > 0 {
		share := remaining / open
		for i := range limit {
			if limit[i] == -1 {
				limit[i] = share
			}
		}
	}
	for i := range out {
		// out[i] may already carry a marker from the per-result cut; re-cutting keeps
		// the markers honest only if we cut the ORIGINAL, so do that
		out[i] = ShapeOutput(results[i], limit[i])
	}
	return out
}

// LocalOutputCap is how many characters the hardware probe is told a command can
// print before it is cut. Kept as one number with LocalResultCap.
func LocalOutputCap() int { return LocalResultCap() }
