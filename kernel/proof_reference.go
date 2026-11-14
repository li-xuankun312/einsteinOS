package kernel

// proof_reference.go — what happens when a theory chunk ends with `sorry` in its files.
//
// The theory prompt used to say "if you cannot prove something, write sorry", and
// nothing counted what came back: the chunk's own Axle check sends each file as both
// formal_statement and content, Axle answers okay:true with a warning for `sorry`, and
// the kernel's proof ledger only hears the Go HTTP path while every batch check went
// through the curl fallback. A chunk could end with unproved theorems and nobody knew.
//
// Now the kernel measures it on the files it downloaded (ContainsSorry, comments
// ignored) and, while there are any, continues the conversation with the facts: which
// file, which declaration, and where the reference proofs are — the same openai/math
// clone the prompt has the chunk read before it writes anything. It stops when nothing
// is left, when a round removes none, or after chunkSorryRounds.

import (
	"fmt"
	"log"
	"regexp"
	"strings"
)

// The Lean proof reference every theory chunk clones and reads.
var (
	proofRefURL = "https://github.com/openai/math.git"
	proofRefDir = "/tmp/openai_math"
	proofRefSub = "lean/OAI"
)

// chunkSorryRounds bounds extra continues for one chunk after it ended. 0: none — the
// sorry count reaches the chunk during its own rounds, with every Axle answer
// (OutputsSorryNote, turn_budget.go); here it is only measured and logged.
var chunkSorryRounds = 0

// ProofReferencePath is where the reference proofs are read from.
func ProofReferencePath() string {
	return strings.TrimRight(proofRefDir, "/") + "/" + strings.Trim(proofRefSub, "/") + "/"
}

const (
	chunkSorryHeadFmt = "%[1]d declaration(s) in %[2]d file(s) still use sorry:\n"
	chunkSorryLineFmt = "  %[1]s: %[2]s\n"
	chunkSorryTailFmt = "Lean 4 proofs: the files under %[1]s\n"
)

// sorrySite is one file and the declarations in it that still use sorry.
type sorrySite struct {
	File  string
	Decls []string
}

var leanDeclRe = regexp.MustCompile(`^\s*(?:@\[[^\]]*\]\s*)?(?:(?:private|protected|noncomputable|partial)\s+)*(theorem|lemma|def|abbrev|instance|example)\b\s*([^\s:({\[]*)`)

// sorrySites measures, file by file, which declarations use sorry (comments ignored).
func sorrySites(files []chunkFile) []sorrySite {
	var out []sorrySite
	for _, f := range files {
		src := stripLeanComments(string(f.Content))
		if !sorryRe.MatchString(src) {
			continue
		}
		site := sorrySite{File: f.Name}
		decl, seen := "", map[string]bool{}
		for _, l := range strings.Split(src, "\n") {
			if m := leanDeclRe.FindStringSubmatch(l); m != nil {
				decl = m[2]
				if decl == "" {
					decl = m[1] // an anonymous example / instance
				}
			}
			if sorryRe.MatchString(l) {
				name := decl
				if name == "" {
					name = "(top level)"
				}
				if !seen[name] {
					seen[name] = true
					site.Decls = append(site.Decls, name)
				}
			}
		}
		out = append(out, site)
	}
	return out
}

func sorryCount(sites []sorrySite) int {
	n := 0
	for _, s := range sites {
		n += len(s.Decls)
	}
	return n
}

// chunkSorryPrompt is the kernel's continue message: the measured sites and the reference.
func chunkSorryPrompt(sites []sorrySite) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(chunkSorryHeadFmt, sorryCount(sites), len(sites)))
	for _, s := range sites {
		b.WriteString(fmt.Sprintf(chunkSorryLineFmt, s.File, strings.Join(s.Decls, ", ")))
	}
	b.WriteString(fmt.Sprintf(chunkSorryTailFmt, ProofReferencePath()))
	return Bagu(chunkSorryWhere, fmt.Sprintf(chunkSorryWhatFmt, sorryCount(sites)), b.String(), fmt.Sprint(sorryCount(sites)), theoryTo())
}

// inserted into the shell when a chunk's files still use sorry: where, %[1]d how many
const chunkSorryWhere = "这个对话写的 .lean 文件"
const chunkSorryWhatFmt = "内核在这台机器上测到还有 %[1]d 处 sorry"

// hooks, so the loop can be tested without a server
var (
	sorrySettledFn  = func(convID string) bool { s, _, err := ConvSettled(convID); return err == nil && s }
	sorryContinueFn = func(convID, prompt string) { ContinueConv(convID, prompt) }
	sorryFilesFn    = chunkFilesFor
	sorryForgetFn   = forgetChunkFiles
)

// chunk_fill_sorry continues convID while its files use sorry, and returns what is left.
func chunk_fill_sorry(convID, tag string) []sorrySite {
	files := sorryFilesFn(convID)
	if len(files) == 0 {
		return nil // nothing downloaded: nothing measured
	}
	sites := sorrySites(files)
	for round := 1; round <= chunkSorryRounds && len(sites) > 0; round++ {
		if !sorrySettledFn(convID) {
			log.Printf("chunk_sorry: %s %d declaration(s) use sorry — NOT continuing (conversation not settled)", tag, sorryCount(sites))
			break
		}
		log.Printf("chunk_sorry: %s %d declaration(s) in %d file(s) use sorry — continue round %d", tag, sorryCount(sites), len(sites), round)
		sorryContinueFn(convID, chunkSorryPrompt(sites))
		sorryForgetFn(convID) // the files changed: download them again
		next := sorrySites(sorryFilesFn(convID))
		removed := sorryCount(sites) - sorryCount(next)
		sites = next
		if removed <= 0 {
			break
		}
	}
	if n := sorryCount(sites); n > 0 {
		var names []string
		for _, s := range sites {
			names = append(names, s.File+": "+strings.Join(s.Decls, ", "))
		}
		log.Printf("chunk_sorry: %s ends with %d declaration(s) using sorry: %s", tag, n, strings.Join(names, "; "))
	} else {
		log.Printf("chunk_sorry: %s no sorry in its files", tag)
	}
	return sites
}
