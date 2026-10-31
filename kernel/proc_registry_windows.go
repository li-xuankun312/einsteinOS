//go:build windows

package kernel

// Windows has no equivalent of a POSIX process group kill, and setProcGroup /
// hardKillOnCancel are already no-ops there (see git_guard_windows.go,
// shadow_proc_windows.go) — so there is nothing isolated to track or kill.
func killPgid(pgid int) {}
