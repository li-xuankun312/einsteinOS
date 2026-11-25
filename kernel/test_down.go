package kernel

// test_down.go — a test.sh the machine went down in, read from the crash notes.
//
// 11/24 17:40:14.827 the integration unit's test.sh started (einsteinos-stages.log); no end of
// it was noted, and at 17:48 the node's uptime was 6 minutes. The launch log on the crash
// directory ended with router_top10_parity (pid 32031) launching its 12th distinct kernel, at
// 0x1e5b0 of the executable, unresolved ("-"): the executable was in /tmp and went with the
// machine. A run started again continues from the same parked tree, builds it and runs the same
// test.sh. 12:14:51 the same build had passed on another machine, which went down the same way.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// testDown is a test.sh noted as started in the crash notes and never noted as ended, on a
// machine booted after it started.
type testDown struct {
	Unit, Test string
	LaunchLog  string // the launch log its test.sh wrote, on the crash directory
	At         time.Time
	Boot       time.Time
	// the process whose launch was noted last, and that launch; Kernels: the distinct kernels
	// that process had launched (the probe notes each once per process, before it runs)
	Exe     string
	PID     int
	Launch  probeLaunch
	Kernels int
	Noted   bool // the launch log held a launch
}

// testDownRecord is what the kernel measured of a testDown after the unit's build had made the
// executable again, kept beside the launch log on the crash directory (einsteinos-down-*.json).
type testDownRecord struct {
	// the test the machine went down in: the unit, what it ran (its test.sh, or the program run
	// alone before it) and when it started
	Unit   string `json:"unit,omitempty"`
	Test   string `json:"test,omitempty"`
	At     string `json:"at,omitempty"`
	Symbol string `json:"symbol,omitempty"`
	Kernel string `json:"kernel,omitempty"`
	Why    string `json:"why,omitempty"` // why no kernel name could be given
	// Sources: the workspace files defining the kernel, and their contents' sha256
	Sources map[string]string `json:"sources,omitempty"`
	ExeSHA  string            `json:"exe_sha256,omitempty"`
}

const crashStagesTimeLayout = "2006/01/02 15:04:05.000"

var (
	crashTestStartRe = regexp.MustCompile(`^unit (\S+): (\S+) starts; its kernel launches go to (.*)$`)
	crashTestEndRe   = regexp.MustCompile(`^unit (\S+): (\S+) ended \(exit -?\d+\)$`)
)

// machineBootTimeFn is a variable for tests (machineBootTime: dev_lease.go). A container reads
// its node's: 11/24 12:24 and 17:48 the uptime a shell in the pod read was the node's.
var machineBootTimeFn = machineBootTime

// lastTestDown reads dir's einsteinos-stages.log: unit's last test.sh start, when no end of it
// follows and this machine booted after it, with what its launch log noted last; nil otherwise.
func lastTestDown(dir, unit string) *testDown {
	f, err := os.Open(filepath.Join(dir, "einsteinos-stages.log"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var d *testDown
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		l := sc.Text()
		if len(l) <= len(crashStagesTimeLayout)+1 {
			continue
		}
		at, err := time.ParseInLocation(crashStagesTimeLayout, l[:len(crashStagesTimeLayout)], time.Local)
		if err != nil {
			continue
		}
		rest := l[len(crashStagesTimeLayout)+1:]
		if m := crashTestStartRe.FindStringSubmatch(rest); m != nil && m[1] == unit {
			d = &testDown{Unit: unit, Test: m[2], LaunchLog: strings.TrimSpace(m[3]), At: at}
		} else if m := crashTestEndRe.FindStringSubmatch(rest); m != nil && m[1] == unit && d != nil && m[2] == d.Test {
			d = nil
		}
	}
	if d == nil {
		return nil
	}
	boot := machineBootTimeFn()
	if boot.IsZero() || !boot.After(d.At) {
		return nil // still running, or this machine has not been down since
	}
	d.Boot = boot
	if d.LaunchLog != "" {
		pl := readProbeLog(d.LaunchLog)
		if n := len(pl.Launches); n > 0 {
			d.Launch, d.PID, d.Noted = pl.Launches[n-1], pl.Launches[n-1].PID, true
			d.Exe = pl.Loaded[d.PID]
			for _, l := range pl.Launches {
				if l.PID == d.PID {
					d.Kernels++
				}
			}
		}
	}
	return d
}

// testDownRecordPath: the record of d, beside its launch log (or the stages log).
func testDownRecordPath(dir string, d *testDown) string {
	key := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(d.LaunchLog), "einsteinos-launch-"), ".log")
	if d.LaunchLog == "" {
		key = d.Unit + "-" + d.At.Format("20060102-150405.000")
	}
	return filepath.Join(dir, "einsteinos-down-"+key+".json")
}

func loadTestDownRecord(path string) (*testDownRecord, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var r testDownRecord
	if json.Unmarshal(b, &r) != nil {
		return nil, false
	}
	return &r, true
}

func saveTestDownRecord(path string, r *testDownRecord) {
	b, _ := json.MarshalIndent(r, "", "  ")
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

// measureTestDown resolves d's last launch in the executable the build has just made again at
// the same path, and finds the workspace files defining that kernel.
func measureTestDown(ws string, d *testDown) *testDownRecord {
	r := &testDownRecord{Unit: d.Unit, Test: d.Test, At: d.At.Format(crashStagesTimeLayout)}
	switch {
	case !d.Noted:
		r.Why = "its launch log noted no kernel launch"
		return r
	case d.Exe == "":
		r.Why = fmt.Sprintf("its launch log has no load line for pid %d", d.PID)
		return r
	}
	if h, ok := fileSHA256(d.Exe); ok {
		r.ExeSHA = h
	}
	ks := resolveLaunches(probeLog{Loaded: map[int]string{d.PID: d.Exe}, Launches: []probeLaunch{d.Launch}})
	if len(ks) == 1 {
		r.Symbol, r.Kernel, r.Why = ks[0].Symbol, ks[0].Name, ks[0].Why
	}
	if r.Kernel != "" {
		r.Sources = map[string]string{}
		for _, p := range kernelDefiningFiles(ws, r.Kernel) {
			if h, ok := fileSHA256(filepath.Join(ws, p)); ok {
				r.Sources[p] = h
			}
		}
	}
	return r
}

func fileSHA256(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), true
}

var kernelSourceExts = map[string]bool{".cu": true, ".cuh": true, ".cpp": true, ".cc": true, ".cxx": true, ".c": true, ".h": true, ".hpp": true, ".hh": true, ".inc": true, ".inl": true}

// kernelDefiningFiles: the workspace's source files (relative, sorted) with a __global__
// definition of the kernel named name — its name followed by '(' and, before the next ';', a '{',
// with __global__ since the last ';' or '}' before it.
func kernelDefiningFiles(ws, name string) []string {
	re, err := regexp.Compile(`\b` + regexp.QuoteMeta(name) + `\s*\(`)
	if err != nil {
		return nil
	}
	var out []string
	_ = filepath.WalkDir(ws, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if e.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !kernelSourceExts[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		if st, err := e.Info(); err != nil || st.Size() > 4<<20 {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		s := string(b)
		for _, m := range re.FindAllStringIndex(s, -1) {
			before := s[:m[0]]
			if i := strings.LastIndexAny(before, ";}"); i >= 0 {
				before = before[i+1:]
			}
			after := s[m[1]:]
			brace, semi := strings.Index(after, "{"), strings.Index(after, ";")
			if strings.Contains(before, "__global__") && brace >= 0 && (semi < 0 || brace < semi) {
				rel, _ := filepath.Rel(ws, p)
				out = append(out, filepath.ToSlash(rel))
				break
			}
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// testDownFact: what was measured of d and r, in one line.
const testDownFactFmt = "%[1]s started %[2]s at %[3]s, no end of it was noted, and this machine booted again at %[4]s; the last kernel launch its probe noted was the %[5]s of %[6]s (pid %[7]d): %[8]s"

func testDownFact(d *testDown, r *testDownRecord) string {
	if !d.Noted {
		return fmt.Sprintf("%s started %s at %s, no end of it was noted, and this machine booted again at %s; its launch log (%s) noted no kernel launch",
			d.Unit, d.Test, d.At.Format("01/02 15:04:05"), d.Boot.Format("01/02 15:04:05"), d.LaunchLog)
	}
	what := ""
	switch {
	case r.Kernel != "" && len(r.Sources) > 0:
		what = fmt.Sprintf("%s, defined in %s", r.Kernel, strings.Join(sourcePaths(r.Sources), ", "))
	case r.Kernel != "":
		what = r.Kernel + ", defined in no source file of the workspace"
	default:
		what = fmt.Sprintf("the stub at %#x of the executable, unresolved (%s)", d.Launch.Addr-d.Launch.Base, r.Why)
	}
	return fmt.Sprintf(testDownFactFmt, d.Unit, d.Test, d.At.Format("01/02 15:04:05"), d.Boot.Format("01/02 15:04:05"),
		ordinal(d.Kernels)+" distinct kernel", d.Exe, d.PID, what)
}

func sourcePaths(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func ordinal(n int) string {
	suf := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suf = "st"
		case 2:
			suf = "nd"
		case 3:
			suf = "rd"
		}
	}
	return strconv.Itoa(n) + suf
}

// noteTestDown, before a unit's test.sh: when the crash notes say the machine went down in its
// last one, the executable its last noted launch ran from is measured (once — the build has made
// it again), kept on the crash directory and logged. It returns what is known, or nil.
func noteTestDown(ws, unitID string) (*testDown, *testDownRecord, bool) {
	dir := crashDir()
	if dir == "" {
		return nil, nil, false
	}
	d := lastTestDown(dir, unitID)
	if d == nil {
		return nil, nil, false
	}
	path := testDownRecordPath(dir, d)
	if r, ok := loadTestDownRecord(path); ok {
		return d, r, false
	}
	r := measureTestDown(ws, d)
	saveTestDownRecord(path, r)
	log.Printf("dev_verify: unit %s: the machine went down in its test.sh — %s", unitID, testDownFact(d, r))
	return d, r, true
}

// gateMachineDownFmt: the verification's gate when test.sh is not run because the machine went
// down in the last one and what it went down with is as it was.
const gateMachineDownFmt = "machine_down_during_test: %[1]s — test.sh is not run again while %[2]s"

// testDownSame: whether what the machine went down with is as it was — the files defining the
// kernel its probe noted last, else that executable as the build makes it — and what was compared.
// With neither known, only the verification that first read the crash notes is held.
func testDownSame(ws string, d *testDown, r *testDownRecord, first bool) (bool, string) {
	switch {
	case len(r.Sources) > 0:
		var changed []string
		for _, p := range sourcePaths(r.Sources) {
			if h, ok := fileSHA256(filepath.Join(ws, p)); !ok || h != r.Sources[p] {
				changed = append(changed, p)
			}
		}
		if len(changed) > 0 {
			// a kernel the machine went down with more than once is not let run by a change of its
			// files: 11/25 01:49 src/ix_router_top10_wave.cu had changed and the machine went down
			// at the same kernel, at the same offset
			if n := len(testDownsOfKernel(crashDir(), r.Kernel)); n >= testDownRepeatHold && !testDownReleased(r.Kernel) {
				return true, fmt.Sprintf(testDownRepeatFmt, strings.Join(changed, ", "), r.Kernel, n, testDownReleaseEnv)
			}
			return false, fmt.Sprintf("%s changed since the machine went down", strings.Join(changed, ", "))
		}
		return true, fmt.Sprintf("%s, defining %s, are as they were", strings.Join(sourcePaths(r.Sources), ", "), r.Kernel)
	case r.ExeSHA != "":
		if h, ok := fileSHA256(d.Exe); !ok || h != r.ExeSHA {
			return false, d.Exe + " differs from the one built after the machine went down"
		}
		return true, fmt.Sprintf("%s builds the same (sha256 %s)", d.Exe, r.ExeSHA[:12])
	default:
		return first, "the program it went down with is not known; only this verification is held"
	}
}

// testDownRepeatHold: how many times the machine may go down at one kernel, on the crash
// directory, before a change of the files defining it no longer lets test.sh run.
const testDownRepeatHold = 2

// testDownReleaseEnv names, comma-separated, kernels whose test.sh runs again after a change of
// their files however many times the machine went down at them — set by whoever runs the kernel.
const testDownReleaseEnv = "EINSTEINOS_TEST_DOWN_RELEASE"

const testDownRepeatFmt = "%[1]s changed since the machine went down, but the machine went down at %[2]s %[3]d times on the crash notes; test.sh runs again after a change only when %[4]s names %[2]s"

func testDownReleased(kernel string) bool {
	for _, k := range strings.Split(os.Getenv(testDownReleaseEnv), ",") {
		if strings.TrimSpace(k) == kernel && kernel != "" {
			return true
		}
	}
	return false
}

// testDownsOfKernel: the records on dir (einsteinos-down-*.json) of the times the machine went
// down at the kernel named kernel, oldest first.
func testDownsOfKernel(dir, kernel string) []*testDownRecord {
	if dir == "" || kernel == "" {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "einsteinos-down-*.json"))
	var out []*testDownRecord
	for _, p := range paths {
		if r, ok := loadTestDownRecord(p); ok && r.Kernel == kernel {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}
