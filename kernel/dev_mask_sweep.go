package kernel

// dev_mask_sweep.go — the dev branch itself does not name the reference.
//
// 11/05 03:17:40 dev_unit_14 cloned Niko1221/Strata into its sandbox: the prompt was masked,
// but the dev branch it clones held the name — in the repository's own documents
// (STRATA_FEATURE_TABLE.md, INFCCL_FEATURE_TABLE_v7–v9.md, STRATA_COMMITS_FULL.txt), in
// comments of src/ix_*.cu, in files earlier units wrote (include/baloon/strata_pack_builder.hpp),
// and in 17 stored conversation records. Before the units run, every tracked text file of the
// dev branch goes through the same mask as a prompt, and a path that holds the name is renamed
// the same way, so references between files stay consistent. Only the dev branch is changed.

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const devMaskSweepMsgFmt = "mask: the reference's name replaced in %[1]d file(s), %[2]d path(s) renamed — dev conversations do not know the reference"

// devMaskTree applies the mask to every tracked text file under dir (git ls-files) and renames
// paths that hold the name. It returns how many files changed and how many were renamed.
func devMaskTree(dir string, m *refMasker) (changed, renamed int, err error) {
	out, err := exec.Command("git", "-C", dir, "ls-files", "-z").Output()
	if err != nil {
		return 0, 0, err
	}
	for _, f := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if f == "" || devMaskKeep(f) {
			continue
		}
		p := filepath.Join(dir, f)
		b, err := os.ReadFile(p)
		if err != nil || bytes.IndexByte(b, 0) >= 0 { // unreadable or binary
			continue
		}
		if masked := m.Replace(string(b)); masked != string(b) {
			if err := os.WriteFile(p, []byte(masked), 0o644); err != nil {
				return changed, renamed, err
			}
			changed++
		}
		if np := m.Replace(f); np != f {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, np)), 0o755); err != nil {
				return changed, renamed, err
			}
			if o, err := exec.Command("git", "-C", dir, "mv", "-k", f, np).CombinedOutput(); err != nil {
				return changed, renamed, fmt.Errorf("git mv %s: %v: %s", f, err, o)
			}
			renamed++
		}
	}
	return changed, renamed, nil
}

// devMaskSweep masks the dev branch once per run and pushes the result when anything changed.
func devMaskSweep(repo, devRepo, branch string) {
	cloneDir, err := devRepoLocalDir(devRepo)
	if err != nil {
		log.Printf("dev_mask_sweep: %s no local clone: %v", devRepo, err)
		return
	}
	if err := gitUseBranchFromRemote(cloneDir, branch, branch); err != nil {
		log.Printf("dev_mask_sweep: %s checkout %s: %v", devRepo, branch, err)
		return
	}
	changed, renamed, err := devMaskTree(cloneDir, newRefMask(refMaskIdent, repo))
	if err != nil {
		log.Printf("dev_mask_sweep: %s: %v", devRepo, err)
		return
	}
	// build outputs earlier units committed: binary files the dev branch has and main does not
	removed := devRemoveUnitBinaries(cloneDir, "origin/"+devDefaultBase)
	if removed > 0 {
		log.Printf("dev_mask_sweep: %d binary build output(s) committed by earlier units removed from %s", removed, branch)
	}
	changed += removed
	if n := devRemoveConvRecords(cloneDir); n > 0 {
		log.Printf("dev_mask_sweep: %d conversation record(s) under %s removed from %s", n, devConvRecordsDir, branch)
		changed += n
	}
	if changed+renamed == 0 {
		log.Printf("dev_mask_sweep: %s %s holds no reference name", devRepo, branch)
		return
	}
	exec.Command("git", "-C", cloneDir, "add", "-A").Run()
	GitCommit(cloneDir, fmt.Sprintf(devMaskSweepMsgFmt, changed, renamed))
	if err := devGitPush(cloneDir, devRepo, branch, 20); err != nil {
		log.Printf("dev_mask_sweep: %s push %s: %v", devRepo, branch, err)
		return
	}
	log.Printf("dev_mask_sweep: "+devMaskSweepMsgFmt, changed, renamed)
}

// devMaskKeep: the kernel's own state on the branch, which names the reference by design
// (plan.json's "ref" is the plan's identity — masking it would make every restart re-plan).
// Dev conversations do not need it to work, and it is not what they read.
func devMaskKeep(f string) bool {
	return strings.HasPrefix(f, ".einsteinOS/") && strings.HasSuffix(f, ".json")
}

// devRemoveUnitBinaries removes tracked binary files that base does not have (build outputs a
// unit committed — 11/05 08:07: 42 *.bc files with u12d8dd5102). The repository's own
// binaries, on base, are kept.
func devRemoveUnitBinaries(dir, base string) int {
	out, err := exec.Command("git", "-C", dir, "ls-files", "-z").Output()
	if err != nil {
		return 0
	}
	n := 0
	for _, f := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if f == "" || devMaskKeep(f) || !hasNULBytes(filepath.Join(dir, f)) {
			continue
		}
		if exec.Command("git", "-C", dir, "cat-file", "-e", base+":"+f).Run() == nil {
			continue // the repository's own
		}
		if exec.Command("git", "-C", dir, "rm", "-q", "--cached", "--", f).Run() == nil {
			os.Remove(filepath.Join(dir, f))
			n++
		}
	}
	return n
}

// devConvRecordsDir: where the kernel used to commit every unit's conversation record. It is
// no longer written (the conversation stays on the server, under the id the plan keeps), and
// what earlier units committed is removed: 11/07 54 files, 4.3 MiB on the dev branch, each
// mostly the heredocs of code that is on the branch anyway, read only as "head -60".
const devConvRecordsDir = ".einsteinOS/convs"

// devRemoveConvRecords removes the tracked files under devConvRecordsDir and returns how many.
func devRemoveConvRecords(dir string) int {
	out, err := exec.Command("git", "-C", dir, "ls-files", "-z", "--", devConvRecordsDir).Output()
	if err != nil || len(out) == 0 {
		return 0
	}
	n := strings.Count(string(out), "\x00")
	if exec.Command("git", "-C", dir, "rm", "-r", "-q", "--", devConvRecordsDir).Run() != nil {
		return 0
	}
	return n
}
