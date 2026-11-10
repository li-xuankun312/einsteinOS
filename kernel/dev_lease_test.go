package kernel

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// One run per dev branch: another machine's run that stored the plan recently holds the
// branch; the same machine's earlier run that is gone, a stale run, and a plan no run has
// stamped do not; a still-running process of this machine does.
func TestOneRunPerDevBranch(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 30, 0, 0, time.UTC)
	start := now.Add(-time.Hour)
	me := DevHolder{Host: "cc-58e31738", PID: 200, Started: start, Renewed: now}
	other := DevHolder{Host: "cc-adc62d1c", PID: 100, Started: start, Renewed: now.Add(-14 * time.Minute)}
	dead := func(DevHolder) bool { return false }
	alive := func(DevHolder) bool { return true }

	if err := devLeaseCheck(other, me, now, false, dead); err == nil || !strings.Contains(err.Error(), "cc-adc62d1c") {
		t.Fatalf("another machine's live run holds it: %v", err)
	}
	if err := devLeaseCheck(other, me, now, true, dead); err != nil {
		t.Fatalf("taken on request: %v", err)
	}
	stale := other
	stale.Renewed = now.Add(-devLeaseStale - time.Minute)
	if err := devLeaseCheck(stale, me, now, false, dead); err != nil {
		t.Fatalf("a stopped run holds nothing: %v", err)
	}
	if err := devLeaseCheck(DevHolder{}, me, now, false, dead); err != nil {
		t.Fatalf("an unstamped plan: %v", err)
	}
	if err := devLeaseCheck(me, me, now, false, alive); err != nil {
		t.Fatalf("this run: %v", err)
	}
	earlier := DevHolder{Host: me.Host, PID: 150, Started: start.Add(-time.Hour), Renewed: now.Add(-time.Minute)}
	if err := devLeaseCheck(earlier, me, now, false, dead); err != nil {
		t.Fatalf("this machine's earlier run is gone: %v", err)
	}
	if err := devLeaseCheck(earlier, me, now, false, alive); err == nil {
		t.Fatal("this machine's other process is still running")
	}
}

// Every plan store stamps the run that made it, so the plan on the state branch names its run.
func TestThePlanNamesTheRunThatStoredIt(t *testing.T) {
	p := &DevPlan{}
	devPlanSave("ref/proj", "", "einsteinOS/dev-x", p, "plan: test")
	me := thisHolder(time.Now())
	if p.Holder.Host != me.Host || p.Holder.PID != me.PID || !p.Holder.Started.Equal(me.Started) || p.Holder.Renewed.IsZero() {
		t.Fatalf("holder: %+v", p.Holder)
	}
	if err := devLeaseCheck(p.Holder, me, time.Now(), false, holderRunning); err != nil {
		t.Fatalf("its own run: %v", err)
	}
}

// A pid that a different process has taken since the run that stamped the plan is not that
// run: the process must also have started when the run did. A live process stamped with its
// own start is the run.
func TestAReusedPidIsNotTheRun(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer cmd.Process.Kill()
	_, start, ok := procStat(cmd.Process.Pid)
	if !ok || start.IsZero() {
		t.Skip("no /proc start time here")
	}
	if !holderRunning(DevHolder{PID: cmd.Process.Pid, Started: start}) {
		t.Fatal("the live process that started then is the run")
	}
	if holderRunning(DevHolder{PID: cmd.Process.Pid, Started: start.Add(-8 * time.Hour)}) {
		t.Fatal("a process that started 8 hours after the run is not the run")
	}
	cmd.Process.Kill()
	cmd.Wait()
	if holderRunning(DevHolder{PID: cmd.Process.Pid, Started: start}) {
		t.Fatal("a process that ended is not running")
	}
}
