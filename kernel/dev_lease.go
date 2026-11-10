package kernel

// dev_lease.go — one run per dev branch.
//
// 11/06 two machines ran the same pipeline on the same dev branch: the first was pushing
// units 20-29 while the second stored its theory, and the second's push was rejected
// non-fast-forward; their plan writes and the dev branch's masking would have overwritten each
// other's too. The plan on the state branch names the run that last stored it (every
// devPlanSave stamps it), and a run that finds the branch held by another live run does not
// start on it.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DevHolder: a run of the kernel.
type DevHolder struct {
	Host    string    `json:"host,omitempty"`
	PID     int       `json:"pid,omitempty"`
	Started time.Time `json:"started,omitempty"` // when the process started
	Renewed time.Time `json:"renewed,omitempty"` // the last plan store
}

// devLeaseStale: a run on another machine that has not stored its plan for this long has
// stopped (the same bound as a clone's backstop, git_ops.go); a dev conversation and its
// verification store the plan far more often than that.
const devLeaseStale = 6 * time.Hour

// devTakeLeaseEnv: set, a run takes a branch another machine holds — for a machine that is
// known to be gone.
const devTakeLeaseEnv = "EINSTEINOS_TAKE_LEASE"

var processStarted = time.Now()

func thisHolder(now time.Time) DevHolder {
	h, _ := os.Hostname()
	return DevHolder{Host: h, PID: os.Getpid(), Started: processStarted, Renewed: now}
}

// pidAlive: a process of this machine is running under pid (a zombie is not).
func pidAlive(pid int) bool {
	st, _, ok := procStat(pid)
	return ok && st != "Z" && st != "X"
}

// holderRunning: the run held is still running — its pid is a live process that started when
// the run did. 11/09 23:59 a restart on cc-9cdb0db1 was refused "held by process 1891 …
// still running" while the run stamped with pid 1891 had stopped storing anything at 16:30;
// a pid alone does not say it is the same process.
func holderRunning(held DevHolder) bool {
	st, start, ok := procStat(held.PID)
	if !ok || st == "Z" || st == "X" {
		return false
	}
	if held.Started.IsZero() || start.IsZero() {
		return true
	}
	d := start.Sub(held.Started)
	if d < 0 {
		d = -d
	}
	return d < 10*time.Second
}

// procStat: the state letter and start time of a process of this machine (Linux /proc).
func procStat(pid int) (string, time.Time, bool) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", time.Time{}, false
	}
	s := string(b)
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return "", time.Time{}, true
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return "", time.Time{}, true
	}
	state := f[0]
	ticks, err := strconv.ParseInt(f[19], 10, 64) // field 22: starttime, clock ticks after boot
	if err != nil {
		return state, time.Time{}, true
	}
	boot := machineBootTime()
	if boot.IsZero() {
		return state, time.Time{}, true
	}
	return state, boot.Add(time.Duration(ticks) * time.Second / 100), true
}

// machineBootTime: when this machine booted (btime in /proc/stat).
func machineBootTime() time.Time {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "btime ") {
			if n, err := strconv.ParseInt(strings.TrimSpace(l[6:]), 10, 64); err == nil {
				return time.Unix(n, 0)
			}
		}
	}
	return time.Time{}
}

// devLeaseCheck: nil when this run may work on the branch whose plan was last stored by held.
func devLeaseCheck(held, me DevHolder, now time.Time, take bool, running func(DevHolder) bool) error {
	switch {
	case held.Host == "" || take:
		return nil
	case held.Host == me.Host && held.PID == me.PID && held.Started.Equal(me.Started):
		return nil // this run
	case held.Host == me.Host:
		if held.PID != me.PID && running(held) {
			return fmt.Errorf("the dev branch is held by process %d on this machine (started %s), which is still running — stop it first", held.PID, held.Started.Format(time.RFC3339))
		}
		return nil // an earlier run of this machine, gone
	case now.Sub(held.Renewed) < devLeaseStale:
		return fmt.Errorf("the dev branch is held by a run on %s (pid %d), whose last plan store was %s ago — two runs on one branch reject each other's pushes; stop that run, or set %s=1 if that machine is gone",
			held.Host, held.PID, now.Sub(held.Renewed).Round(time.Second), devTakeLeaseEnv)
	}
	return nil
}
