package claudeweb

import "testing"

// A dev conversation that walks this machine for the kernel, its accounts and its files does
// not run here. 11/19 12:56:51 "find / -name 'einsteinos*' -o -name '.einsteinOS'", 12:57:25
// "find /usr/local/go … -name 'adk-go'; which adk-go", 12:58:06 "cat /etc/passwd | grep -v
// nologin", 12:58:10 "find /root/.config /home/claude/.config -type f" (1672 bytes) and 12:01:55
// "ls /mnt/user-data/uploads/ && ls /mnt/transcripts/" (640 bytes), 12:01:51 "cat
// /container_info.json" all ran here.
func TestACommandThatWalksThisMachineForTheKernelDoesNotRunHere(t *testing.T) {
	for _, c := range []string{
		"find / -name 'einsteinos*' -o -name '.einsteinOS' -type f 2>/dev/null | grep -v '/home/claude/baloon' | head -10",
		"find /usr/local/go /usr/local/bin /opt -name 'adk-go' -o -name 'adk' 2>/dev/null | head -5; which adk-go 2>/dev/null",
		"cat /etc/passwd | grep -v nologin | grep -v false | head -20; ls /opt/ 2>/dev/null",
		"find /root/.config /home/claude/.config -type f 2>/dev/null | head -20; ls /root/.config/ 2>/dev/null",
		"ls /mnt/user-data/uploads/ 2>/dev/null && ls /mnt/transcripts/ 2>/dev/null | head -5",
		"cat /container_info.json",
	} {
		if touchesKernel(c, "/home/claude") == "" {
			t.Errorf("ran here: %q", c)
		}
	}
	for _, c := range []string{
		"cd /home/claude/baloon-p71c0367d2e && bash .einsteinOS/verify/p71c0367d2e/test.sh 2>&1",
		"find /home/claude/baloon-p4fde6588a6 -name \"logits_selection*\" 2>/dev/null",
		"which g++ clang++ 2>/dev/null | head -3; g++ --version",
		"ls /usr/local/corex/bin/",
	} {
		if p := touchesKernel(c, "/home/claude"); p != "" {
			t.Errorf("the unit's own work refused (%s): %q", p, c)
		}
	}
}
