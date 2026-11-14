package kernel

// theory_reuse.go — a reference commit whose theory is already stored is not theorized again.
//
// 11/04 commit 0 was theorized twice (02:16, plan 7da0eac920, 8 chunks; 13:xx, plan
// 64014d8d29, 5 chunks). The second theory named its modules differently, so the stored dev
// plan dropped units and grew new ones for the same code ("33 module(s) new … 4 new
// unit(s)"; 16 units became 8). A complete stored plan — plan.json and a chunk directory with
// .lean files for each of its chunks on einsteinOS/theory — is now the theory of that
// commit, read back from the store.

import (
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
)

const theoryBranch = "einsteinOS/theory"

// storedTheoryFn is a variable for tests.
var storedTheoryFn = storedTheory

// storedTheory returns the stored theory of sha (storedTheoryIn) for reference repo.
func storedTheory(repo, sha string) (string, string, bool) {
	devRepos := GetDevRepos()
	if len(devRepos) == 0 || len(sha) < 12 {
		return "", "", false
	}
	parts := strings.Split(devRepos[0], "/")
	repoDir := fmt.Sprintf("/tmp/ref_store_%s", parts[len(parts)-1])
	if err := GitCloneOrReuse(devRepos[0], repoDir); err != nil {
		return "", "", false
	}
	if _, err := gitFetchRefs(repoDir, devRepos[0], theoryBranch); err != nil {
		return "", "", false
	}
	return storedTheoryIn(repoDir, "origin/"+theoryBranch, sha, storedPlanModules(devRepos[0], repo, sha))
}

// storedPlanModules: the modules the plan stored for the dev branch of repo@sha names (its
// units' and its module states'); none when there is no plan.
func storedPlanModules(devRepo, repo, sha string) map[string]bool {
	plan, err := loadDevPlanFromBranch(devRepo, devBranchFor(repo, sha, 0))
	if err != nil || plan == nil {
		return nil
	}
	out := map[string]bool{}
	for _, n := range plan.Nodes {
		for _, m := range n.Modules {
			out[m] = true
		}
	}
	for m := range plan.State {
		out[m] = true
	}
	return out
}

func gitIn(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return string(out), err
}

// storedTheoryIn reads a complete stored theory for sha under ref in the clone at dir: the one
// that has the most of the modules the dev branch's stored plan names (planMods — the theory
// the plan was built from), else the newest. 11/09 commit 2 had two complete theories,
// 61aa998c60 (08:38, the dev plan's) and 3a7c20346e (10:55, made by a second run on the same
// commit); a restart at 13:49 took the newer, and reconciling the plan with it dropped the
// units that had passed (ub6cd72ef49, u62b936371e) and the one in progress (ube4f27df84).
func storedTheoryIn(dir, ref, sha string, planMods map[string]bool) (string, string, bool) {
	cands := storedTheoryCandidates(dir, ref, sha)
	if len(cands) == 0 {
		return "", "", false
	}
	best := cands[0]
	for _, c := range cands {
		if c.when > best.when {
			best = c
		}
	}
	why := "the newest"
	if len(planMods) > 0 {
		most := 0
		for _, c := range cands {
			n := 0
			for _, m := range devModulesFromSections(dev_extract_file_sections(c.text)) {
				if planMods[m.Name] {
					n++
				}
			}
			if n > most {
				best, most = c, n
				why = fmt.Sprintf("the one with %d of the %d modules the dev branch's plan names", n, len(planMods))
			}
		}
	}
	log.Printf("theory_reuse: commit %s has %d complete stored theor(ies); using %s, %s (%d bytes) — not theorized again", shortSHA12(sha), len(cands), best.dir, why, len(best.text))
	return best.text, best.dir, true
}

// storedTheoryOneDir reads the stored theory in one plan directory (its last path element).
func storedTheoryOneDir(dir, ref, sha, planDir string) (string, string, bool) {
	for _, c := range storedTheoryCandidates(dir, ref, sha) {
		if path.Base(c.dir) == planDir {
			return c.text, c.dir, true
		}
	}
	return "", "", false
}

type theoryCand struct {
	dir  string
	when int64
	text string
}

// storedTheoryCandidates: every complete stored theory of sha under ref.
func storedTheoryCandidates(dir, ref, sha string) []theoryCand {
	root := theoryStoreRoot + "/" + shortSHA12(sha)
	ls, err := gitIn(dir, "ls-tree", "-r", "--name-only", ref, "--", root)
	if err != nil {
		return nil
	}
	byDir := map[string][]string{} // chunk dir -> .lean files
	var plans []string
	for _, f := range strings.Split(strings.TrimSpace(ls), "\n") {
		switch {
		case path.Base(f) == theoryPlanFile:
			plans = append(plans, path.Dir(f))
		case strings.HasSuffix(f, ".lean"):
			byDir[path.Dir(f)] = append(byDir[path.Dir(f)], f)
		}
	}
	var out []theoryCand
	for _, pd := range plans {
		raw, err := gitIn(dir, "show", ref+":"+pd+"/"+theoryPlanFile)
		if err != nil {
			continue
		}
		var p theoryPlanRef
		if json.Unmarshal([]byte(raw), &p) != nil || len(p.Chunks) == 0 {
			continue
		}
		var parts []string
		complete := true
		for i := 1; i <= len(p.Chunks); i++ {
			// the newest directory of chunk i (a retried chunk has more than one)
			chunkDir, newest := "", int64(-1)
			for d := range byDir {
				if path.Dir(d) == pd && strings.HasPrefix(path.Base(d), fmt.Sprintf("%02d-", i)) {
					t, _ := gitIn(dir, "log", "-1", "--format=%ct", ref, "--", d)
					if n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64); n > newest {
						chunkDir, newest = d, n
					}
				}
			}
			if chunkDir == "" {
				complete = false
				break
			}
			files := byDir[chunkDir]
			sort.Strings(files)
			var cf []chunkFile
			for _, f := range files {
				b, err := gitIn(dir, "show", ref+":"+f)
				if err != nil {
					continue
				}
				cf = append(cf, chunkFile{RemotePath: TheoryOutputsDir + "/" + path.Base(f), Name: path.Base(f), Content: []byte(b)})
			}
			parts = append(parts, renderChunkFiles(cf))
		}
		if !complete {
			continue
		}
		t, _ := gitIn(dir, "log", "-1", "--format=%ct", ref, "--", pd)
		when, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		out = append(out, theoryCand{pd, when, strings.Join(parts, "\n\n")})
	}
	return out
}
