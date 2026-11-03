package kernel

// hw_facts.go — what a dev or review prompt is told about the machine, and where each
// part comes from.
//
// The [HARDWARE] block used to be the probe conversation's text followed by the local
// output: HWInfo = stdout + "\n" + stderr. On 2026/11/02 (9d9dd1c5) the probe ran its one
// command, and in the SAME turn — before the kernel had run it on this machine and
// written the result back — wrote its report from its own sandbox's output ("Kernel:
// Linux 6.18…"); on 2026/11/03 that report said "lspci listed no GPU … neither ixsmi nor
// nvidia-smi is installed" on a machine with iluvatar0-3 and /usr/local/corex/bin/nvcc.
// That sandbox report was the first thing in [HARDWARE], ahead of the correct local
// output, in the same prompt as [GPU CONFIGURATION] saying 0,1,2,3.
//
// Now the block is built from three sources, each said for what it is:
//
//	measured   GPUs (device nodes) and the toolchain, by the kernel on this machine
//	summary    only text the probe wrote AFTER this machine's results reached it
//	           (GroundedRound, conv_grounded.go); a turn that ran tools is never used
//	raw        this machine's output of the probe's commands
//
// Every literal is a format string or a variable below.

import (
	"fmt"
	"strings"
)

// toolchainDirs are where a machine's accelerator toolchain lives outside the default
// PATH; dev_verify puts them first on PATH. toolchainTools are the programs looked for.
var (
	toolchainDirs  = []string{"/usr/local/corex/bin"}
	toolchainTools = []string{"clang++", "nvcc", "ixsmi", "nvidia-smi"}
)

const (
	hwFactsHeader    = "[measured by the kernel on this machine]"
	hwGPUFmt         = "GPUs: %[1]s (device nodes /dev/%[2]s<N>)"
	hwNoGPUFmt       = "GPUs: none (no /dev/<%[1]s><N> device nodes)"
	hwGPUUnmeasured  = "GPUs: not measured (listing /dev failed)"
	hwToolchainFmt   = "toolchain: %[1]s"
	hwNoToolchainFmt = "toolchain: none of %[1]s in %[2]s or on PATH"
	hwSummaryFmt     = "[the probe conversation %[1]s, after this machine's output reached it]\n%[2]s"
	hwNoSummaryFmt   = "[the probe conversation %[1]s wrote nothing after this machine's output reached it]"
	hwRawFmt         = "[this machine's output of the probe's commands]\n%[1]s"
	hwSectionFmt     = "[HARDWARE]\n%[1]s\n\n"
)

// HWFacts is what the kernel knows about the machine, by source.
type HWFacts struct {
	GPUMeasured bool
	GPUVendor   string   // device-node prefix that matched ("iluvatar"); "" with no GPU
	GPUs        string   // "0,1,2,3"; "" with no GPU or when not measured
	Toolchain   []string // absolute paths of toolchainTools found on this machine
	ProbeConv   string   // the probe conversation
	Summary     string   // its text written after the local results reached it
	Raw         string   // the local output of its commands
}

// Render is the [HARDWARE] block's content. It starts with hwFactsHeader, which is how a
// stored block from an older format is recognised (and not reused).
func (h HWFacts) Render() string {
	var b strings.Builder
	b.WriteString(hwFactsHeader + "\n")
	switch {
	case !h.GPUMeasured:
		b.WriteString(hwGPUUnmeasured)
	case h.GPUs == "":
		b.WriteString(fmt.Sprintf(hwNoGPUFmt, strings.Join(gpuVendors, "|")))
	default:
		b.WriteString(fmt.Sprintf(hwGPUFmt, h.GPUs, h.GPUVendor))
	}
	b.WriteString("\n")
	if len(h.Toolchain) > 0 {
		b.WriteString(fmt.Sprintf(hwToolchainFmt, strings.Join(h.Toolchain, " ")))
	} else {
		b.WriteString(fmt.Sprintf(hwNoToolchainFmt, strings.Join(toolchainTools, ", "), strings.Join(toolchainDirs, ":")))
	}
	conv := shortID(h.ProbeConv)
	b.WriteString("\n\n")
	if s := strings.TrimSpace(h.Summary); s != "" {
		b.WriteString(fmt.Sprintf(hwSummaryFmt, conv, s))
	} else {
		b.WriteString(fmt.Sprintf(hwNoSummaryFmt, conv))
	}
	if r := strings.TrimSpace(h.Raw); r != "" {
		b.WriteString("\n\n")
		b.WriteString(fmt.Sprintf(hwRawFmt, r))
	}
	return b.String()
}

// isCurrentHWInfo: a stored [HARDWARE] content in this format (an older one began with
// the probe's own, possibly sandbox-based, report).
func isCurrentHWInfo(s string) bool { return strings.HasPrefix(s, hwFactsHeader) }

// hardwareSection is the [HARDWARE] block of a prompt, or nothing.
func hardwareSection(info string) string {
	if strings.TrimSpace(info) == "" {
		return ""
	}
	return fmt.Sprintf(hwSectionFmt, info)
}

// toolchainCmd prints the absolute path of every toolchainTools program: from
// toolchainDirs first, else from PATH.
func toolchainCmd() string {
	var b strings.Builder
	for _, t := range toolchainTools {
		var tries []string
		for _, d := range toolchainDirs {
			p := strings.TrimRight(d, "/") + "/" + t
			tries = append(tries, fmt.Sprintf("[ -x %[1]s ] && echo %[1]s", shq(p)))
		}
		tries = append(tries, fmt.Sprintf("command -v %s", shq(t)))
		b.WriteString("{ " + strings.Join(tries, " || ") + "; } 2>/dev/null; ")
	}
	b.WriteString("true")
	return b.String()
}

// toolchainPATH is the PATH prefix that makes toolchainDirs win.
func toolchainPATH() string { return strings.Join(toolchainDirs, ":") }

// measureHW fills what the kernel measures itself.
func measureHW() HWFacts {
	var h HWFacts
	h.GPUs, h.GPUMeasured = detectLocalGPUs()
	if h.GPUMeasured && h.GPUs != "" {
		if out, err := gpuProbeFn(gpuVendorCmd("/dev")); err == nil {
			h.GPUVendor = strings.TrimSpace(out)
		}
	}
	if out, err := gpuProbeFn(toolchainCmd()); err == nil {
		seen := map[string]bool{}
		for _, l := range strings.Split(out, "\n") {
			if l = strings.TrimSpace(l); strings.HasPrefix(l, "/") && !seen[l] {
				seen[l] = true
				h.Toolchain = append(h.Toolchain, l)
			}
		}
	}
	return h
}
