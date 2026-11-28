package claudeweb

// conv_tmp.go — what a conversation leaves in this machine's /tmp goes with it.
//
// Each conversation's sandbox starts with an empty /tmp and loses it when the conversation
// ends; the commands mirrored here write into this machine's /tmp, which stays. The next
// conversation then reads, and runs, what an earlier one left: 11/17 10:23:14 dev_planner
// (03223912) got "exit differs: sandbox 1, here 0" for ten reads of /tmp/FULLFIX_*.lean and
// /tmp/lean_fixed_files/… that dev_unit_8's conversation had written at 09:56, and at
// 10:27:13 ran its /tmp/FULLFIX.py here, which rewrites the mirror's Lean files.

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// tmpNameRe: an entry of the temp dir a command names (/tmp/<name>…).
var tmpNameRe = regexp.MustCompile(`/tmp/([A-Za-z0-9._+-]+)`)

var (
	convTmpMu    sync.Mutex
	convTmpNamed = map[string]map[string]bool{} // convID -> /tmp entries its commands named
)

// noteConvTmp records the /tmp entries a conversation's tool call names.
func noteConvTmp(convID, inputJSON string) {
	if convID == "" {
		return
	}
	ms := tmpNameRe.FindAllStringSubmatch(inputJSON, -1)
	if len(ms) == 0 {
		return
	}
	convTmpMu.Lock()
	defer convTmpMu.Unlock()
	set := convTmpNamed[convID]
	if set == nil {
		set = map[string]bool{}
		convTmpNamed[convID] = set
	}
	for _, m := range ms {
		set[m[1]] = true
	}
}

// tmpEntries are the names in this machine's temp dir now.
func tmpEntries() map[string]bool {
	out := map[string]bool{}
	es, _ := os.ReadDir(tmpRoot)
	for _, e := range es {
		out[e.Name()] = true
	}
	return out
}

// tmpRoot is the temp dir a conversation's commands name as /tmp (a variable for tests).
var tmpRoot = "/tmp"

// clearConvTmp removes what the conversation left in this machine's temp dir: the entries its
// commands named that were not there when it started. The kernel's own temp directories
// (einsteinos_*, einsteinos-*) are never a conversation's. It returns what it removed.
func clearConvTmp(convID string, before map[string]bool) []string {
	convTmpMu.Lock()
	named := convTmpNamed[convID]
	delete(convTmpNamed, convID)
	convTmpMu.Unlock()
	var removed []string
	for name := range named {
		if before[name] || strings.HasPrefix(name, "einsteinos_") || strings.HasPrefix(name, "einsteinos-") || name == "." || name == ".." {
			continue
		}
		p := filepath.Join(tmpRoot, name)
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		if err := os.RemoveAll(p); err == nil {
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	if len(removed) > 0 {
		log.Printf("claudeweb: conversation %s ended; what it left in %s here goes with its sandbox's: %s",
			truncate(convID, 8), tmpRoot, strings.Join(removed, " "))
	}
	return removed
}
