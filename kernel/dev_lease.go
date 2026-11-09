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

// pidAlive: a process of this machine is running.
func pidAlive(pid int) bool {
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return err == nil
}

// devLeaseCheck: nil when this run may work on the branch whose plan was last stored by held.
func devLeaseCheck(held, me DevHolder, now time.Time, take bool, alive func(int) bool) error {
	switch {
	case held.Host == "" || take:
		return nil
	case held.Host == me.Host && held.PID == me.PID && held.Started.Equal(me.Started):
		return nil // this run
	case held.Host == me.Host:
		if held.PID != me.PID && alive(held.PID) {
			return fmt.Errorf("the dev branch is held by process %d on this machine, which is still running — stop it first", held.PID)
		}
		return nil // an earlier run of this machine, gone
	case now.Sub(held.Renewed) < devLeaseStale:
		return fmt.Errorf("the dev branch is held by a run on %s (pid %d), whose last plan store was %s ago — two runs on one branch reject each other's pushes; stop that run, or set %s=1 if that machine is gone",
			held.Host, held.PID, now.Sub(held.Renewed).Round(time.Second), devTakeLeaseEnv)
	}
	return nil
}
