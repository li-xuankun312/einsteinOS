package kernel

// turn_budget.go — how much a conversation may do, as numbers its prompt can be given.
//
// In the 11/02 and 11/03 logs 167 of 170 rounds ended because the model chose
// end_turn (3 were SSE breaks); none ran out of tool calls, and the most any round used
// was 22. A theory chunk does need a round per Axle check (Axle answers in the next
// message), but between checks it can read and write as much as it likes. Its prompt
// now says so with the real numbers.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ConvRoundCeiling caps every conversation's rounds in the model layer, whatever its
// termination condition allows (MaxRounds(40) for a theory chunk still stops at this).
var ConvRoundCeiling = 20

// TurnToolBudget is how many tool calls one round may make.
var TurnToolBudget = 40

// roundLimitOf is the smallest MaxRounds limit an Or of conditions holds (0: none).
func roundLimitOf(c TerminationCondition) int {
	switch v := c.(type) {
	case *MaxRounds:
		return v.Limit
	case *Or:
		best := 0
		for _, sub := range v.Conditions {
			if n := roundLimitOf(sub); n > 0 && (best == 0 || n < best) {
				best = n
			}
		}
		return best
	}
	return 0
}

// RoundBudget is the number of rounds a conversation of this role really gets.
func RoundBudget(role string) int {
	n := roundLimitOf(TerminationForRole(role))
	if n == 0 || n > ConvRoundCeiling {
		return ConvRoundCeiling
	}
	return n
}

// budgetFmt: %[1]d rounds, %[2]d tool calls per round.
const budgetFmt = `BUDGET — up to %[1]d rounds, and up to %[2]d tool calls in each round.
everything you need is in this message. this work means reading many files and writing many:
use the calls, a round that uses all of them is fine — the kernel continues you in the next round.
every Axle answer reaches you at the start of your next round, so batch your checks.
`

// BudgetBlock is the budget section of a prompt for role.
func BudgetBlock(role string) string {
	return fmt.Sprintf(budgetFmt, RoundBudget(role), TurnToolBudget)
}

// ---- sorry, measured where the files are mirrored --------------------------------------

const (
	outputsSorryHeadFmt = "[measured on this machine] %[1]d declaration(s) in %[2]d of the %[3]d .lean file(s) in %[4]s still use sorry:\n"
	outputsSorryLineFmt = "  %[1]s: %[2]s\n"
	outputsSorryRefFmt  = "the proofs under %[1]s show how these are built.\n"
	outputsNoSorryFmt   = "[measured on this machine] no sorry in the %[1]d .lean file(s) in %[2]s\n"
)

// outputsLeanFn reads the .lean files the shadow mirrored into dir (a variable for tests).
var outputsLeanFn = readLeanFiles

// OutputsSorryNote measures sorry in the theory outputs directory on this machine — the
// copy the shadow keeps of what the conversation wrote — for the writeback that carries
// an Axle answer. "" when the directory holds no .lean file.
func OutputsSorryNote() string {
	files := outputsLeanFn(TheoryOutputsDir)
	if len(files) == 0 {
		return ""
	}
	sites := sorrySites(files)
	if len(sites) == 0 {
		return fmt.Sprintf(outputsNoSorryFmt, len(files), TheoryOutputsDir)
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf(outputsSorryHeadFmt, sorryCount(sites), len(sites), len(files), TheoryOutputsDir))
	for _, s := range sites {
		b.WriteString(fmt.Sprintf(outputsSorryLineFmt, s.File, strings.Join(s.Decls, ", ")))
	}
	b.WriteString(fmt.Sprintf(outputsSorryRefFmt, ProofReferencePath()))
	return b.String()
}

// readLeanFiles reads dir/*.lean, by name.
func readLeanFiles(dir string) []chunkFile {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.lean"))
	sort.Strings(paths)
	var out []chunkFile
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		out = append(out, chunkFile{RemotePath: p, Name: filepath.Base(p), Content: b})
	}
	return out
}

// IsTheoryRole: a conversation that writes theory .lean files into TheoryOutputsDir.
func IsTheoryRole(role string) bool { return strings.HasPrefix(role, "theorize") }

// WritebackNote is what the kernel adds, measured, to a writeback of a conversation of
// this role: for theory, the sorry state of its files. A continued conversation has no
// fork role; its registered one is used.
func WritebackNote(role, convID string) string {
	if role == "" && convID != "" {
		role = ConvManifest(convID).Role
	}
	if !IsTheoryRole(role) {
		return ""
	}
	return OutputsSorryNote()
}
