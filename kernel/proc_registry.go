package kernel

// proc_registry.go — nothing isolated into its own process group is allowed to
// outlive this program.
//
// gitGuard (git clone/fetch/push) and axleRunOnce (the Axle curl fallback) both
// put their command in its own process group (Setpgid:true, Pgid=0, so the
// group's pgid equals the leader's own pid) so that OUR OWN timeout logic can
// reliably kill the whole tree instead of just the top-level bash.
//
// That isolation has a cost: a process group separate from this program's own
// is also separate from the TERMINAL's process group, so a Ctrl-C (SIGINT) typed
// at the terminal is delivered to this program but never reaches those isolated
// children — they do not die with it. Without this file, interrupting the
// program while a git push or an Axle curl fallback is running leaves that
// subprocess (and whatever it spawned — git-remote-https, curl, a verification
// script) running in the background indefinitely: it keeps whatever file
// handles, network connections and CPU/IO it had, and a later run can collide
// with it (two processes reading/writing the same local clone, two curls
// hitting Axle under the same session).
//
// The fix is not to deliver the signal to the children (their isolation from
// the terminal is deliberate, so OUR timeouts keep working); it is for this
// program to catch the signal itself and kill every process group it isolated,
// before it exits. See examples/claudeweb's signal handler.

import "sync"

var (
	procRegMu sync.Mutex
	procReg   = map[int]struct{}{}
)

// TrackProcessGroup records a process group (identified by its leader's pid —
// true for every caller here, since they all use Setpgid:true with Pgid=0) as
// currently running and isolated from this program's own process group. Call it
// right after Start() succeeds.
func TrackProcessGroup(pgid int) {
	if pgid <= 0 {
		return
	}
	procRegMu.Lock()
	procReg[pgid] = struct{}{}
	procRegMu.Unlock()
}

// UntrackProcessGroup removes pgid once its command has finished on its own
// (successfully, by error, or via this package's own timeout kill). Safe to call
// even if pgid was never tracked or was already removed.
func UntrackProcessGroup(pgid int) {
	procRegMu.Lock()
	delete(procReg, pgid)
	procRegMu.Unlock()
}

// KillAllTrackedProcessGroups force-kills every process group currently tracked
// and returns how many it acted on. Intended to run once, synchronously, from a
// signal handler right before the program exits — it does not wait for the
// children to actually die (SIGKILL cannot be caught, so there is nothing
// further to wait for) and does not touch anything not tracked here.
func KillAllTrackedProcessGroups() int {
	procRegMu.Lock()
	pgids := make([]int, 0, len(procReg))
	for pgid := range procReg {
		pgids = append(pgids, pgid)
	}
	procRegMu.Unlock()

	for _, pgid := range pgids {
		killPgid(pgid)
		UntrackProcessGroup(pgid)
	}
	return len(pgids)
}
