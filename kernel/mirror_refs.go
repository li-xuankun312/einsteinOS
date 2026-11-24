package kernel

// mirror_refs.go — the unit's mirror holds the commits the kernel parks, under the remote names
// the conversations' sandboxes have them by.
//
// 11/23 20:27:45 a command of the gdn part ran CONT=origin/<wip>-cont; git show $CONT:<path> >
// <path> for 14 files: in its sandbox the branch had been fetched; in the mirror here the
// remote-tracking ref was not there, git show failed for each, and each file was left 0 bytes.
// The kernel pushed that branch itself (the rejected tree it parks): after each park the mirror's
// clone is given the commit under refs/remotes/origin/<branch>.

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// mirrorKnowsPark sets refs/remotes/origin/<branch> of the mirror's clone to sha, fetched from the
// kernel's clone that made it (no network).
func mirrorKnowsPark(mirror, cloneDir, branch, sha string) error {
	if mirror == "" || cloneDir == "" || branch == "" || sha == "" {
		return nil
	}
	// a mirror that is no clone has no refs to give: 11/24 03:24:47 the fetch into the integration's
	// mirror failed with "not a git repository" — a git command reading a branch there fails as
	// it did at 11/23 20:27:45, and what such a command leaves empty is written back
	// (mirror_writes.go)
	if _, err := os.Stat(filepath.Join(mirror, ".git")); err != nil {
		return nil
	}
	tmp := "refs/einsteinos/park/" + strings.ReplaceAll(branch, "/", "_")
	run := func(dir string, args ...string) error {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, truncStr(string(out), 300))
		}
		return nil
	}
	if err := run(cloneDir, "update-ref", tmp, sha); err != nil {
		return err
	}
	if err := run(mirror, "fetch", "--quiet", "--no-tags", cloneDir, "+"+tmp+":refs/remotes/origin/"+branch); err != nil {
		return err
	}
	log.Printf("dev_preverify: the mirror %s has %s as origin/%s, as the sandboxes do", mirror, shortSHA(sha), branch)
	return nil
}
