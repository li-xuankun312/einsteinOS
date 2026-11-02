package kernel

// hw_probe.go — what the kernel asks of the hardware probe, and what it measures
// itself.

import (
	"fmt"
	"strings"
)

// hwProbePrompt: facts only. The old prompt ended with "context: <the task>", and
// the child answered by comparing the machine with the task ("the probe results
// don't match the setup described..."); that sentence then became the [HARDWARE]
// block of every dev prompt.
func hwProbePrompt() string {
	return fmt.Sprintf("probe this machine's hardware and report FACTS ONLY: GPU vendor/model/count, "+
		"interconnect topology, CPU model and cores, memory, kernel version. do not compare what you find with anything.\n"+
		"run ONE command that gathers all of it (lspci, nvidia-smi or ixsmi, lscpu, free, uname...). "+
		"what a command prints is cut (head and tail kept) beyond about %d characters, so keep the output compact: "+
		"pick the lines you need with grep/head/awk. if a tool is missing, say so in one line and go on.\n%s",
		LocalOutputCap(), DoneInstruction())
}

// gpuIndexCmd lists the GPU device indices under devDir: /dev/iluvatar<N> when any
// exist, otherwise /dev/nvidia<N> (not nvidiactl, nvidia-uvm…). One vendor, not both:
// on the Iluvatar machine /dev holds iluvatar0-3 AND nvidia0-7 (corex's compatibility
// nodes), and merging them reported 8 GPUs for a machine with 4 cards.
func gpuIndexCmd(devDir string) string {
	list := func(vendor string) string {
		return "ls " + shq(devDir) + " 2>/dev/null | sed -n 's/^" + vendor + "\\([0-9][0-9]*\\)$/\\1/p' | sort -n | uniq | paste -sd, -"
	}
	return "ids=$(" + list("iluvatar") + "); if [ -z \"$ids\" ]; then ids=$(" + list("nvidia") + "); fi; echo \"$ids\""
}

// gpuProbeFn runs a command on this machine and returns its stdout; a variable so
// parsing can be tested without a booted kernel.
var gpuProbeFn = func(cmd string) (string, error) {
	pid, err := ForkExec(cmd, "")
	if err != nil {
		return "", err
	}
	childPid, code, err := WaitTimeout(pid, 60*1e9)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("exit %d", code)
	}
	return GetResult(childPid), nil
}

// detectLocalGPUs returns the GPU indices ("0,1,2,3", or "" for none) and whether
// the measurement worked. An empty answer with measured=true means: this machine
// has no GPU device nodes.
func detectLocalGPUs() (string, bool) {
	out, err := gpuProbeFn(gpuIndexCmd("/dev"))
	if err != nil {
		return "", false
	}
	return parseGPUIndices(out), true
}

// parseGPUIndices keeps only numeric, non-repeated tokens of a comma/space/newline
// separated list.
func parseGPUIndices(out string) string {
	seen := map[string]bool{}
	var ids []string
	for _, f := range strings.FieldsFunc(out, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == '\r' }) {
		if f == "" || seen[f] {
			continue
		}
		ok := true
		for _, c := range f {
			if c < '0' || c > '9' {
				ok = false
			}
		}
		if ok {
			seen[f] = true
			ids = append(ids, f)
		}
	}
	return strings.Join(ids, ",")
}
