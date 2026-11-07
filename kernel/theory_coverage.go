package kernel

// theory_coverage.go — "did the theory actually cover the files it was asked to?"
//
// In the Strata run a chunk counted as done when its result was >= 500 bytes.
// Chunk 1 (10 files) "passed" with 1595 bytes — five apologies from five bare
// Continues ("I don't have any prior context") — and eight further chunks passed
// as 256-byte error stubs. The merged theory was 377,211 bytes, covered about 11%
// of the 114 files, and ref_bind recorded it as theorized.
//
// Coverage is measured against the files the chunk was assigned, not against the
// size of whatever text came back.

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Coverage reports which assigned source files a body of theory text mentions.
type Coverage struct {
	Total   int
	Covered []string
	Missing []string
	How     map[string]string // source path -> "path" | "basename" | "stem"
}

func (c Coverage) CoveredCount() int { return len(c.Covered) }

func (c Coverage) Fraction() float64 {
	if c.Total == 0 {
		return 1
	}
	return float64(len(c.Covered)) / float64(c.Total)
}

func alnumLower(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// minStemLen: a stem shorter than this ("layer", "graph", "main") is too generic
// to count on its own, because it appears in unrelated prose. Short stems must
// match by path or basename ("layer.cpp").
const minStemLen = 6

// ChunkCoverage checks every assigned file against the texts. A file is covered
// when a text contains its path, its basename, or — for stems of at least
// minStemLen characters — its stem in any case/underscore form (expert_cache,
// ExpertCache and expertcache all match).
func ChunkCoverage(files []string, texts ...string) Coverage {
	joined := strings.Join(texts, "\n")
	lower := strings.ToLower(joined)
	norm := alnumLower(joined)
	cov := Coverage{Total: len(files), How: map[string]string{}}
	for _, f := range files {
		pl := strings.ToLower(strings.TrimSpace(f))
		if pl == "" {
			cov.Total--
			continue
		}
		base := path.Base(pl)
		stem := strings.TrimSuffix(base, path.Ext(base))
		ns := alnumLower(stem)
		switch {
		case strings.Contains(lower, pl):
			cov.Covered = append(cov.Covered, f)
			cov.How[f] = "path"
		case strings.Contains(lower, base):
			cov.Covered = append(cov.Covered, f)
			cov.How[f] = "basename"
		case len(ns) >= minStemLen && strings.Contains(norm, ns):
			cov.Covered = append(cov.Covered, f)
			cov.How[f] = "stem"
		case len(ns) < minStemLen && shortStemRef(lower, stem):
			// "graph.hpp/cpp" names graph.cpp too; "the graph." does not match
			cov.Covered = append(cov.Covered, f)
			cov.How[f] = "stem.ext"
		default:
			cov.Missing = append(cov.Missing, f)
		}
	}
	return cov
}

// shortStemRef matches a short stem used as a file reference: stem, a dot, then a
// 1-4 letter extension ("graph.hpp", "graph.cpp", "layer.h").
func shortStemRef(lower, stem string) bool {
	if stem == "" {
		return false
	}
	re := regexp.MustCompile(`(^|[^a-z0-9_])` + regexp.QuoteMeta(stem) + `\.[a-z]{1,4}([^a-z0-9]|$)`)
	return re.MatchString(lower)
}

// maxJunkLen: a result shorter than this that covers none of its files is junk
// (apologies, error stubs). A long result that fails the name match is more
// likely a naming mismatch than an empty answer, so it is not discarded.
const maxJunkLen = 20000

// chunkIsJunk reports whether a chunk's result should be treated as no result.
func chunkIsJunk(result string, cov Coverage) bool {
	if len(result) < 500 {
		return true
	}
	return cov.Total > 0 && cov.CoveredCount() == 0 && len(result) < maxJunkLen
}

// ---------------------------------------------------------------------------
// per-split registry + gate
// ---------------------------------------------------------------------------

// SplitCoverage is the whole-split outcome recorded by ref_split.
type SplitCoverage struct {
	Total   int
	Covered int
	Missing []string
	Chunks  int
	Junk    []int // 1-based chunk numbers discarded as junk
}

var (
	splitCovMu sync.Mutex
	splitCov   = map[string]SplitCoverage{}
)

func splitCovKey(repo, sha string) string { return repo + "@" + sha }

func setSplitCoverage(repo, sha string, c SplitCoverage) {
	splitCovMu.Lock()
	splitCov[splitCovKey(repo, sha)] = c
	splitCovMu.Unlock()
}

func getSplitCoverage(repo, sha string) (SplitCoverage, bool) {
	splitCovMu.Lock()
	defer splitCovMu.Unlock()
	c, ok := splitCov[splitCovKey(repo, sha)]
	return c, ok
}

// TheoryMinCoverage is the fraction of assigned files the theory must cover
// before ref_bind accepts it. EINSTEIN_THEORY_MIN_COVERAGE overrides it
// (0 disables the gate).
func TheoryMinCoverage() float64 {
	if v := strings.TrimSpace(os.Getenv("EINSTEIN_THEORY_MIN_COVERAGE")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			return f
		}
	}
	return 0.5
}

// theoryCoverageGate refuses a theory that covers too little of its source.
// Blocking is deliberate: proceeding on 11% of the theory is what produced a
// 5-module spec for a 114-file project.
func theoryCoverageGate(repo, sha string) error {
	min := TheoryMinCoverage()
	if min <= 0 {
		return nil
	}
	c, ok := getSplitCoverage(repo, sha)
	if !ok || c.Total == 0 {
		return nil // not a split run; nothing was measured
	}
	frac := float64(c.Covered) / float64(c.Total)
	if frac >= min {
		return nil
	}
	show := c.Missing
	if len(show) > 12 {
		show = append(append([]string{}, show[:12]...), fmt.Sprintf("… +%d more", len(c.Missing)-12))
	}
	return fmt.Errorf("theory coverage gate BLOCKED %s@%s: %d/%d files covered (%.0f%% < %.0f%% required); "+
		"junk chunks discarded: %v; uncovered: %s",
		repo, short_sha(sha), c.Covered, c.Total, frac*100, min*100, c.Junk, strings.Join(show, ", "))
}
