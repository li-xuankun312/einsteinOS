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

// hideToolchainRun runs script in dir with every one of dirs replaced by an empty tmpfs.
var hideToolchainRun = func(dir, script string, dirs []string, timeout time.Duration) (int, string, error) {
	var mounts []string
	for _, d := range dirs {
		mounts = append(mounts, fmt.Sprintf("mount -t tmpfs -o size=1m none %q", d))
	}
	inner := strings.Join(mounts, " && ") + " && cd " + fmt.Sprintf("%q", dir) + " && exec bash " + fmt.Sprintf("%q", script) + " 2>&1"
	cmd := exec.Command("timeout", fmt.Sprint(int(timeout.Seconds())), "unshare", "-m", "--propagation", "private", "sh", "-c", inner)
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		code := ee.ExitCode()
		// the mount itself failing means nothing was measured
		if strings.Contains(string(out), "mount:") || strings.Contains(string(out), "unshare:") {
			return -1, string(out), fmt.Errorf("%s", tailText(string(out), 300))
		}
		return code, string(out), nil
	}
	if err != nil {
		return -1, string(out), err
	}
	return 0, string(out), nil
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
		return "build_toolchain_dependence_unmeasured", fmt.Sprintf(gateNoToolchainUnmeasuredFmt, hidden, nt.Err)
	case nt.Exit == 0:
		return "build_passes_without_toolchain", fmt.Sprintf(gateBuildWithoutToolchainFmt, hidden, nt.Out)
	}
	return "", ""
}
