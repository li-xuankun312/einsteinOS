package kernel

// launch_probe.go — the kernel launch probe: built from launchprobe/launch_probe.c with
// this machine's C compiler and preloaded into the processes of a GPU unit's test.sh.
//
// 11/04: "devices opened" was the only GPU measurement, and a test that opened a device and
// then ran its checks on the CPU passed it. The probe records the kernels a process
// actually launched.

import (
	"bufio"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

//go:embed launchprobe/launch_probe.c
var launchProbeSource string

// launchProbeDir is where the built probe is kept (one file per source version).
var launchProbeDir = func() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, ".einsteinOS", "launchprobe")
}

// launchProbeCompilers are tried in order; the toolchain's clang comes last.
func launchProbeCompilers() []string {
	out := []string{"cc", "gcc", "clang"}
	for _, d := range toolchainDirs {
		out = append(out, filepath.Join(d, "clang"))
	}
	return out
}

var launchProbeMu sync.Mutex

// launchProbeLib returns the path of the built probe, building it on first use.
func launchProbeLib() (string, error) {
	launchProbeMu.Lock()
	defer launchProbeMu.Unlock()
	sum := sha256.Sum256([]byte(launchProbeSource))
	dir := launchProbeDir()
	lib := filepath.Join(dir, "launch_probe_"+hex.EncodeToString(sum[:6])+".so")
	if st, err := os.Stat(lib); err == nil && st.Size() > 0 {
		return lib, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	src := filepath.Join(dir, "launch_probe.c")
	if err := os.WriteFile(src, []byte(launchProbeSource), 0o644); err != nil {
		return "", err
	}
	var errs []string
	for _, cc := range launchProbeCompilers() {
		path, err := exec.LookPath(cc)
		if err != nil {
			continue
		}
		tmp := lib + ".tmp"
		out, err := exec.Command(path, "-shared", "-fPIC", "-O2", "-o", tmp, src, "-ldl", "-lpthread").CombinedOutput()
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v: %s", path, err, strings.TrimSpace(string(out))))
			continue
		}
		if err := os.Rename(tmp, lib); err != nil {
			return "", err
		}
		return lib, nil
	}
	if len(errs) == 0 {
		return "", fmt.Errorf("no C compiler found (tried %s)", strings.Join(launchProbeCompilers(), ", "))
	}
	return "", fmt.Errorf("the launch probe did not build: %s", strings.Join(errs, "; "))
}

// probeLaunch is one "launch" line of the probe's log.
type probeLaunch struct {
	PID    int
	API    string
	Addr   uint64
	Base   uint64
	Object string
	Symbol string // from dladdr, "" when it had none
}

// probeLog is the probe's log, read back.
type probeLog struct {
	Loaded   map[int]string // pid → executable the probe was loaded into
	Launches []probeLaunch
}

// readProbeLog parses the probe's log file; a missing file is an empty log.
func readProbeLog(path string) probeLog {
	pl := probeLog{Loaded: map[int]string{}}
	f, err := os.Open(path)
	if err != nil {
		return pl
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), "\t")
		switch {
		case len(parts) == 3 && parts[0] == "load":
			if pid, err := strconv.Atoi(parts[1]); err == nil {
				pl.Loaded[pid] = parts[2]
			}
		case len(parts) == 7 && parts[0] == "launch":
			pid, err1 := strconv.Atoi(parts[1])
			addr, err2 := strconv.ParseUint(parts[3], 0, 64)
			base, err3 := strconv.ParseUint(parts[4], 0, 64)
			if err1 != nil || err2 != nil || err3 != nil {
				continue
			}
			sym := parts[6]
			if sym == "-" {
				sym = ""
			}
			pl.Launches = append(pl.Launches, probeLaunch{PID: pid, API: parts[2], Addr: addr, Base: base, Object: parts[5], Symbol: sym})
		}
	}
	return pl
}

// launchProbeEnv is the shell prelude that preloads the probe and names its log.
func launchProbeEnv(lib, logPath string) string {
	return fmt.Sprintf("export EINSTEINOS_LAUNCH_LOG=%s LD_PRELOAD=%s${LD_PRELOAD:+:$LD_PRELOAD}; ", shq(logPath), shq(lib))
}
