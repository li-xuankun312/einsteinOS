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
	return Bagu(hwProbeWhere, hwProbeWhat, fmt.Sprintf(hwProbeContentFmt, LocalOutputCap(), RoundBudget("hw_probe"))+DoneInstruction(), "6", "hw_init")
}

// inserted into the shell for the hardware probe
const hwProbeWhere = "运行内核的这台机器"
const hwProbeWhat = "内核要这台机器的硬件事实"

// %[1]d the output cap, %[2]d the rounds; what the kernel takes from it
const hwProbeContentFmt = "[HARDWARE] GPU vendor/model/count, interconnect topology, CPU model and cores, memory, kernel version — the kernel keeps them as facts of this machine, beside the GPUs and toolchain it measures itself.\n" +
	"[OUTPUT CAP] what a command prints is cut (head and tail kept) beyond about %[1]d characters.\n" +
	"[ROUNDS] this conversation ends after %[2]d rounds; each round is one round trip.\n"

// gpuVendors are the device-node prefixes the kernel looks for under /dev, in order of
// preference; the first vendor that has nodes is the machine's vendor. One vendor, not
// all: on the Iluvatar machine /dev holds iluvatar0-3 AND nvidia0-7 (corex's
// compatibility nodes), and merging them reported 8 GPUs for a machine with 4 cards.
var gpuVendors = []string{"iluvatar", "nvidia"}

// gpuListCmdFmt lists one vendor's indices: %[1]s the directory, %[2]s the prefix.
const gpuListCmdFmt = `ls %[1]s 2>/dev/null | sed -n 's/^%[2]s\([0-9][0-9]*\)$/\1/p' | sort -n | uniq | paste -sd, -`

// gpuPickCmd prints, for the first vendor in gpuVendors with device nodes under devDir,
// what pick(vendor, list-command) produces; nothing when no vendor has nodes.
func gpuPickCmd(devDir string, pick func(vendor, list string) string) string {
	var b strings.Builder
	for i, v := range gpuVendors {
		if i > 0 {
			b.WriteString(" el")
		}
		b.WriteString(fmt.Sprintf(`if [ -n "$(%[1]s)" ]; then %[2]s;`, fmt.Sprintf(gpuListCmdFmt, shq(devDir), v), pick(v, fmt.Sprintf(gpuListCmdFmt, shq(devDir), v))))
	}
	if len(gpuVendors) > 0 {
		b.WriteString(" fi")
	}
	return b.String()
}

// gpuIndexCmd prints the GPU indices ("0,1,2,3") of the machine's vendor under devDir.
func gpuIndexCmd(devDir string) string {
	return gpuPickCmd(devDir, func(_, list string) string { return list })
}

// gpuVendorCmd prints which vendor's device nodes gpuIndexCmd counts.
func gpuVendorCmd(devDir string) string {
	return gpuPickCmd(devDir, func(vendor, _ string) string { return "echo " + shq(vendor) })
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
