package kernel

import (
	"strings"
	"testing"
)

// 11/04 03:13:42: ple.cu and qsa.cu were reported missing though PleBlockGpu.lean and
// QsaCacheIndexerAttention.lean covered them. A SOURCES line names the paths exactly.
func TestASourcesLineCoversShortNamedFiles(t *testing.T) {
	if !strings.Contains(theorizeFacts, "SOURCES:") {
		t.Fatal("the module header must ask for the SOURCES line")
	}
	files := []string{"src/kernels/cuda/ple.cu", "src/kernels/cuda/qsa.cu"}
	without := "/-!\n# MODULE: ple_block_gpu\nPer-layer embedding block on the GPU.\n-/"
	if c := ChunkCoverage(files, without); len(c.Missing) != 2 {
		t.Fatalf("without SOURCES the short names are not found: %+v", c)
	}
	with := "/-!\n# MODULE: ple_block_gpu\nSOURCES: src/kernels/cuda/ple.cu\n-/\n/-!\n# MODULE: qsa_cache\nSOURCES: src/kernels/cuda/qsa.cu, src/core/layer.cpp\n-/"
	if c := ChunkCoverage(files, with); len(c.Missing) != 0 || c.How["src/kernels/cuda/ple.cu"] != "path" {
		t.Fatalf("SOURCES covers by path: %+v", c)
	}
}
