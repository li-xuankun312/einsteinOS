package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The 11/03 build.sh shape: skip the CUDA compile when the toolchain is absent and exit 0.
// With the toolchain hidden it still passes, so the gate fails it; a build that calls the
// toolchain fails when it is hidden, and passes the gate.
func TestBuildMustFailWithoutToolchain(t *testing.T) {
	if err := exec.Command("unshare", "-m", "--propagation", "private", "true").Run(); err != nil {
		t.Skipf("no mount namespace here: %v", err)
	}
	tc := t.TempDir()
	os.WriteFile(filepath.Join(tc, "clang++"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	old := toolchainDirs
	defer func() { toolchainDirs = old }()
	toolchainDirs = []string{tc}

	ws := t.TempDir()
	write := func(body string) {
		os.MkdirAll(filepath.Join(ws, ".einsteinOS/verify/u1"), 0o755)
		os.WriteFile(filepath.Join(ws, ".einsteinOS/verify/u1/build.sh"), []byte(body), 0o755)
	}
	write("if [ ! -x " + tc + "/clang++ ]; then echo 'corex toolchain not present on this host: skipping'; fi\necho BUILD OK\n")
	nt := buildWithoutToolchain(ws, ".einsteinOS/verify/u1/build.sh", time.Minute)
	if !nt.Measured || nt.Exit != 0 {
		t.Fatalf("the skipping build was not measured passing: %+v", nt)
	}
	if k, txt := noToolchainGate(nt); k != "build_passes_without_toolchain" || !strings.Contains(txt, "skipping") {
		t.Fatalf("%s: %s", k, txt)
	}

	write("set -e\n" + tc + "/clang++ -x cuda -c src/a.cu\necho BUILD OK\n")
	nt = buildWithoutToolchain(ws, ".einsteinOS/verify/u1/build.sh", time.Minute)
	if !nt.Measured || nt.Exit == 0 {
		t.Fatalf("a build that needs the toolchain must fail without it: %+v", nt)
	}
	if k, _ := noToolchainGate(nt); k != "" {
		t.Fatalf("gate %s for a build that needs the toolchain", k)
	}
	if _, err := os.Stat(filepath.Join(ws, ".einsteinOS/verify/u1/build.sh")); err != nil {
		t.Fatal("the workspace itself must not be touched")
	}
	if k, _ := noToolchainGate(NoToolchainBuild{Err: "unshare: permission denied"}); k != "build_toolchain_dependence_unmeasured" {
		t.Fatalf("no measurement is a failure, got %q", k)
	}
	// through the gates: a GPU unit whose build passes with the toolchain hidden fails
	in := GateInput{Req: UnitRequirements{RequireGPU: true}, Compilers: []string{tc + "/clang++"},
		Build:       ProcTrace{Exes: []string{tc + "/clang++"}, Cmdlines: []string{tc + "/clang++ -x cuda -c a.cu"}},
		Test:        ProcTrace{Devices: []string{"/dev/iluvatar0"}},
		NoToolchain: NoToolchainBuild{Measured: true, Exit: 0, Out: "BUILD OK"}}
	if k, _ := unitGates(in); k != "build_passes_without_toolchain" {
		t.Fatalf("gates: %q", k)
	}
}

// When the first way to get a mount namespace is refused, the next one is used, and the
// refusals are what an unmeasured result reports.
func TestHidingFallsBackToTheNextNamespace(t *testing.T) {
	if err := exec.Command("unshare", "-m", "--propagation", "private", "true").Run(); err != nil {
		t.Skipf("no mount namespace here: %v", err)
	}
	old := hideNamespaces
	defer func() { hideNamespaces = old }()
	hideNamespaces = [][]string{{"--no-such-option"}, {"-m", "--propagation", "private"}}
	tc := t.TempDir()
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "b.sh"), []byte("ls "+tc+" | wc -l\n"), 0o755)
	os.WriteFile(filepath.Join(tc, "clang++"), []byte("x"), 0o755)
	code, out, err := hideToolchainRun(ws, "b.sh", []string{tc}, time.Minute)
	if err != nil || code != 0 || strings.TrimSpace(out) != "0" {
		t.Fatalf("fallback: code=%d out=%q err=%v", code, out, err)
	}
	hideNamespaces = [][]string{{"--no-such-option"}}
	if _, _, err := hideToolchainRun(ws, "b.sh", []string{tc}, time.Minute); err == nil || !strings.Contains(err.Error(), "--no-such-option") {
		t.Fatalf("all refused must be an error naming them: %v", err)
	}
}

// When the tmpfs mount is refused, an empty directory bound over the toolchain hides it.
func TestHidingFallsBackToABindMount(t *testing.T) {
	if err := exec.Command("unshare", "-m", "--propagation", "private", "true").Run(); err != nil {
		t.Skipf("no mount namespace here: %v", err)
	}
	old := hideMountFmts
	defer func() { hideMountFmts = old }()
	hideMountFmts[0] = "false %[1]q" // the tmpfs mount refused
	tc := t.TempDir()
	os.WriteFile(filepath.Join(tc, "clang++"), []byte("x"), 0o755)
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "b.sh"), []byte("ls "+tc+" | wc -l\n"), 0o755)
	code, out, err := hideToolchainRun(ws, "b.sh", []string{tc}, time.Minute)
	if err != nil || code != 0 || strings.TrimSpace(out) != "0" {
		t.Fatalf("bind fallback: code=%d out=%q err=%v", code, out, err)
	}
	if _, err := os.Stat(filepath.Join(tc, "clang++")); err != nil {
		t.Fatal("the toolchain itself must be untouched outside the namespace")
	}
}
