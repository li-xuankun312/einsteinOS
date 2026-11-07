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
	in.Build.Exes = append(in.Build.Exes, toolchainDirs[0]+"/clang++")
	if _, text := unitGates(in); strings.Contains(text, "toolchain_unused") {
		t.Fatalf("the toolchain ran: %s", text)
	}
}
