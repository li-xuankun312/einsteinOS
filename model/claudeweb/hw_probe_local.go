package claudeweb

// hw_probe_local.go — the GPU line in localExecPrefix is probed once at boot,
// not hardcoded.
//
// localExecPrefix told every child "GPU (4x Iluvatar BI-V100)" as a flat
// assertion, regardless of what machine actually runs the kernel. The hw_probe
// child conversation then genuinely probed the SAME local machine (shadow-mirrored
// bash, same as every other tool call) and reported back "no GPUs found" —
// contradicting the system prompt it had just been given. The sandbox was never
// lying; the hardcoded string was.
//
// Fix: probe the machine THIS PROCESS runs on, once, synchronously, before the
// first conversation starts — the same pattern already used for Axle reachability
// (axle_path.go's configureAxleProxy). Format the real result into
// localExecPrefix via fmt.Sprintf, so it is never stale and never a guess.

import (
	"os/exec"
	"strings"
	"time"
)

const gpuProbeTimeout = 5 * time.Second

// probeLocalGPU runs a bounded set of real commands and returns one honest line
// describing what GPU(s), if any, this machine actually has. It never returns
// an empty string: with nothing detected it says so explicitly, which is itself
// useful information (a child writing GPU kernels needs to know there is
// nothing to run them on, not silently inherit an assumption).
func probeLocalGPU() string {
	// vendor-specific tools first (-L: one line per device, no root needed)
	for _, probe := range []struct {
		cmd    string
		vendor string
	}{
		{"nvidia-smi -L", "NVIDIA"},
		{"ixsmi -L", "Iluvatar"},
	} {
		if out, ok := runProbeCmd(probe.cmd); ok {
			if lines := gpuLines(out); len(lines) > 0 {
				return formatGPULine(probe.vendor, lines)
			}
		}
	}
	// generic fallback: works even without a vendor CLI installed
	if out, ok := runProbeCmd("lspci"); ok {
		var gpus []string
		for _, line := range strings.Split(out, "\n") {
			low := strings.ToLower(line)
			if strings.Contains(low, "vga compatible controller") || strings.Contains(low, "3d controller") ||
				strings.Contains(low, "display controller") {
				if i := strings.Index(line, ": "); i >= 0 {
					gpus = append(gpus, strings.TrimSpace(line[i+2:]))
				}
			}
		}
		if len(gpus) > 0 {
			return formatGPULine("", gpus)
		}
	}
	return "GPU: none detected on this machine (nvidia-smi / ixsmi / lspci found nothing) — do not assume GPU hardware is available unless you probe it yourself and see otherwise"
}

// gpuLines keeps non-empty lines from a "-L"-style listing (one device per line).
func gpuLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			lines = append(lines, t)
		}
	}
	return lines
}

func formatGPULine(vendor string, devices []string) string {
	who := vendor
	if who == "" {
		who = "GPU"
	}
	if len(devices) == 1 {
		return who + " (1x): " + devices[0]
	}
	// show the count and the first device's description — repeating N near-identical
	// lines in a system prompt sent to every child wastes tokens for no benefit
	return who + " (" + itoa(len(devices)) + "x): " + devices[0]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// runProbeCmd runs a short shell command with a hard timeout. ok=false means
// "command missing, failed, or produced nothing" — never distinguished further,
// since the caller only needs to know whether to trust this probe's output.
func runProbeCmd(cmd string) (out string, ok bool) {
	c := exec.Command("bash", "-c", cmd)
	done := make(chan struct{})
	var result []byte
	var runErr error
	go func() {
		result, runErr = c.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(gpuProbeTimeout):
		if c.Process != nil {
			c.Process.Kill()
		}
		return "", false
	}
	if runErr != nil || strings.TrimSpace(string(result)) == "" {
		return "", false
	}
	return string(result), true
}
