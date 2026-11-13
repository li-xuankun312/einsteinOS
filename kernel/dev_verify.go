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
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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

// VerifyResult is the kernel's own measurement of a unit.
type VerifyResult struct {
	Compile   string // pass | fail | missing
	Smoke     string // pass | fail | skipped | missing
	BuildExit int
	TestExit  int
	BuildOut  string // tail
	TestOut   string // tail
	Elapsed   time.Duration
	// Gate is the first measured requirement the unit failed (unit_gates.go), "" if none;
	// GateText says what was measured. Build/Test are what the scripts' processes did.
	Gate     string
	GateText string
	Build    ProcTrace
	Test     ProcTrace
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
	pid, err := ForkExec(command, dir)
	if err != nil {
		return -1, "", err
	}
	childPid, code, err := WaitTimeout(pid, timeout)
	if err != nil {
		return -1, "", err
	}
	return code, GetResult(childPid) + GetStderr(childPid), nil
}

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
	stop := watchProcTree(pidFile)
	code, out, rerr := devVerifyRunner(watchedCommand(pidFile, command), dir, timeout)
	return code, out, stop(), rerr
}

// runUnitVerifyWith is runUnitVerify plus what the reference requires of the unit: both
// scripts run watched, and after they pass the gates are judged on the measurements.
func runUnitVerifyWith(ws, unitID, gpuUsable string, req UnitRequirements, changed []string, buildTimeout, testTimeout time.Duration) VerifyResult {
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
	env := devVerifyEnvISA(gpuUsable, req.ISAFlags)
	code, out, btrace, err := runWatched(env+"bash "+build+" 2>&1", ws, buildTimeout)
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
	code, out, ttrace, err := runWatched(testEnv+"bash "+test+" 2>&1", ws, testTimeout)
	ttrace.ProbeErr = probeErr
	if probeLog != "" {
		pl := readProbeLog(probeLog)
		ttrace.Launches = resolveLaunches(pl)
		ttrace.ProbeLoaded = probeLoadedExes(pl)
		ttrace.Devices = probeGPUDevices(ttrace.Devices, pl)
	}
	res.Test = ttrace
	res.TestExit, res.TestOut = code, tailText(out, 3000)
	switch {
	case err != nil:
		res.Smoke, res.TestOut = "fail", "could not run test.sh: "+err.Error()
	case code != 0:
		res.Smoke = "fail"
	default:
		res.Smoke = "pass"
		res.Gate, res.GateText = unitGates(unitGateInput(ws, unitID, req, changed, res.Build, res.Test, res.NoToolchain))
		// the integration unit: the port against the reference and the integration before,
		// on this machine (eval_integration.go)
		if fn := integrationEval(unitID); fn != nil && res.Gate == "" {
			res.Gate, res.GateText = fn(ws, req.Benches)
		}
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

func unitGateInput(ws, unitID string, req UnitRequirements, changed []string, build, test ProcTrace, nt NoToolchainBuild) GateInput {
	pm := readPortMap(ws, unitID)
	return GateInput{Req: req, Changed: changed, Build: build, Test: test, Compilers: presentGPUCompilers(),
		PortMissing: portMissing(ws, unitID, req.Symbols), PortExtra: portExtra(ws, unitID, req.Symbols), Kernels: unitKernels(ws, changed), KernelsMeasured: true,
		ISAFuncs: isaPortFuncs(pm, req.FlagFiles), PortKernels: portKernels(ws, unitID), UnitID: unitID,
		PortFiles:   portFiles(pm),
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
	if err := gitUseBranchFromRemote(cloneDir, onto, from); err != nil {
		return fmt.Errorf("checkout %s: %w", onto, err)
	}
	if err := applyDelta(mirrorDir, cloneDir, changed, removed); err != nil {
		return fmt.Errorf("apply delta: %w", err)
	}
	GitCommit(cloneDir, msg)
	if err := devGitPush(cloneDir, devRepo, onto, 20); err != nil {
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
	ws, err := assembleWorkspace(cloneDir, mirror, changed, removed)
	if err != nil {
		out.Err = fmt.Errorf("assemble workspace: %w", err)
		return out
	}
	defer os.RemoveAll(ws)

	log.Printf("dev_verify: unit %s: %d file(s) changed, %d removed; verifying %s on the real machine",
		unitID, len(changed), len(removed), ws)
	// the gates judge the unit's code, not this attempt's edit of it (unitFiles)
	out.Result = runUnitVerifyWith(ws, unitID, gpuUsable, req, unitFiles(changed, removed, devUnitPriorFiles(cloneDir, baseRef, unitID)), 60*time.Minute, 60*time.Minute)
	out.Status = out.Result.Status()
	log.Printf("dev_verify: unit %s: compile=%s (exit %d) smoke=%s (exit %d) in %s; gate=%q; devices opened %v; kernels launched: %s",
		unitID, out.Result.Compile, out.Result.BuildExit, out.Result.Smoke, out.Result.TestExit, out.Result.Elapsed.Round(time.Second),
		out.Result.Gate, out.Result.Test.Devices, out.Result.Test.LaunchSummaryOr("not measured"))

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
	log.Printf("dev_verify: unit %s (existing code): compile=%s (exit %d) smoke=%s (exit %d) in %s; gate=%q; devices opened %v; kernels launched: %s",
		unitID, out.Result.Compile, out.Result.BuildExit, out.Result.Smoke, out.Result.TestExit, out.Result.Elapsed.Round(time.Second),
		out.Result.Gate, out.Result.Test.Devices, out.Result.Test.LaunchSummaryOr("not measured"))
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
			if baseRef == devWipBranch(branch, unitID) {
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
