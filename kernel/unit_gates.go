package kernel

// unit_gates.go — what "verified" requires beyond two exit codes, judged on measurements.
//
// A gate is a fact about the unit the kernel measured (its changed files, the processes
// its build and test ran) set against what the reference says the unit is
// (UnitRequirements). The first gate that fails is the unit's failure kind, so the next
// attempt is told exactly that; every failed gate is in the text.

import (
	"fmt"
	"path"
	"strings"
)

// GateInput is everything a gate may look at.
type GateInput struct {
	Req     UnitRequirements
	Changed []string  // files the unit added or changed (repo-relative)
	Build   ProcTrace // what build.sh's processes did
	Test    ProcTrace // what test.sh's processes did
}

const gateGPUCodeMissingFmt = "gpu_code_missing: the unit's modules come from CUDA sources in the reference (%[1]s) but it wrote no .cu/.cuh file"

// unitGates returns the first failed gate's kind and the text of every failed gate.
func unitGates(in GateInput) (kind, text string) {
	var kinds, texts []string
	fail := func(k, t string) { kinds = append(kinds, k); texts = append(texts, t) }

	if in.Req.GPU() && len(changedGPUFiles(in.Changed)) == 0 {
		fail("gpu_code_missing", fmt.Sprintf(gateGPUCodeMissingFmt, strings.Join(in.Req.GPUSources, ", ")))
	}

	if len(kinds) == 0 {
		return "", ""
	}
	return kinds[0], strings.Join(texts, "\n")
}

// changedGPUFiles: the .cu/.cuh among the changed files.
func changedGPUFiles(changed []string) []string {
	var out []string
	for _, f := range changed {
		for _, ext := range gpuSourceExts {
			if path.Ext(f) == ext {
				out = append(out, f)
			}
		}
	}
	return out
}
