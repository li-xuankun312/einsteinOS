package claudeweb

// outputs_archive.go — keep the local mirror of /mnt/user-data/outputs per child.
//
// A child's tools run in a remote sandbox that is fresh for every conversation,
// and the same commands are mirrored on the local machine. The local directory,
// though, is shared by every child that ever ran there and nothing cleaned it. In
// the Strata run a child wrote 10 .lean files; its verification loop
//
//	for f in *.lean; do /tmp/axle.sh $f; done
//
// ran on the local mirror, found 88 (the oldest from two days earlier), and took
// 2h05m — about 85 s per whole-file Axle call. The next child then ran the same
// loop again. Moving the old files aside when a child is created makes the local
// directory look like the remote one.

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// localOutputsDir is where the sandbox prompts tell children to write.
const localOutputsDir = "/mnt/user-data/outputs"

var archiveMu sync.Mutex

// shouldArchiveOutputs: only children that write .lean files into outputs. Dev and
// review children work in repo checkouts, and leaving them alone keeps the blast
// radius of this clean-up to the theory stage.
func shouldArchiveOutputs(role string) bool {
	return strings.HasPrefix(role, "theorize_") || strings.HasPrefix(role, "proof_fix")
}

// archiveLocalOutputs moves everything directly inside dir into
// <dir>_archive/<UTC timestamp of now>/ — moved, never deleted. The archive lives
// outside dir so a later `ls` or `*.lean` glob in dir never sees it.
//
// It assumes children run one at a time (ref_split forks chunk after chunk): a
// child that is still writing would lose its files. EINSTEIN_ARCHIVE_OUTPUTS=0
// turns it off.
func archiveLocalOutputs(dir string, now time.Time) (moved int, dest string, err error) {
	if os.Getenv("EINSTEIN_ARCHIVE_OUTPUTS") == "0" {
		return 0, "", nil
	}
	archiveMu.Lock()
	defer archiveMu.Unlock()

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, "", nil
		}
		return 0, "", err
	}
	if len(entries) == 0 {
		return 0, "", nil
	}
	base := filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"_archive", now.UTC().Format("20060102T150405Z"))
	dest = base
	for n := 2; ; n++ { // two children forked within one second must not share a directory
		if _, serr := os.Stat(dest); os.IsNotExist(serr) {
			break
		}
		dest = fmt.Sprintf("%s-%d", base, n)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return 0, "", err
	}
	for _, e := range entries {
		if rerr := os.Rename(filepath.Join(dir, e.Name()), filepath.Join(dest, e.Name())); rerr != nil {
			log.Printf("claudeweb: archive outputs: %s: %v", e.Name(), rerr)
			err = rerr
			continue
		}
		moved++
	}
	return moved, dest, err
}
