package kernel

import (
	"strings"
	"testing"
)

func TestAGPUUnitWhoseBuildNeverRanTheToolchainFails(t *testing.T) {
	req := UnitRequirements{GPUSources: []string{"src/x.cu"}}
	in := GateInput{Req: req, Compilers: []string{toolchainDirs[0] + "/clang++"}, Changed: []string{"src/x.cu"},
		Build: ProcTrace{Exes: []string{"/usr/bin/bash", "/usr/bin/g++"}}, Test: ProcTrace{Devices: []string{"/dev/iluvatar0"}}}
	if kind, text := unitGates(in); !strings.Contains(text, "toolchain_unused") {
		t.Fatalf("%s %s", kind, text)
	}
	// 11/24 00:5x "$CLANG" --version in build.sh, a build that compiled nothing: not a use
	clang := toolchainDirs[0] + "/clang++"
	in.Build.Exes = append(in.Build.Exes, clang)
	in.Build.Processes = []ProcSeen{{Exe: clang, Cmdline: clang + " --version"}}
	if _, text := unitGates(in); !strings.Contains(text, "toolchain_unused") {
		t.Fatalf("clang++ --version counted as the toolchain's use: %s", text)
	}
	in.Build.Processes = append(in.Build.Processes, ProcSeen{Exe: clang, Cmdline: clang + " -O2 -c src/x.cu -o build/x.o"})
	if _, text := unitGates(in); strings.Contains(text, "toolchain_unused") {
		t.Fatalf("the toolchain compiled src/x.cu: %s", text)
	}
}
