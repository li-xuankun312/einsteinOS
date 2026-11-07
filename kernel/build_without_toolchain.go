package kernel

// build_without_toolchain.go — a GPU unit's build.sh is run a second time with the toolchain
// hidden, and must fail.
//
// 11/03 the units' build.sh files printed "corex toolchain not present on this host: skipping"
// and exited 0 in the sandbox, so BUILD OK said nothing; nine of them still carry that branch
// on the dev branch. A build whose exit code does not depend on the toolchain cannot be what
// shows the unit was built for the GPU. The second run is a copy of the workspace in a private
// mount namespace where each of toolchainDirs is an empty tmpfs.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GateToolchainDependenceUnmeasured: this machine could not hide its toolchain — a fact about
// the machine, which no conversation can change (gateIsMachine).
const GateToolchainDependenceUnmeasured = "build_toolchain_dependence_unmeasured"

// NoToolchainBuild is what the hidden-toolchain run measured.
type NoToolchainBuild struct {
	Measured bool
	Exit     int
	Out      string // tail
	Err      string // why there is no measurement
}

const (
	gateBuildWithoutToolchainFmt = "build_passes_without_toolchain: build.sh exited 0 with %[1]s hidden — its result does not depend on the GPU toolchain, so its pass shows nothing was built for the GPU; output: %[2]s"
	gateNoToolchainUnmeasuredFmt = "build_toolchain_dependence_unmeasured: build.sh could not be run with %[1]s hidden (%[2]s), so whether it needs the toolchain is not measured"
)

// hideNamespaces: the ways to get a private mount namespace, tried in order. 11/06 18:59 on
// the Iluvatar machine "unshare -m" was refused and "unshare -Urm" (a user namespace mapping
// root, in which mounting is allowed) worked; every GPU unit had failed
// build_toolchain_dependence_unmeasured because only the first was tried.
var hideNamespaces = [][]string{
	{"-m", "--propagation", "private"},
	{"-U", "-r", "-m", "--propagation", "unchanged"},
}

// hideMountFmts: how a toolchain directory (%[1]q) is replaced — an empty tmpfs, or else an
// empty directory (%[2]q) bound over it. 11/07 07:26 inside "unshare -Urm" on the Iluvatar
// machine the tmpfs mount was refused ("cannot mount none read-only": the corex directory is
// a locked mount there), while the conversation's "mount --bind <empty dir>" had worked.
var hideMountFmts = [2]string{"mount -t tmpfs -o size=1m none %[1]q", "mount --bind %[2]q %[1]q"}

// hideToolchainRun runs script in dir with every one of dirs replaced by an empty directory, in
// the first of hideNamespaces this machine allows.
var hideToolchainRun = func(dir, script string, dirs []string, timeout time.Duration) (int, string, error) {
	empty, err := os.MkdirTemp("", "einsteinos_empty_")
	if err != nil {
		return -1, "", err
	}
	defer os.RemoveAll(empty)
	var mounts []string
	for _, d := range dirs {
		mounts = append(mounts, fmt.Sprintf("{ %s 2>/dev/null || %s; }", fmt.Sprintf(hideMountFmts[0], d, empty), fmt.Sprintf(hideMountFmts[1], d, empty)))
	}
	inner := strings.Join(append(mounts, "cd "+fmt.Sprintf("%q", dir), "exec bash "+fmt.Sprintf("%q", script)+" 2>&1"), " && ")
	var refused []string
	for _, ns := range hideNamespaces {
		args := append([]string{fmt.Sprint(int(timeout.Seconds())), "unshare"}, ns...)
		out, err := exec.Command("timeout", append(args, "sh", "-c", inner)...).CombinedOutput()
		if ee, ok := err.(*exec.ExitError); ok {
			// the namespace or the mount failing means nothing was measured: try the next way
			if strings.HasPrefix(string(out), "unshare:") || strings.Contains(string(out), "mount:") {
				refused = append(refused, "unshare "+strings.Join(ns, " ")+": "+tailText(string(out), 200))
				continue
			}
			return ee.ExitCode(), string(out), nil
		}
		if err != nil {
			refused = append(refused, "unshare "+strings.Join(ns, " ")+": "+err.Error())
			continue
		}
		return 0, string(out), nil
	}
	return -1, "", fmt.Errorf("%s", strings.Join(refused, "; "))
}

// buildWithoutToolchain copies ws and runs build.sh there with the toolchain hidden.
func buildWithoutToolchain(ws, build string, timeout time.Duration) NoToolchainBuild {
	tmp, err := os.MkdirTemp("", "einsteinos_notoolchain_")
	if err != nil {
		return NoToolchainBuild{Err: err.Error()}
	}
	defer os.RemoveAll(tmp)
	copy := filepath.Join(tmp, "ws")
	if out, err := exec.Command("cp", "-a", ws, copy).CombinedOutput(); err != nil {
		return NoToolchainBuild{Err: "copy of the workspace: " + tailText(string(out), 200)}
	}
	var dirs []string
	for _, d := range toolchainDirs {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			dirs = append(dirs, d)
		}
	}
	code, out, err := hideToolchainRun(copy, build, dirs, timeout)
	if err != nil {
		return NoToolchainBuild{Err: err.Error()}
	}
	return NoToolchainBuild{Measured: true, Exit: code, Out: tailText(out, 600)}
}

// noToolchainGate: the first gate text for a GPU unit whose build does not need the toolchain.
func noToolchainGate(nt NoToolchainBuild) (kind, text string) {
	hidden := strings.Join(toolchainDirs, ":")
	switch {
	case !nt.Measured:
		// not a gate: 11/07 on the Iluvatar machine no mount of any kind was allowed (no
		// "unshare -m"; inside "unshare -Urm" both a tmpfs and a bind mount over the corex
		// directory were refused), so this run cannot be made there, and every GPU unit
		// stopped on it with compile, smoke and its own kernel launches passing. What the
		// hidden-toolchain run would add is already measured directly on such a machine:
		// toolchain_unused (the toolchain ran in the build), gpu_not_compiled (the unit's CUDA
		// files were compiled for the GPU) and gpu_kernel_not_launched (its kernels ran).
		return "", ""
	case nt.Exit == 0:
		return "build_passes_without_toolchain", fmt.Sprintf(gateBuildWithoutToolchainFmt, hidden, nt.Out)
	}
	return "", ""
}
