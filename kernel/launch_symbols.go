package kernel

// launch_symbols.go — which kernel each recorded launch was. A runtime-API launch passes
// the kernel's host stub; its address, less the object's load base, is a FUNC symbol of the
// executable or library, named with the kernel's mangled name. dladdr only sees exported
// symbols, so the full .symtab is read here.

import (
	"debug/elf"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// KernelLaunch is one kernel a test process launched.
type KernelLaunch struct {
	PID    int
	Object string // executable or library holding the stub
	API    string
	Symbol string // mangled stub symbol, "" when it could not be resolved
	Name   string // the kernel's identifier ("" for driver-API handles and unresolved stubs)
	// Why: for a launch without a name, why none could be given
	Why string
}

// elfFuncSymbols maps symbol value → name for the FUNC symbols of an ELF file, and reports
// whether the file is position-independent (values are offsets from the load base).
func elfFuncSymbols(path string) (map[uint64]string, bool, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	out := map[uint64]string{}
	add := func(syms []elf.Symbol) {
		for _, s := range syms {
			if elf.ST_TYPE(s.Info) == elf.STT_FUNC && s.Value != 0 && s.Name != "" {
				if _, ok := out[s.Value]; !ok {
					out[s.Value] = s.Name
				}
			}
		}
	}
	if syms, err := f.Symbols(); err == nil {
		add(syms)
	}
	if syms, err := f.DynamicSymbols(); err == nil {
		add(syms)
	}
	return out, f.Type == elf.ET_DYN, nil
}

// resolveLaunches turns the probe's launch lines into named kernel launches.
func resolveLaunches(pl probeLog) []KernelLaunch {
	type objSyms struct {
		syms map[uint64]string
		pic  bool
		err  error
	}
	cache := map[string]objSyms{}
	var out []KernelLaunch
	for _, l := range pl.Launches {
		k := KernelLaunch{PID: l.PID, Object: l.Object, API: l.API, Symbol: l.Symbol}
		switch {
		case k.Symbol != "":
		case l.API == "cuLaunchKernel":
			k.Why = "a driver-API launch (cuLaunchKernel) passes a handle, not a function"
		case l.Object == "?":
			k.Why = fmt.Sprintf("no loaded object holds the launched address %#x", l.Addr)
		default:
			o, ok := cache[l.Object]
			if !ok {
				o.syms, o.pic, o.err = elfFuncSymbols(l.Object)
				cache[l.Object] = o
			}
			addr := l.Addr
			if o.pic {
				addr -= l.Base
			}
			switch {
			case o.err != nil:
				k.Why = fmt.Sprintf("%s could not be read (%v)", l.Object, o.err)
			case len(o.syms) == 0:
				k.Why = l.Object + " has no symbol table (stripped?)"
			default:
				if k.Symbol = o.syms[addr]; k.Symbol == "" {
					k.Why = fmt.Sprintf("%s has no function symbol at %#x", l.Object, addr)
				}
			}
		}
		if k.Symbol != "" {
			k.Name = kernelIdent(k.Symbol)
		}
		out = append(out, k)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// kernelIdent extracts the kernel's identifier from its stub symbol: the (last) name of an
// Itanium-mangled name, without clang's __device_stub__ prefix; extern "C" names as they are.
func kernelIdent(sym string) string {
	name := sym
	switch {
	case strings.HasPrefix(sym, "_ZN"):
		rest := sym[3:]
		rest = strings.TrimLeft(rest, "rVK")
		last := ""
		for {
			rest = strings.TrimPrefix(rest, "L") // internal linkage marks a name, not ends it
			if rest == "" || rest[0] < '0' || rest[0] > '9' {
				break
			}
			id, r, ok := mangledName(rest)
			if !ok {
				break
			}
			last, rest = id, r
		}
		if last != "" {
			name = last
		}
	case strings.HasPrefix(sym, "_Z"):
		if id, _, ok := mangledName(sym[2:]); ok {
			name = id
		}
	}
	return strings.TrimPrefix(name, "__device_stub__")
}

// mangledName reads "<length><identifier>" from the front of s.
func mangledName(s string) (id, rest string, ok bool) {
	i := 0
	for i < len(s) && unicode.IsDigit(rune(s[i])) {
		i++
	}
	if i == 0 {
		return "", s, false
	}
	n := 0
	for _, c := range s[:i] {
		n = n*10 + int(c-'0')
	}
	if n <= 0 || i+n > len(s) {
		return "", s, false
	}
	return s[i : i+n], s[i+n:], true
}

// launchedNames is the set of kernel identifiers among the launches.
func launchedNames(ls []KernelLaunch) map[string]bool {
	set := map[string]bool{}
	for _, l := range ls {
		if l.Name != "" {
			set[l.Name] = true
		}
	}
	return set
}

// probeMeasured: the launch probe ran in at least one process of the trace.
func probeMeasured(t ProcTrace) bool { return t.ProbeErr == "" && len(t.ProbeLoaded) > 0 }

// anyLaunched: some launch is one of the named kernels.
func anyLaunched(names map[string]bool, ls []KernelLaunch) bool {
	for _, l := range ls {
		if names[l.Name] {
			return true
		}
	}
	return false
}

// setText lists a name set, sorted.
func setText(set map[string]bool) string {
	var out []string
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// launchesText lists the launched kernels for a failure text ("none" when nothing launched;
// driver-API and unresolved launches are counted).
func launchesText(ls []KernelLaunch) string {
	names := launchedNames(ls)
	unnamed := 0
	for _, l := range ls {
		if l.Name == "" {
			unnamed++
		}
	}
	txt := setText(names)
	if unnamed > 0 {
		if txt != "" {
			txt += " "
		}
		txt += fmt.Sprintf("(+%d unnamed)", unnamed)
	}
	if txt == "" {
		return "none"
	}
	return txt
}

// launchUnmeasured says why a GPU test's launches could not be seen, "" when they could
// (or when the test opened no device, which gpu_unused judges). Called on traces taken with
// the probe (ProbeErr or ProbeLoaded set); a trace without either was not probed.
func launchUnmeasured(t ProcTrace) string {
	switch {
	case t.ProbeErr != "":
		return "the launch probe could not be built on this machine: " + t.ProbeErr
	case len(t.ProbeLoaded) == 0:
		return ""
	case len(t.Devices) == 0:
		return ""
	case len(t.Launches) > 0:
		// launches were seen but none could be named: which kernels ran is not measured, and
		// "launched none of the unit's kernels" would be a guess — 11/09 12:02-12:29 unit
		// ube4f27df84 was sent back gpu_kernel_not_launched three times with "kernels
		// launched: 7 distinct ((+7 unnamed))"
		if len(launchedNames(t.Launches)) > 0 {
			return ""
		}
		why := map[string]bool{}
		for _, l := range t.Launches {
			if l.Why != "" {
				why[l.Why] = true
			} else {
				why["a "+l.API+" launch without a name"] = true
			}
		}
		return fmt.Sprintf("%d launch(es) were seen and none could be named: %s", len(t.Launches), shortList(sortedKeys(why), 4))
	}
	var static []string
	for _, exe := range t.ProbeLoaded {
		if definesFunc(exe, "cudaLaunchKernel") {
			static = append(static, exe)
		}
	}
	if len(static) > 0 {
		return fmt.Sprintf("%s link the CUDA runtime statically, so their launches never pass through the probe; link the shared runtime (clang: -lcudart, nvcc: -cudart shared)", shortList(static, 6))
	}
	// nothing launched and nothing could have been seen: no probed executable imports a
	// launch function the probe watches — a toolchain with another launch path is not
	// taken for "no kernel ran"
	var cudaImports []string
	importsProbed := false
	for _, exe := range t.ProbeLoaded {
		for _, sym := range importedFuncs(exe) {
			if probedLaunchAPIs[sym] {
				importsProbed = true
			}
			if strings.HasPrefix(sym, "cuda") || strings.HasPrefix(sym, "cu") && len(sym) > 2 && sym[2] >= 'A' && sym[2] <= 'Z' {
				cudaImports = append(cudaImports, sym)
			}
		}
	}
	if !importsProbed {
		sort.Strings(cudaImports)
		return fmt.Sprintf("a GPU device was opened but no probed executable imports a launch function the probe watches (%s); the CUDA functions they import: %s",
			setText(probedLaunchAPIs), shortList(dedupe(cudaImports), 12))
	}
	return ""
}

// probedLaunchAPIs are the launch functions launch_probe.c interposes.
var probedLaunchAPIs = map[string]bool{"cudaLaunchKernel": true, "cudaLaunchKernel_ptsz": true,
	"cudaLaunchCooperativeKernel": true, "cudaLaunchKernelExC": true, "cudaLaunch": true, "cuLaunchKernel": true}

// importedFuncs: the undefined (imported) symbols of an ELF file, nil when it is not one.
func importedFuncs(path string) []string {
	f, err := elf.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	syms, err := f.ImportedSymbols()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(syms))
	for _, s := range syms {
		out = append(out, s.Name)
	}
	return out
}

// dedupe drops repeats from a sorted list.
func dedupe(xs []string) []string {
	var out []string
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}

// definesFunc: the ELF file defines (not only imports) the named function.
func definesFunc(path, name string) bool {
	syms, _, err := elfFuncSymbols(path)
	if err != nil {
		return false
	}
	for _, n := range syms {
		if n == name {
			return true
		}
	}
	return false
}

// sortedKeys returns a set's members, sorted.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// LaunchSummary: the kernels the trace's processes launched, for the status and the
// reviewer ("" when the test was not probed).
func (t ProcTrace) LaunchSummary() string {
	if t.ProbeErr != "" {
		return "not measured: " + t.ProbeErr
	}
	if len(t.ProbeLoaded) == 0 {
		return ""
	}
	return fmt.Sprintf("%d distinct (%s) in %d probed executable(s)", len(t.Launches), launchesText(t.Launches), len(t.ProbeLoaded))
}

// LaunchSummaryOr is LaunchSummary with a stand-in for an unprobed test.
func (t ProcTrace) LaunchSummaryOr(none string) string {
	if s := t.LaunchSummary(); s != "" {
		return s
	}
	return none
}

// LaunchCount: the number of distinct kernels launched, "-" for an unprobed test.
func (t ProcTrace) LaunchCount() string {
	if t.ProbeErr != "" || len(t.ProbeLoaded) == 0 {
		return "-"
	}
	return fmt.Sprint(len(t.Launches))
}
