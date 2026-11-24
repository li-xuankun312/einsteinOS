package kernel

// dev_verify.go — the dev unit's objective verdict, produced by the KERNEL on
// the REAL machine, and the kernel (not the child) putting the code on the
// branch.
//
// What the Strata run (2026/11/01) showed, per round:
//
//   - every "compile=pass" came from dev_collect_child's text fallback — the
//     child's own claim, not a return code. CompileResult{BuildCmd:"make"} never
//     fired because the children build with their own scripts, and even when it
//     matches it only looks for the word "make" and the absence of "error:".
//   - the child's git ran in the REMOTE sandbox, which has no deploy key and no
//     /tmp/.gh_token (those live on this machine). Three times the code existed
//     only as an unpushed commit there; three times the reviewer saw an empty
//     branch.
//
// So: the child writes code (in its sandbox; shadow mirrors every file write to
// the local mirror dir) plus two scripts, and STOPS — it does not push. The
// kernel then
//
//  1. computes the child's delta against the tree the mirror was primed from,
//  2. assembles a clean workspace = branch tip + delta,
//  3. runs the unit's build.sh then test.sh on this machine (real corex), the
//     exit codes being compile= / smoke=,
//  4. pushes the delta itself — to the dev branch if verification passed, to a
//     per-unit wip branch if not (the next conversation's sandbox has to be able
//     to clone the failing code to fix it).
//
// Whether the tests actually test the theory's invariants is NOT decided here:
// an exit code proves the child's tests passed. That is the reviewer's job.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---- file trees -------------------------------------------------------------------

// fileDigest maps a slash-separated relative path to a content digest.
type fileDigest map[string]string

const devMaxFileBytes = 2 << 20 // build outputs and blobs, not source

var devSkipDirs = map[string]bool{
	".git": true, "__pycache__": true, "node_modules": true, ".cache": true,
}

var devSkipExts = map[string]bool{
	".o": true, ".obj": true, ".a": true, ".so": true, ".dylib": true,
	".exe": true, ".out": true, ".pyc": true, ".ptx": true, ".cubin": true,
}

// skipTreeEntry says whether a path is kept out of the mirror/delta: git
// metadata, caches, and build outputs. Source never matches these.
func skipTreeEntry(rel string, isDir bool, size int64) bool {
	base := filepath.Base(rel)
	if isDir {
		return devSkipDirs[base]
	}
	if devSkipExts[strings.ToLower(filepath.Ext(base))] {
		return true
	}
	return size > devMaxFileBytes
}

// hasNULBytes: the file's first 8 KiB hold a NUL byte — a build output or a blob, never
// source. 11/05 08:07 a unit's build.sh left 42 LLVM bitcode files (*.bc) next to the sources
// and they were committed with the unit; an extension list cannot know every toolchain's.
func hasNULBytes(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	return bytes.IndexByte(buf[:n], 0) >= 0
}

func digestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// snapshotTree digests every kept regular file under dir.
func snapshotTree(dir string) (fileDigest, error) {
	out := fileDigest{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if d.IsDir() {
			if skipTreeEntry(rel, true, 0) {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() || skipTreeEntry(rel, false, info.Size()) || hasNULBytes(p) {
			return nil
		}
		dg, derr := digestFile(p)
		if derr != nil {
			return derr
		}
		out[rel] = dg
		return nil
	})
	return out, err
}

func copyOne(src, dst string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm()|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// copyTree copies every kept file of src into dst (existing files overwritten).
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if d.IsDir() {
			if skipTreeEntry(rel, true, 0) {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !info.Mode().IsRegular() || skipTreeEntry(rel, false, info.Size()) {
			return nil
		}
		return copyOne(p, filepath.Join(dst, rel), info.Mode())
	})
}

// wipeDirContents empties dir but keeps dir itself.
func wipeDirContents(dir string) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(dir, 0o755)
		}
		return err
	}
	for _, e := range ents {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// devMirrorDir is where shadow replays the child's file writes: the path the
// child is told its repo lives at (/home/claude/<repo>) — on THIS machine.
// devUnitMirrorDir: where a unit's conversations work on this machine — a mirror of the unit's
// own, so units that run side by side (dev_schedule.go) never write into each other's tree.
// Until 11/19 every unit of a dev repo shared /home/claude/<repo>.
func devUnitMirrorDir(devRepo, unitID string) string {
	return devMirrorDir(devRepo) + "-" + unitID
}

func devMirrorDir(devRepo string) string {
	name := devRepo
	if i := strings.LastIndex(devRepo, "/"); i >= 0 {
		name = devRepo[i+1:]
	}
	return "/home/claude/" + name
}

// primeMirror makes the mirror an exact copy of the branch tip (no .git — a
// .git there would make devRepoLocalDir mistake the mirror for the kernel's
// clone) and returns the manifest the child's delta is measured against.
// Shadow skips every git command, so without this the mirror never contains the
// units that already passed.
func primeMirror(tipDir, mirrorDir string) (fileDigest, error) {
	if err := wipeDirContents(mirrorDir); err != nil {
		return nil, fmt.Errorf("prime mirror %s: %w", mirrorDir, err)
	}
	if err := copyTree(tipDir, mirrorDir); err != nil {
		return nil, fmt.Errorf("prime mirror %s: %w", mirrorDir, err)
	}
	return snapshotTree(mirrorDir)
}

// treeDelta compares the mirror now with the manifest it was primed with.
func treeDelta(mirrorDir string, base fileDigest) (changed, removed []string, err error) {
	now, err := snapshotTree(mirrorDir)
	if err != nil {
		return nil, nil, err
	}
	for p, d := range now {
		if bd, ok := base[p]; !ok || bd != d {
			changed = append(changed, p)
		}
	}
	for p := range base {
		if _, ok := now[p]; !ok {
			removed = append(removed, p)
		}
	}
	sort.Strings(changed)
	sort.Strings(removed)
	return changed, removed, nil
}

// applyDelta puts the child's changes onto dst (a branch-tip tree).
func applyDelta(mirrorDir, dst string, changed, removed []string) error {
	for _, rel := range changed {
		src := filepath.Join(mirrorDir, filepath.FromSlash(rel))
		info, err := os.Stat(src)
		if err != nil {
			return err
		}
		if err := copyOne(src, filepath.Join(dst, filepath.FromSlash(rel)), info.Mode()); err != nil {
			return err
		}
	}
	for _, rel := range removed {
		if err := os.Remove(filepath.Join(dst, filepath.FromSlash(rel))); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// ---- running the unit's scripts on the real machine ------------------------------------

// gateLogText: the gate of a verification as its log line names it — every gate that failed,
// not the first only, which is all the conversation's text does not hide.
func gateLogText(r VerifyResult) string {
	if len(r.Gates) > 1 {
		return fmt.Sprintf("gate=%q (also failed: %s)", r.Gate, strings.Join(r.Gates[1:], ", "))
	}
	return fmt.Sprintf("gate=%q", r.Gate)
}

// VerifyResult is the kernel's own measurement of a unit.
type VerifyResult struct {
	Compile   string // pass | fail | missing
	Smoke     string // pass | fail | skipped | missing
	BuildExit int
	// BuildReused: build.sh was not run — the unit's last build read what this one would
	// (verify_build_memo.go) and its record and outputs were put back
	BuildReused bool `json:"build_reused,omitempty"`
	TestExit    int
	BuildOut    string // tail
	TestOut     string // tail
	Elapsed     time.Duration
	// Gate is the first measured requirement the unit failed (unit_gates.go), "" if none;
	// GateText says what was measured. Build/Test are what the scripts' processes did.
	Gate     string
	GateText string
	// Gates: every gate unit_gates.go measured failed, Gate first
	Gates []string
	Build ProcTrace
	Test  ProcTrace
	// NoToolchain: build.sh run again with the toolchain hidden, for GPU units
	// (build_without_toolchain.go)
	NoToolchain NoToolchainBuild
}

// Status renders the result as the status map the rest of dev_* reads.
func (v VerifyResult) Status() map[string]string {
	st := map[string]string{
		"compile":        v.Compile,
		"smoke":          v.Smoke,
		"compile_source": "kernel build.sh exit code on the real machine",
	}
	switch {
	case v.Compile == "fail":
		st["compile_error"] = fmt.Sprintf("build.sh exit=%d: %s", v.BuildExit, v.BuildOut)
	case v.Compile == "missing":
		st["compile_error"] = "no build.sh was written; the unit cannot be verified"
	case v.Smoke == "fail":
		st["smoke_error"] = fmt.Sprintf("test.sh exit=%d: %s", v.TestExit, v.TestOut)
	case v.Smoke == "missing":
		st["smoke_error"] = "no test.sh was written; the unit has no tests"
	}
	if v.Gate != "" {
		st["gate"], st["gate_error"] = v.Gate, v.GateText
	}
	// what was measured, for the reviewer and the next attempt
	if len(v.Test.Devices) > 0 {
		st["gpu_devices_opened"] = strings.Join(v.Test.Devices, " ")
	}
	if p := v.Test.LaunchSummary(); p != "" {
		st["gpu_kernels_launched"] = p
	}
	return st
}

// Passed: both stages ran and exited 0, and every measured requirement held.
func (v VerifyResult) Passed() bool { return v.Compile == "pass" && v.Smoke == "pass" && v.Gate == "" }

// devVerifyRunner runs one shell command line and returns its exit code and
// combined output. The default uses the kernel's own process facility, so the
// run is a real process on this machine (visible like any other pid). A variable
// so the logic can be tested without a booted kernel.
var devVerifyRunner = func(command, dir string, timeout time.Duration) (int, string, error) {
	pid, err := ForkExecVerify(command, dir)
	if err != nil {
		return -1, "", err
	}
	childPid, code, err := WaitTimeout(pid, timeout)
	if err != nil {
		return -1, "", err
	}
	return code, GetResult(childPid) + GetStderr(childPid), nil
}

const (
	// %[1]d lines, %[2]s them
	gateVerifyNamesMachinePackagesFmt = "verify_names_machine_packages: %[1]d line(s) of the unit's verification scripts, or of the files of its workspace they run, name this machine's Python package directories, and none of them was run here: %[2]s"
	gateVerifyModifiesMachineFmt      = "verify_modifies_machine: %[1]s and %[2]s changed %[3]d file(s) or director(ies) of this machine's Python packages, outside the workspace, and no other command ran here meanwhile: %[4]s — the unit runs on the machine as it is"
	verifyMachineBesideFmt            = "dev_verify: unit %[1]s: while build.sh and test.sh ran, %[2]d file(s) or director(ies) of this machine's Python packages changed: %[3]s — not judged: %[4]d command(s) of conversations ran here beside it"
)

// devVerifyScripts: where the unit's scripts live, relative to the repo root.
func devVerifyScripts(unitID string) (build, test string) {
	d := ".einsteinOS/verify/" + unitID
	return d + "/build.sh", d + "/test.sh"
}

func tailText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// devVerifyEnv is the shell prelude that makes the real toolchain and the
// usable devices visible to the scripts: corex lives outside the default PATH.
func devVerifyEnv(gpuUsable string) string {
	return devVerifyEnvISA(gpuUsable, nil)
}

// devVerifyEnvISA adds the reference's ISA flags for the unit (EINSTEINOS_ISA_FLAGS).
func devVerifyEnvISA(gpuUsable string, isa []string) string {
	env := fmt.Sprintf("export PATH=%s:$PATH; ", toolchainPATH())
	if len(isa) > 0 {
		env += fmt.Sprintf("export EINSTEINOS_ISA_FLAGS=%q; ", strings.Join(isa, " "))
	}
	if gpuUsable != "" {
		env += fmt.Sprintf("export EINSTEINOS_GPUS=%q CUDA_VISIBLE_DEVICES=%q; ", gpuUsable, gpuUsable)
	}
	return env
}

// runUnitVerify runs build.sh, then (only if it exited 0) test.sh, inside ws.
func runUnitVerify(ws, unitID, gpuUsable string, buildTimeout, testTimeout time.Duration) VerifyResult {
	return runUnitVerifyWith(ws, unitID, gpuUsable, UnitRequirements{}, nil, buildTimeout, testTimeout)
}

// runWatched runs a script command with its process tree watched.
func runWatched(command, dir string, timeout time.Duration) (int, string, ProcTrace, error) {
	f, err := os.CreateTemp("", "einsteinos-verify-*.pid")
	if err != nil {
		code, out, rerr := devVerifyRunner(command, dir, timeout)
		return code, out, ProcTrace{}, rerr
	}
	pidFile := f.Name()
	f.Close()
	os.Remove(pidFile)
	defer os.Remove(pidFile)
	stop := watchProcTreeIn(pidFile, dir)
	code, out, rerr := devVerifyRunner(watchedCommand(pidFile, command), dir, timeout)
	return code, out, stop(), rerr
}

// runUnitVerifyWith is runUnitVerify plus what the reference requires of the unit: both
// scripts run watched, and after they pass the gates are judged on the measurements.
func runUnitVerifyWith(ws, unitID, gpuUsable string, req UnitRequirements, changed []string, buildTimeout, testTimeout time.Duration) VerifyResult {
	return onTheRealMachine(unitID, func() VerifyResult {
		return runUnitVerifyAlone(ws, unitID, gpuUsable, req, changed, buildTimeout, testTimeout)
	})
}

// the real machine runs one verification at a time: each opens every GPU, and units that run
// side by side (dev_schedule.go) reach their verifications at any time
var (
	realMachineMu   sync.Mutex
	realMachineUnit string
	realMachineStat sync.Mutex
)

// onTheRealMachine runs f when no other unit's verification runs, and says whom it waited for.
func onTheRealMachine(unitID string, f func() VerifyResult) VerifyResult {
	realMachineStat.Lock()
	busy := realMachineUnit
	realMachineStat.Unlock()
	if busy != "" {
		log.Printf("dev_verify: unit %s waits for unit %s's verification on the real machine", unitID, busy)
	}
	realMachineMu.Lock()
	defer realMachineMu.Unlock()
	realMachineStat.Lock()
	realMachineUnit = unitID
	realMachineStat.Unlock()
	defer func() {
		realMachineStat.Lock()
		realMachineUnit = ""
		realMachineStat.Unlock()
	}()
	return f()
}

func runUnitVerifyAlone(ws, unitID, gpuUsable string, req UnitRequirements, changed []string, buildTimeout, testTimeout time.Duration) VerifyResult {
	if len(req.Tests) > 0 {
		// the integration unit's test.sh is the reference's registered tests, written here
		_, test := devVerifyScripts(unitID)
		tp := filepath.Join(ws, test)
		_ = os.MkdirAll(filepath.Dir(tp), 0o755)
		_ = os.WriteFile(tp, []byte(integrationScript(req.Repo, req.SHA, req.Tests)), 0o755)
	}
	start := time.Now()
	build, test := devVerifyScripts(unitID)
	res := VerifyResult{Compile: "missing", Smoke: "missing", BuildExit: -1, TestExit: -1}
	finish := func() VerifyResult { res.Elapsed = time.Since(start); return res }

	if _, err := os.Stat(filepath.Join(ws, filepath.FromSlash(build))); err != nil {
		return finish()
	}
	// a script that names this machine's Python package directories is not run here at all
	if named := scriptsNamingMachinePackages(ws, unitID); len(named) > 0 {
		res.Compile, res.Smoke = "skipped", "skipped"
		res.Gates, res.Gate, res.GateText = []string{"verify_names_machine_packages"}, "verify_names_machine_packages", fmt.Sprintf(gateVerifyNamesMachinePackagesFmt, len(named), shownPaths(named, 12))
		log.Printf("dev_verify: unit %s: %s", unitID, res.GateText)
		return finish()
	}
	env := devVerifyEnvISA(gpuUsable, req.ISAFlags)
	// a build of the unit's own workspace whose inputs did not change is not run again
	// (verify_build_memo.go); the build runs under the probe, which records what it read and wrote
	memoOn := !freshBuild() && filepath.Base(ws) == "einsteinos_verify_"+unitID
	key := buildMemoKey(ws, build, env)
	if memoOn {
		m, ok, changed := reuseBuildMemo(unitID, ws, key)
		if !ok && len(changed) > 0 {
			log.Printf(buildMemoMissFmt, unitID, build, strings.Join(changed, "; "))
		}
		if ok {
			log.Printf(buildMemoReusedFmt, unitID, build, len(m.Inputs), len(m.Dirs), len(m.Outputs))
			res.Build, res.BuildExit, res.BuildOut, res.NoToolchain, res.BuildReused = m.Trace, m.Exit, m.Out, m.NoTool, true
			if m.Exit != 0 {
				res.Compile, res.Smoke = "fail", "skipped"
				return finish()
			}
			if why := builtNothing(m, build); why != "" {
				res.Compile, res.Smoke, res.BuildOut = "fail", "skipped", why+"\n"+res.BuildOut
				log.Printf("dev_verify: unit %s: %s", unitID, why)
				return finish()
			}
			res.Compile = "pass"
			return runUnitTestStage(ws, unitID, gpuUsable, req, changed, buildTimeout, testTimeout, env, build, test, res, start)
		}
	}
	benv, blog := env, ""
	if memoOn {
		if lib, lerr := launchProbeLib(); lerr == nil {
			blog = filepath.Join(os.TempDir(), fmt.Sprintf("einsteinos-build-files-%d.log", time.Now().UnixNano()))
			benv += "export EINSTEINOS_PROBE_FILES=1; " + launchProbeEnv(lib, blog)
			defer os.Remove(blog)
		}
	}
	bstart := time.Now()
	code, out, btrace, err := runWatched(benv+"bash "+build+" 2>&1", ws, buildTimeout)
	log.Printf(verifyProcTimesFmt, unitID, build, time.Since(bstart).Round(time.Second), procTimesByExe(btrace.Processes), e2eProcTimes(btrace.Processes, e2eProcTimesMin))
	res.Build = btrace
	res.BuildExit, res.BuildOut = code, tailText(out, 3000)
	if err != nil {
		res.Compile, res.BuildOut = "fail", "could not run build.sh: "+err.Error()
		res.Smoke = "skipped"
		return finish()
	}
	if code != 0 {
		res.Compile, res.Smoke = "fail", "skipped"
		return finish()
	}
	res.Compile = "pass"
	if (req.GPU() || req.RequireGPU) && len(presentGPUCompilers()) > 0 {
		res.NoToolchain = buildWithoutToolchain(ws, build, buildTimeout)
		if !res.NoToolchain.Measured {
			log.Printf("dev_verify: unit %s: build.sh could not be run with the toolchain hidden here (%s) — not judged on it; the toolchain's use, the CUDA compiles and the kernel launches are measured directly",
				unitID, tailText(res.NoToolchain.Err, 300))
		}
	}

	// what the build read and wrote, kept before test.sh can change anything it wrote
	if blog != "" {
		m := memoFromBuild(ws, key, readProbeLog(blog), res.BuildExit, res.BuildOut, res.Build, res.NoToolchain)
		if why := builtNothing(m, build); why != "" {
			res.Compile, res.Smoke, res.BuildOut = "fail", "skipped", why+"\n"+res.BuildOut
			log.Printf("dev_verify: unit %s: %s", unitID, why)
			return finish()
		}
		if len(m.Inputs) > 0 {
			if serr := saveBuildMemo(unitID, ws, m); serr == nil {
				log.Printf(buildMemoSavedFmt, unitID, build, len(m.Inputs), len(m.Dirs), len(m.Outputs))
			}
		}
	}
	return runUnitTestStage(ws, unitID, gpuUsable, req, changed, buildTimeout, testTimeout, env, build, test, res, start)
}

// runUnitTestStage runs test.sh in ws after a build that passed (res holds its record).
func runUnitTestStage(ws, unitID, gpuUsable string, req UnitRequirements, changed []string, buildTimeout, testTimeout time.Duration, env, build, test string, res VerifyResult, start time.Time) VerifyResult {
	finish := func() VerifyResult { res.Elapsed = time.Since(start); return res }
	if _, err := os.Stat(filepath.Join(ws, filepath.FromSlash(test))); err != nil {
		return finish() // Smoke stays "missing"
	}
	// a GPU unit's test runs with the launch probe preloaded: which kernels ran is measured
	testEnv, probeLog, probeErr := env, "", ""
	if req.GPU() || req.RequireGPU {
		if lib, err := launchProbeLib(); err != nil {
			probeErr = err.Error()
		} else if f, err := os.CreateTemp("", "einsteinos-launch-*.log"); err != nil {
			probeErr = err.Error()
		} else {
			probeLog = f.Name()
			f.Close()
			defer os.Remove(probeLog)
			testEnv += launchProbeEnv(lib, probeLog)
		}
	}
	tstart := time.Now()
	code, out, ttrace, err := runWatched(testEnv+"bash "+test+" 2>&1", ws, testTimeout)
	log.Printf(verifyProcTimesFmt, unitID, test, time.Since(tstart).Round(time.Second), procTimesByExe(ttrace.Processes), e2eProcTimes(ttrace.Processes, e2eProcTimesMin))
	ttrace.ProbeErr = probeErr
	if probeLog != "" {
		pl := readProbeLog(probeLog)
		ttrace.Launches = resolveLaunches(pl)
		ttrace.ProbeLoaded = probeLoadedExes(pl)
		ttrace.Devices = probeGPUDevices(ttrace.Devices, pl)
	}
	res.Test = ttrace
	res.TestExit, res.TestOut = code, tailText(out, 3000)
	// what build.sh and test.sh changed of this machine's Python packages: the unit's when no
	// command but the verification's ran here meanwhile (fork_runs.go); when conversations'
	// commands ran beside it, a change cannot be laid on the unit and is logged
	machineGate, machineText := "", ""
	if mc := machineChangedSince(start); len(mc) > 0 {
		if beside := forkRunsBeside(start.Add(-machineClockSlack), time.Now()); len(beside) > 0 {
			log.Printf(verifyMachineBesideFmt, unitID, len(mc), shownPaths(mc, 12), len(beside))
		} else {
			machineGate, machineText = "verify_modifies_machine", fmt.Sprintf(gateVerifyModifiesMachineFmt, build, test, len(mc), shownPaths(mc, 12))
		}
	}
	switch {
	case err != nil:
		res.Smoke, res.TestOut = "fail", "could not run test.sh: "+err.Error()
	case code != 0:
		res.Smoke = "fail"
	default:
		res.Smoke = "pass"
		if machineGate != "" {
			res.Gates, res.Gate, res.GateText = []string{machineGate}, machineGate, machineText
			break
		}
		gi := unitGateInput(ws, unitID, req, changed, res.Build, res.Test, res.NoToolchain)
		if kinds, texts := unitGatesAll(gi); len(kinds) > 0 {
			res.Gates, res.Gate, res.GateText = kinds, kinds[0], strings.Join(texts, "\n")
		}
		// its Lean modules must check in Lean (lean_gate.go)
		if res.Gate == "" {
			res.Gate, res.GateText = leanGate(ws, changed)
		}
		// the kernels it launched must be checked by its test: rebuilt with each one's body
		// skipped, the test has to fail (gpu_mutation.go)
		if res.Gate == "" && req.GPU() && len(req.Tests) == 0 && probeMeasured(res.Test) {
			own := kernelSet(gi.Kernels)
			if len(gi.PortKernels) > 0 {
				own = gi.PortKernels
			}
			files := kernelFiles(ws, unitID, own, gi.Kernels, launchedNames(res.Test.Launches))
			unchecked, inconclusive := resultUnchecked(ws, build, test, env, files, buildTimeout, testTimeout)
			if len(inconclusive) > 0 {
				log.Printf("dev_verify: unit %s: whether its test checks %s could not be measured", unitID, strings.Join(inconclusive, ", "))
			}
			if len(unchecked) > 0 {
				res.Gate, res.GateText = "gpu_result_unchecked", fmt.Sprintf(gateResultUncheckedFmt, test, strings.Join(unchecked, " "))
			}
		}
		// the integration unit: the port against the reference and the integration before,
		// on this machine (eval_integration.go)
		if fn := integrationEval(unitID); fn != nil && res.Gate == "" {
			// the verification's own line comes when the evaluation returns, which can be
			// hours later: what was measured until here is logged now
			log.Printf("dev_verify: unit %s: compile=%s smoke=%s, every gate passed — the integration's evaluation starts", unitID, res.Compile, res.Smoke)
			res.Gate, res.GateText = fn(ws, req.Benches)
		}
	}
	if machineGate != "" && res.Gate != machineGate {
		log.Printf("dev_verify: unit %s: %s", unitID, machineText)
	}
	return finish()
}

// unitGateInput: what the gates of a unit's verification are given, measured in the
// workspace ws. It is one function that the verification and its tests call: 11/09 the
// port map's kernels (PortKernels, 58a9d1ed and 3086282d) were tested on hand-built inputs
// while the verification never set them, and 11/10 02:07 unit u61716a9a8a was sent back
// gpu_kernel_missing by code whose fix for it had never run.
// portFiles: the files a port map writes counterparts into.
func portFiles(pm map[string]portEntry) map[string]bool {
	out := map[string]bool{}
	for _, e := range pm {
		out[e.file] = true
	}
	return out
}

// changedFilesText names the files of an attempt that differ from the branch, the first four:
// 11/16 12:18 the log said '1 file(s) changed' and which one — the Lean file, not the test.sh
// whose edit had failed here — could only be read off the gate that followed.
func changedFilesText(changed []string) string {
	if len(changed) == 0 {
		return ""
	}
	return " (" + shortList(changed, 4) + ")"
}

func unitGateInput(ws, unitID string, req UnitRequirements, changed []string, build, test ProcTrace, nt NoToolchainBuild) GateInput {
	pm := readPortMap(ws, unitID)
	pf := portFiles(pm)
	var pfRefused []string
	if len(req.Tests) > 0 {
		// the integration unit's kernels: their rows are checked against the planner's missing
		// lines and the kernels the units ported (port_gate.go)
		pf, pfRefused = integrationPortFiles(ws, unitID, pm)
	}
	mutScript, mutMissing := testMutatesMissing(ws, unitID, changed)
	return GateInput{Req: req, Changed: changed, Build: build, Test: test, Compilers: presentGPUCompilers(),
		PortMissing: portMissing(ws, unitID, req.Symbols), PortExtra: portExtra(ws, unitID, req.Symbols), PortRefFiles: portRefFiles(ws, unitID, req.Symbols), RefPathsWritten: refPathsWritten(withRefPathsPresent(ws, changed, req), req),
		PortForeign: portForeignNames(ws, unitID, req.FileNames), PortScripts: unitPortScripts(ws, unitID, req), MutatesMissingScript: mutScript, MutatesMissing: mutMissing, Kernels: unitKernels(ws, changed), KernelsMeasured: true,
		Shadowed: shadowedKernels(unitKernels(ws, changed), portKernels(ws, unitID)),
		ISAFuncs: isaPortFuncs(pm, req.FlagFiles), PortKernels: portKernels(ws, unitID), UnitID: unitID,
		PortFiles: pf, PortFilesRefused: pfRefused,
		NoToolchain: nt, Includers: cuhIncluders(ws, changed), IncludersMeasured: true,
		RepoKernels: integrationRepoKernels(ws, req), LeanCopied: unitLeanCopied(ws, changed)}
}

// ---- assembling and committing ------------------------------------------------------------

// assembleWorkspace builds a clean directory = branch tip + the child's delta.
// A fresh directory (not the mirror) so a stale object file from an earlier
// attempt cannot make a build pass that would not pass from the branch.
func assembleWorkspace(tipDir, mirrorDir string, changed, removed []string) (string, error) {
	ws, err := os.MkdirTemp("", "einsteinos_verify_")
	if err != nil {
		return "", err
	}
	if err := copyTree(tipDir, ws); err != nil {
		os.RemoveAll(ws)
		return "", err
	}
	if err := applyDelta(mirrorDir, ws, changed, removed); err != nil {
		os.RemoveAll(ws)
		return "", err
	}
	return ws, nil
}

// devGitPush is a variable so commit/push flow can be tested against a local
// bare repository.
var devGitPush = GitPushRetry

// devGitDeleteBranch deletes a branch on the dev repo's remote (a variable for tests).
var devGitDeleteBranch = gitDeleteRemoteBranch

// commitDelta checks out `onto` in the kernel clone (the remote tip when the
// remote has it, else forked from `from`), applies the delta, commits and pushes
// it. Returns whether anything was committed.
func commitDelta(cloneDir, devRepo, from, onto, mirrorDir string, changed, removed []string, msg string) error {
	target := onto
	if from == onto {
		if err := gitUseBranchFromRemote(cloneDir, onto, from); err != nil {
			return fmt.Errorf("checkout %s: %w", onto, err)
		}
	} else {
		// the delta was measured against from: laid over what onto held before (an earlier
		// park of the same attempt), a file that delta changed and this one set back would
		// keep the earlier text — onto is from with the delta, and replaces what it held
		if err := gitUseBranchFromRemote(cloneDir, from, from); err != nil {
			return fmt.Errorf("checkout %s: %w", from, err)
		}
		if err := runLocalGit(fmt.Sprintf("cd %q && git checkout -q -B %s", cloneDir, onto), cloneDir); err != nil {
			return fmt.Errorf("checkout %s from %s: %w", onto, from, err)
		}
		target = "+HEAD:refs/heads/" + onto
	}
	if err := applyDelta(mirrorDir, cloneDir, changed, removed); err != nil {
		return fmt.Errorf("apply delta: %w", err)
	}
	GitCommit(cloneDir, msg)
	if err := devGitPush(cloneDir, devRepo, target, 20); err != nil {
		return fmt.Errorf("push %s: %w", onto, err)
	}
	noteKernelPush(devRepo, onto, gitLine(cloneDir, "rev-parse", "HEAD"))
	return nil
}

// promoteBranch brings the verified code on `from` (a unit's wip branch) into
// `to` (the dev branch) and pushes it. A merge, not a fast-forward: while the
// unit was failing and being fixed, the kernel kept committing to the dev branch
// (plan.json after every attempt), so the wip branch no longer descends from the
// dev tip. The two sides touch different files, so the merge is clean; if it is
// not, the error is returned and nothing is pushed.
func promoteBranch(cloneDir, devRepo, from, to string) error {
	if err := gitUseBranchFromRemote(cloneDir, to, devNewBranchBase(devRepo)); err != nil {
		return err
	}
	if err := GitMerge(cloneDir, from, fmt.Sprintf("merge verified %s into %s", from, to)); err != nil {
		return err
	}
	return devGitPush(cloneDir, devRepo, to, 20)
}

// devWipBranch is the per-unit branch failing code is parked on, so the next
// conversation's sandbox can clone it. Not nested under the dev branch name:
// git cannot hold refs/heads/a and refs/heads/a/b at once.
func devWipBranch(branch, unitID string) string { return branch + "-wip-" + unitID }

// devContBranch: where a fix attempt from the wip branch parks a tree it rejected (parkTarget).
func devContBranch(branch, unitID string) string { return devWipBranch(branch, unitID) + "-cont" }

// ---- the whole step ---------------------------------------------------------------------------

// UnitVerifyOutcome is what dev_from_theorems needs back.
type UnitVerifyOutcome struct {
	Result  VerifyResult
	Status  map[string]string
	Pushed  string   // branch the code ended up on ("" if nothing was written)
	Changed []string // files the child added/changed
	Removed []string
	Err     error
	// Verified: the real-machine verification ran and Result holds its verdict. An Err
	// after that is a push failure: only the push is retried (devRetryPush), the verdict
	// stands. 11/04 09:42-09:49 unit u0e60a9f272 passed and was verified three times
	// (3 × 1m22s) because GitHub refused the pushes.
	Verified bool
	msg      string
}

// devVerifyAndPush runs steps 1-4 for one unit after its conversation ended.
//
//	cloneDir  the kernel clone of the dev repo
//	mirror    the dir shadow replays the child's file writes into (devMirrorDir)
//	baseRef   the branch whose tip the mirror was primed from (dev branch, or the
//	          unit's wip branch when this is a fix attempt)
func devVerifyAndPush(cloneDir, mirror, devRepo, branch, baseRef, unitID, unitDesc, gpuUsable string, base fileDigest, req UnitRequirements) UnitVerifyOutcome {
	out := devVerifyOnly(cloneDir, mirror, branch, baseRef, unitID, gpuUsable, base, req)
	if !out.Verified || out.Err != nil || out.Status["existing_code"] == "true" {
		return out
	}
	out.msg = fmt.Sprintf("unit %s: %s [compile=%s smoke=%s gate=%s kernels=%s, verified by the kernel]", unitID, truncStr(unitDesc, 120), out.Result.Compile, out.Result.Smoke, gateOrNone(out.Result.Gate), out.Result.Test.LaunchCount())
	out.Pushed, out.Err = devPushVerdict(cloneDir, mirror, devRepo, branch, baseRef, unitID, out.Changed, out.Removed, out.msg, out.Result.Passed())
	return out
}

// devVerifyOnly runs the kernel's verification of the mirror's delta on the real machine —
// build, test, gates, /proc measurement — without pushing anything. It is what decides a
// unit, and what a conversation is told before it is allowed to end (dev_preverify.go).
func devVerifyOnly(cloneDir, mirror, branch, baseRef, unitID, gpuUsable string, base fileDigest, req UnitRequirements) UnitVerifyOutcome {
	out := UnitVerifyOutcome{}

	changed, removed, err := treeDelta(mirror, base)
	if err != nil {
		out.Err = fmt.Errorf("delta: %w", err)
		return out
	}
	out.Changed, out.Removed = changed, removed
	if len(changed) == 0 && len(removed) == 0 {
		if prior := devUnitPriorFiles(cloneDir, baseRef, unitID); len(prior) > 0 {
			return devVerifyExisting(cloneDir, mirror, branch, baseRef, unitID, gpuUsable, req, prior)
		}
		out.Result = VerifyResult{Compile: "missing", Smoke: "missing", BuildExit: -1, TestExit: -1}
		out.Status = out.Result.Status()
		out.Status["compile_error"] = "the conversation wrote no file (the mirror is identical to the branch tip)"
		return out
	}

	// the tip the delta applies to
	if err := gitUseBranchFromRemote(cloneDir, baseRef, branch); err != nil {
		out.Err = fmt.Errorf("checkout base %s: %w", baseRef, err)
		return out
	}
	// a file of the delta another unit changed on the branch since this unit's mirror was primed
	// is not written over (units side by side, dev_schedule.go): the unit runs again, alone
	if moved := branchMovedUnder(cloneDir, mirror, base, changed, removed); len(moved) > 0 {
		out.Err = fmt.Errorf("%w: %s", errBranchMoved, strings.Join(moved, " "))
		log.Printf("dev_verify: unit %s: %d file(s) of its delta changed on %s since its mirror was primed (%s) — not verified over them", unitID, len(moved), baseRef, strings.Join(moved, " "))
		return out
	}
	ws, seeded, done, err := assembleWorkspaceKept(unitID, cloneDir, mirror, changed, removed)
	if err != nil {
		out.Err = fmt.Errorf("assemble workspace: %w", err)
		return out
	}
	files := unitFiles(changed, removed, devUnitPriorFiles(cloneDir, baseRef, unitID))
	log.Printf("dev_verify: unit %s: %d file(s) changed%s, %d removed; verifying %s on the real machine%s",
		unitID, len(changed), changedFilesText(changed), len(removed), ws, map[bool]string{true: " (on the build directory of its last verification)", false: ""}[seeded])
	// the gates judge the unit's code, not this attempt's edit of it (unitFiles)
	out.Result = runUnitVerifyWith(ws, unitID, gpuUsable, req, files, 60*time.Minute, 60*time.Minute)
	done()
	if (seeded || out.Result.BuildReused) && out.Result.Passed() {
		// a pass on the last build's directory, or on its reused build, counts once it passes from nothing
		log.Printf("dev_verify: unit %s passed on the build of its last verification — verified again from nothing before it counts", unitID)
		fresh, ferr := assembleWorkspace(cloneDir, mirror, changed, removed)
		if ferr != nil {
			out.Err = fmt.Errorf("assemble workspace: %w", ferr)
			return out
		}
		defer os.RemoveAll(fresh)
		out.Result = runUnitVerifyWith(fresh, unitID, gpuUsable, req, files, 60*time.Minute, 60*time.Minute)
	}
	out.Status = out.Result.Status()
	log.Printf("dev_verify: unit %s: compile=%s (exit %d) smoke=%s (exit %d) in %s; %s; devices opened %v; kernels launched: %s",
		unitID, out.Result.Compile, out.Result.BuildExit, out.Result.Smoke, out.Result.TestExit, out.Result.Elapsed.Round(time.Second),
		gateLogText(out.Result), out.Result.Test.Devices, out.Result.Test.LaunchSummaryOr("not measured"))

	out.Verified = true
	return out
}

// unitFiles: what the gates judge — the files this attempt changed and the files the unit's
// earlier verified commits put on the base, less what this attempt removed. 11/06 19:24 a fix
// attempt on the wip branch changed only build.sh and test.sh, and gpu_code_missing said the
// unit had written no .cu — its 8 files, kernels included, were on the base; the conversation
// then wrote an "oracle" kernel to have a .cu of its own.
func unitFiles(changed, removed, prior []string) []string {
	gone := map[string]bool{}
	for _, f := range removed {
		gone[f] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range append(append([]string{}, changed...), prior...) {
		if !seen[f] && !gone[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// devUnitPriorFiles: the files the kernel's earlier verified commits for this unit
// ("unit <id>: …") put on baseRef, when they are still there.
func devUnitPriorFiles(cloneDir, baseRef, unitID string) []string {
	out, err := exec.Command("git", "-C", cloneDir, "log", "--format=", "--name-only",
		"--grep=^unit "+unitID+":", "origin/"+baseRef).Output()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var files []string
	for _, f := range strings.Split(string(out), "\n") {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		if exec.Command("git", "-C", cloneDir, "cat-file", "-e", "origin/"+baseRef+":"+f).Run() == nil {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files
}

// devVerifyExisting verifies the code already on the branch for a unit whose conversation
// wrote nothing, with the same scripts, gates and /proc measurement as new code. 11/04
// 09:28 unit u6d34237bff's conversation found the unit already implemented by an earlier
// run, changed nothing, said so — and was failed as nothing_written without the kernel
// looking. Passing here sends the unit to review; nothing is pushed (nothing changed).
func devVerifyExisting(cloneDir, mirror, branch, baseRef, unitID, gpuUsable string, req UnitRequirements, prior []string) UnitVerifyOutcome {
	out := UnitVerifyOutcome{Verified: true}
	if err := gitUseBranchFromRemote(cloneDir, baseRef, branch); err != nil {
		out.Verified = false
		out.Err = fmt.Errorf("checkout base %s: %w", baseRef, err)
		return out
	}
	ws, err := assembleWorkspace(cloneDir, mirror, nil, nil)
	if err != nil {
		out.Verified = false
		out.Err = fmt.Errorf("assemble workspace: %w", err)
		return out
	}
	defer os.RemoveAll(ws)
	log.Printf("dev_verify: unit %s wrote nothing; verifying the branch's existing code for it (%d file(s) from its earlier verified commits) on the real machine",
		unitID, len(prior))
	out.Result = runUnitVerifyWith(ws, unitID, gpuUsable, req, prior, 60*time.Minute, 60*time.Minute)
	out.Status = out.Result.Status()
	out.Status["existing_code"] = "true"
	log.Printf("dev_verify: unit %s (existing code): compile=%s (exit %d) smoke=%s (exit %d) in %s; %s; devices opened %v; kernels launched: %s",
		unitID, out.Result.Compile, out.Result.BuildExit, out.Result.Smoke, out.Result.TestExit, out.Result.Elapsed.Round(time.Second),
		gateLogText(out.Result), out.Result.Test.Devices, out.Result.Test.LaunchSummaryOr("not measured"))
	if out.Result.Passed() {
		out.Pushed = baseRef
	}
	return out
}

// devRetryPush pushes an outcome whose verification already ran, without verifying again.
func devRetryPush(cloneDir, mirror, devRepo, branch, baseRef, unitID string, out UnitVerifyOutcome) UnitVerifyOutcome {
	out.Pushed, out.Err = devPushVerdict(cloneDir, mirror, devRepo, branch, baseRef, unitID, out.Changed, out.Removed, out.msg, out.Result.Passed())
	return out
}

// devPushVerdict puts verified code on the dev branch and failing code on the unit's wip
// branch. It returns the branch the code ended up on.
func devPushVerdict(cloneDir, mirror, devRepo, branch, baseRef, unitID string, changed, removed []string, msg string, passed bool) (string, error) {
	wip := devWipBranch(branch, unitID)
	if passed {
		// onto the base ref first (it may be the wip branch of a fix attempt), then the dev branch
		if baseRef != branch {
			if err := commitDelta(cloneDir, devRepo, baseRef, baseRef, mirror, changed, removed, msg); err != nil {
				return "", err
			}
			if err := promoteBranch(cloneDir, devRepo, baseRef, branch); err != nil {
				return "", err
			}
			// the wip branch is merged into the dev branch: it has nothing left to hold.
			// 11/03 left einsteinOS/dev-…-wip-u89be81de0b and -wip-ueac0e4654d behind.
			if baseRef == devWipBranch(branch, unitID) || baseRef == devContBranch(branch, unitID) {
				if err := devGitDeleteBranch(cloneDir, devRepo, baseRef); err != nil {
					log.Printf("dev_verify: unit %s: %s merged but not deleted: %v", unitID, baseRef, err)
				} else {
					log.Printf("dev_verify: unit %s: %s merged into %s and deleted", unitID, baseRef, branch)
				}
			}
		} else if err := commitDelta(cloneDir, devRepo, branch, branch, mirror, changed, removed, msg); err != nil {
			return "", err
		}
		return branch, nil
	}
	// not verified: park it where the next sandbox can read it. NEVER on the dev branch.
	if err := commitDelta(cloneDir, devRepo, baseRef, wip, mirror, changed, removed, msg); err != nil {
		return "", err
	}
	return wip, nil
}

func gateOrNone(g string) string {
	if g == "" {
		return "ok"
	}
	return g
}

// integrationRepoKernels: the working tree's kernels, measured for the integration unit only.
func integrationRepoKernels(ws string, req UnitRequirements) map[string]bool {
	if len(req.Tests) == 0 {
		return nil
	}
	return repoKernels(ws)
}

// errBranchMoved: the delta touches files the branch changed since the unit's mirror was primed.
var errBranchMoved = errors.New("the branch changed files of this delta since the unit started")

// branchMovedUnder: the files of a delta (changed or removed) that the tip in tipDir no longer
// holds as the mirror's base had them — another unit's commit changed them meanwhile — and
// does not hold as the mirror does either. A delta copied over them would undo that commit. A
// tip that holds what the mirror holds lost nothing to the delta: 11/23 08:44:05 the
// integration's conversations had pushed the files of their own mirror to the wip branch from
// their sandboxes (96c7a4c…b6a5e3f), and the verification was refused over seven files whose
// tip was the mirror's.
func branchMovedUnder(tipDir, mirrorDir string, base fileDigest, changed, removed []string) []string {
	var out []string
	for _, f := range append(append([]string{}, changed...), removed...) {
		was, had := base[f]
		now, err := digestFile(filepath.Join(tipDir, f))
		has := err == nil
		if !(had != has || (had && was != now)) {
			continue
		}
		mine, merr := digestFile(filepath.Join(mirrorDir, f))
		if (merr == nil) == has && (!has || mine == now) {
			continue // the tip is the mirror's
		}
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// %[1]s unit, %[2]s the script, %[3]s how long it ran, %[4]s by executable, %[5]s its longest
// processes (e2eProcTimes). 11/22
// a verification of the integration unit took 6m26s from its start to the end of build.sh,
// with the build directory of the last one put back or not, and how much of it was build.sh,
// which compile, or test.sh was not in the log.
const verifyProcTimesFmt = "dev_verify: unit %[1]s: %[2]s ran %[3]s; by executable: %[4]s; its processes seen running at least 1s, longest first: %[5]s"

const (
	// %[1]s unit, %[2]s build.sh, %[3]d files read, %[4]d directories listed, %[5]d files written
	buildMemoReusedFmt = "dev_verify: unit %[1]s: %[2]s not run — the %[3]d workspace file(s) its last run read and the %[4]d director(ies) it listed are what they were; the %[5]d file(s) it wrote are put back"
	// %[1]s unit, %[2]s build.sh, %[3]s what changed
	buildMemoMissFmt = "dev_verify: unit %[1]s: %[2]s runs — since its last run %[3]s"
	// %[1]s build.sh
	buildBuiltNothingFmt = "compile: %[1]s read no file of the workspace but itself and wrote none — it built nothing, and what test.sh would run is what an earlier verification left in the build directory"
	buildMemoSavedFmt    = "dev_verify: unit %[1]s: %[2]s read %[3]d workspace file(s), listed %[4]d director(ies) and wrote %[5]d file(s) — kept, for a verification whose inputs are the same"
)

// builtNothing: why a build that read no workspace file but its own script and wrote no file is
// not a compile ("" when it built something). 11/23 20:43:10 and 11/24 00:56:46 the integration's
// build.sh was 0 bytes — a command had written an empty file over it — and compile=pass went
// with a test.sh that ran the 29 executables the 20:25 verification had left in the build
// directory.
func builtNothing(m buildMemo, build string) string {
	// a probe that recorded nothing (no read of the script itself) measured nothing
	if len(m.Outputs) > 0 || len(m.Inputs) == 0 {
		return ""
	}
	for f := range m.Inputs {
		if filepath.ToSlash(f) != filepath.ToSlash(build) {
			return ""
		}
	}
	return fmt.Sprintf(buildBuiltNothingFmt, build)
}
