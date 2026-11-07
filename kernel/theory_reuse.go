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

// storedTheory returns the theory text of the most recently completed stored plan for sha.
func storedTheory(sha string) (string, string, bool) {
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
	return storedTheoryIn(repoDir, "origin/"+theoryBranch, sha)
}

func gitIn(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return string(out), err
}

// storedTheoryIn reads the newest complete plan for sha under ref in the clone at dir.
func storedTheoryIn(dir, ref, sha string) (string, string, bool) {
	root := theoryStoreRoot + "/" + shortSHA12(sha)
	ls, err := gitIn(dir, "ls-tree", "-r", "--name-only", ref, "--", root)
	if err != nil {
		return "", "", false
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
	type cand struct {
		dir  string
		when int64
		text string
	}
	var best *cand
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
		if best == nil || when > best.when {
			best = &cand{pd, when, strings.Join(parts, "\n\n")}
		}
	}
	if best == nil {
		return "", "", false
	}
	log.Printf("theory_reuse: commit %s has a complete stored theory at %s (%d bytes) — not theorized again", shortSHA12(sha), best.dir, len(best.text))
	return best.text, best.dir, true
}
