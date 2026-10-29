package kernel

import (
	"strings"
	"testing"
)

// Chunk 1's assigned files (Strata core engine).
var chunk1Files = []string{
	"include/strata/core/expert_cache.hpp", "src/core/expert_cache.cpp",
	"include/strata/core/expert_source.hpp", "src/core/expert_source.cpp",
	"include/strata/core/graph.hpp", "src/core/graph.cpp",
	"include/strata/core/layer.hpp", "src/core/layer.cpp",
	"include/strata/core/layout.hpp", "src/core/layout.cpp",
}

// What ref_split actually merged for chunk 1 (1595 bytes): five bare Continues,
// each answered with an apology (export e39ca4ef).
const chunk1Junk = `t looks like you're asking me to continue, but I don't have any prior context in this conversation to pick up from. Could you let me know what you'd like help with? I'm happy to assist with whatever you need.

t seems like there might be some prior context I'm missing — this conversation doesn't have any earlier messages for me to continue from.

t looks like you've included a note about local tool results, but there aren't any actual results or prior context attached.

I appreciate your patience, but I'm not seeing any prior context, tool results, or content to continue from in this conversation.

I understand you'd like me to continue, but there's genuinely no prior context, tool output, or task visible in this conversation for me to build on.`

// What was actually persisted for that chunk, later (abridged from the export).
const chunk1Real = `I produced **12 Lean 4 theory files** covering all 10 assigned source files from Strata's core engine:
| Source File(s) | Theory File |
| ` + "`expert_cache.hpp/cpp`" + ` | **ExpertCache.lean** |
| ` + "`expert_source.hpp/cpp`" + ` | **ExpertSource.lean** |
| ` + "`graph.hpp/cpp`" + ` | **GraphRegistry.lean** |
| ` + "`layout.hpp/cpp`" + ` | **ModelLayout.lean** |
| ` + "`layer.hpp`" + ` | **LayerComposition.lean** |
| ` + "`layer.cpp`" + ` (1064 lines, split into 6 modules) | **LayerImpl.lean** |`

func TestCoverage_ApologiesCoverNothingAndAreJunk(t *testing.T) {
	if len(chunk1Junk) < 500 {
		t.Fatalf("fixture must exceed the old 500-byte threshold, got %d", len(chunk1Junk))
	}
	cov := ChunkCoverage(chunk1Files, chunk1Junk)
	if cov.CoveredCount() != 0 {
		t.Fatalf("apologies cover no files: %v", cov.Covered)
	}
	// the old check (len < 500) accepted this; the new one must not
	if !chunkIsJunk(chunk1Junk, cov) {
		t.Fatalf("1.5KB of apologies must be junk")
	}
}

func TestCoverage_RealSummaryCoversAllTenFiles(t *testing.T) {
	cov := ChunkCoverage(chunk1Files, chunk1Real)
	if cov.CoveredCount() != 10 || len(cov.Missing) != 0 {
		t.Fatalf("want 10/10, got %d covered, missing %v (how=%v)", cov.CoveredCount(), cov.Missing, cov.How)
	}
	if chunkIsJunk(chunk1Real+strings.Repeat("x", 30000), cov) {
		t.Fatalf("a real, covering result must not be junk")
	}
	// graph.cpp is only reachable through the "graph.hpp/cpp" shorthand
	if cov.How["src/core/graph.cpp"] != "stem.ext" {
		t.Fatalf("graph.cpp should match via stem.ext, got %q", cov.How["src/core/graph.cpp"])
	}
}

func TestCoverage_StemMatchingIsCaseAndUnderscoreInsensitive(t *testing.T) {
	cov := ChunkCoverage([]string{"src/kernels/quantized_scaled_attention.cu"}, "namespace Strata.QuantizedScaledAttention")
	if cov.CoveredCount() != 1 || cov.How["src/kernels/quantized_scaled_attention.cu"] != "stem" {
		t.Fatalf("CamelCase should match snake_case stem: %+v", cov)
	}
}

func TestCoverage_ShortGenericStemsNeedAFileReference(t *testing.T) {
	files := []string{"src/core/layer.cpp", "src/core/graph.cpp", "src/main.cpp"}
	// prose that merely uses the words
	prose := "The layer norm and the graph. A main idea: every layer feeds the next graph node."
	cov := ChunkCoverage(files, prose)
	if cov.CoveredCount() != 0 {
		t.Fatalf("generic words must not count as coverage: %v how=%v", cov.Covered, cov.How)
	}
	cov = ChunkCoverage(files, "see layer.cpp and graph.hpp for details")
	if cov.CoveredCount() != 2 {
		t.Fatalf("explicit file references must count: %v", cov.Covered)
	}
}

func TestCoverage_FullPathAndBasename(t *testing.T) {
	cov := ChunkCoverage([]string{"src/a/long_module_name.cpp", "src/b/other_thing.cpp"},
		"-- source: src/a/long_module_name.cpp\n-- also other_thing.cpp")
	if cov.How["src/a/long_module_name.cpp"] != "path" || cov.How["src/b/other_thing.cpp"] != "basename" {
		t.Fatalf("how=%v", cov.How)
	}
}

// 256-byte account-error stubs were merged as "theory" in 8 of 12 chunks.
func TestJunk_ErrorStubsAndLongUnnamedResults(t *testing.T) {
	stub := strings.Repeat("e", 256)
	if !chunkIsJunk(stub, ChunkCoverage(chunk1Files, stub)) {
		t.Fatalf("256-byte stub must be junk")
	}
	// a long result that happens not to name files is more likely a naming mismatch
	// than an empty answer: keep it, do not throw away 100KB of theory
	long := strings.Repeat("theorem t : True := trivial\n", 5000)
	if chunkIsJunk(long, ChunkCoverage(chunk1Files, long)) {
		t.Fatalf("a long substantive result must survive a naming mismatch")
	}
}

func TestGate_BlocksTheStrataRun(t *testing.T) {
	// 13 of 114 files had theory: chunks 2 and 4 (6+7); everything else junk
	setSplitCoverage("Niko1221/Strata", "01529aaa82b9", SplitCoverage{
		Total: 114, Covered: 13, Chunks: 12, Junk: []int{1, 3, 5, 6, 7, 8, 9, 10, 11, 12},
		Missing: []string{"src/core/expert_cache.cpp", "src/core/graph.cpp"},
	})
	err := theoryCoverageGate("Niko1221/Strata", "01529aaa82b9")
	if err == nil {
		t.Fatal("11% coverage must block")
	}
	msg := err.Error()
	for _, want := range []string{"BLOCKED", "13/114", "junk chunks", "expert_cache.cpp"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q: %s", want, msg)
		}
	}
}

func TestGate_PassesGoodCoverageAndHonoursOverride(t *testing.T) {
	setSplitCoverage("o/good", "aaaaaaaaaaaa", SplitCoverage{Total: 100, Covered: 60})
	if err := theoryCoverageGate("o/good", "aaaaaaaaaaaa"); err != nil {
		t.Fatalf("60%% must pass the default 50%% gate: %v", err)
	}
	setSplitCoverage("o/bad", "bbbbbbbbbbbb", SplitCoverage{Total: 100, Covered: 10})
	t.Setenv("EINSTEIN_THEORY_MIN_COVERAGE", "0")
	if err := theoryCoverageGate("o/bad", "bbbbbbbbbbbb"); err != nil {
		t.Fatalf("0 disables the gate: %v", err)
	}
	t.Setenv("EINSTEIN_THEORY_MIN_COVERAGE", "0.05")
	if err := theoryCoverageGate("o/bad", "bbbbbbbbbbbb"); err != nil {
		t.Fatalf("10%% must pass a 5%% gate: %v", err)
	}
	t.Setenv("EINSTEIN_THEORY_MIN_COVERAGE", "garbage")
	if TheoryMinCoverage() != 0.5 {
		t.Fatalf("bad override must fall back to the default")
	}
}

func TestGate_NoMeasurementMeansNoOpinion(t *testing.T) {
	if err := theoryCoverageGate("never/split", "cccccccccccc"); err != nil {
		t.Fatalf("a leaf (unsplit) theory was never measured: %v", err)
	}
}
