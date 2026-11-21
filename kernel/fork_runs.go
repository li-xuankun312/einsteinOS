package kernel

// fork_runs.go — when the commands run here ran, and whether they were the kernel's own
// verification.
//
// A unit's verification is the only thing it runs here, but not the only thing running: units
// work side by side and their conversations' commands run on this machine meanwhile. What the
// machine shows changed during a verification can be laid on the unit only when no other
// command ran beside it — measured from these records, not assumed.

import (
	"sync"
	"time"
)

type forkRun struct {
	command    string
	verify     bool
	start, end time.Time // end is zero while it runs
}

var (
	forkRunsMu sync.Mutex
	forkRuns   []*forkRun
)

// forkRunsKeep: how long a finished run is remembered.
const forkRunsKeep = 6 * time.Hour

func noteForkRunStart(command string, verify bool) *forkRun {
	r := &forkRun{command: truncStr(command, 80), verify: verify, start: time.Now()}
	forkRunsMu.Lock()
	defer forkRunsMu.Unlock()
	kept := forkRuns[:0]
	for _, o := range forkRuns {
		if o.end.IsZero() || time.Since(o.end) < forkRunsKeep {
			kept = append(kept, o)
		}
	}
	forkRuns = append(kept, r)
	return r
}

func noteForkRunEnd(r *forkRun) {
	forkRunsMu.Lock()
	r.end = time.Now()
	forkRunsMu.Unlock()
}

// forkRunsBeside: the commands other than the kernel's verifications that ran at some time
// between from and to.
func forkRunsBeside(from, to time.Time) []string {
	forkRunsMu.Lock()
	defer forkRunsMu.Unlock()
	var out []string
	for _, r := range forkRuns {
		if r.verify || r.start.After(to) || (!r.end.IsZero() && r.end.Before(from)) {
			continue
		}
		out = append(out, r.command)
	}
	return out
}
