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

// RoundBudgetOf is the number of rounds a conversation under cond really gets.
func RoundBudgetOf(cond TerminationCondition) int {
	n := roundLimitOf(cond)
	if n == 0 || n > ConvRoundCeiling {
		return ConvRoundCeiling
	}
	return n
}

// RoundBudget is RoundBudgetOf the role's own condition.
func RoundBudget(role string) int { return RoundBudgetOf(TerminationForRole(role)) }

// WorkLoad is what one conversation is about to read and write, measured from what its
// prompt holds.
type WorkLoad struct {
	Read   string // what it reads ("source code: .cu ×4, .cpp ×3")
	Amount string // how much ("9 file(s), 2345 lines")
	Write  string // what it writes
}

// budgetFmt: %[1]s read, %[2]s amount, %[3]s write, %[4]d tool calls a round, %[5]d rounds.
const budgetFmt = `the information is complete. this time you read a lot of %[1]s (%[2]s) and write %[3]s. start.
using up the tool calls is fine: up to %[4]d in one round, and the kernel resets you for the next one (up to %[5]d rounds).
`

// BudgetBlock is the first section of a conversation's prompt.
func BudgetBlock(w WorkLoad, rounds int) string {
	return fmt.Sprintf(budgetFmt, w.Read, w.Amount, w.Write, TurnToolBudget, rounds)
}

// what theory and dev conversations read and write, as formats
const (
	sourceReadFmt   = "source code: %[1]s"
	sourceAmountFmt = "%[1]d file(s), %[2]d lines"
	theoryWrite     = "its .lean theory, one file per module"
	devReadFmt      = "theory: LAYER 1/2 of %[1]d module(s)"
	devAmountFmt    = "%[1]d KB of theory"
	devWrite        = "the code for them, with the build and test scripts"
)

// sourceTally counts the source files a prompt embeds, by extension.
type sourceTally struct {
	byExt map[string]int
	order []string
	files int
	lines int
}

func (t *sourceTally) Add(path string, lines int) {
	if t.byExt == nil {
		t.byExt = map[string]int{}
	}
	ext := filepath.Ext(path)
	if ext == "" {
		ext = filepath.Base(path)
	}
	if t.byExt[ext] == 0 {
		t.order = append(t.order, ext)
	}
	t.byExt[ext]++
	t.files++
	t.lines += lines
}

// Theory is the work of a theory conversation over these sources.
func (t sourceTally) Theory() WorkLoad {
	order := append([]string{}, t.order...)
	sort.SliceStable(order, func(i, j int) bool { return t.byExt[order[i]] > t.byExt[order[j]] })
	var parts []string
	for _, e := range order {
		parts = append(parts, fmt.Sprintf("%s ×%d", e, t.byExt[e]))
	}
	return WorkLoad{
		Read:   fmt.Sprintf(sourceReadFmt, strings.Join(parts, ", ")),
		Amount: fmt.Sprintf(sourceAmountFmt, t.files, t.lines),
		Write:  theoryWrite,
	}
}

// devWorkLoad is the work of a dev unit over its modules' theory.
func devWorkLoad(modules int, theoryBytes int) WorkLoad {
	return WorkLoad{
		Read:   fmt.Sprintf(devReadFmt, modules),
		Amount: fmt.Sprintf(devAmountFmt, (theoryBytes+1023)/1024),
		Write:  devWrite,
	}
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
