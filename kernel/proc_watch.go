package kernel

// proc_watch.go — what a build or test actually did on this machine, from /proc.
//
// "compile=pass smoke=pass" was two exit codes. On 2026/11/03 they passed for units whose
// sources are CUDA while no kernel was ever launched: the tests ran on the CPU, and the
// only CUDA compiled was the repository's pre-existing src/*.cu. Exit codes cannot tell
// these apart; the processes can. While a script runs, the watcher walks its process tree
// every tick and records:
//
//	Devices   GPU device nodes any process in the tree had open (/proc/<pid>/fd → /dev/iluvatar0)
//	Exes      executables that ran (/proc/<pid>/exe)
//	Cmdlines  command lines that ran (/proc/<pid>/cmdline) — a compiler invocation names its file
//
// The script records its own pid (echo $$) before exec'ing, so the tree is exactly its
// processes. Short processes can fall between ticks; what is recorded did happen.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	procRoot      = "/proc"
	procWatchTick = 50 * time.Millisecond
	// gpuDeviceRe matches an accelerator device node; built from gpuVendors (hw_probe.go)
	gpuDeviceRe = func() *regexp.Regexp {
		return regexp.MustCompile(fmt.Sprintf(`^/dev/(%s)[0-9]+$`, strings.Join(gpuVendors, "|")))
	}
)

// ProcTrace is what a watched process tree did.
type ProcTrace struct {
	Devices []string
	// Weights: model weight files (.safetensors, .gguf) the processes had open or mapped
	Weights  []string
	Exes     []string
	Cmdlines []string
	Procs    int // distinct processes seen
	// the kernel launch probe (launch_probe.go), for a GPU unit's test.sh: the kernels its
	// processes launched, the executables the probe was loaded into, and why there is no
	// measurement when the probe could not be built
	Launches    []KernelLaunch
	ProbeLoaded []string
	ProbeErr    string
	// Processes: each process of the tree with what it held — so that what one process did
	// (loaded the weights) and what another did (launched kernels) are not one record
	Processes []ProcSeen
}

// ProcSeen is one process of a watched tree: its executable, the weight files it opened or
// mapped, and the shared objects of the run's own directory it mapped (a library the
// workspace built).
type ProcSeen struct {
	PID           int      `json:"pid"`
	Exe           string   `json:"exe"`
	Weights       []string `json:"weights,omitempty"`
	WorkspaceLibs []string `json:"workspace_libs,omitempty"`
	// Cmdline: its arguments; WorkspaceArgs: those that name a file of the run's directory (a
	// script of the workspace an interpreter runs); Large: the other files of at least
	// largeFileMin it opened or mapped (a pack of weights the port converted)
	Cmdline       string   `json:"cmdline,omitempty"`
	WorkspaceArgs []string `json:"workspace_args,omitempty"`
	Large         []string `json:"large,omitempty"`
	// First, Last: when the watcher first and last saw it running
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
}

type procWatcher struct {
	mu       sync.Mutex
	devRe    *regexp.Regexp
	devices  map[string]bool
	weights  map[string]bool
	exes     map[string]bool
	cmdlines map[string]bool
	seen     map[int]bool
	root     string // the run's directory: its shared objects are recorded per process
	procs    map[int]*procSeenSets
	sizes    map[string]int64 // a file's size, read once
}

// largeFileMin: the size from which a file a process opens is recorded with it (ProcSeen.Large).
const largeFileMin = 64 << 20

// noteLarge records f with the process when it is a regular file of at least largeFileMin.
func (w *procWatcher) noteLarge(p *procSeenSets, f string) {
	if !strings.HasPrefix(f, "/") || strings.HasPrefix(f, "/dev/") || strings.HasPrefix(f, "/proc/") || strings.Contains(filepath.Base(f), ".so") {
		return
	}
	n, ok := w.sizes[f]
	if !ok {
		n = -1
		if st, err := os.Stat(f); err == nil && st.Mode().IsRegular() {
			n = st.Size()
		}
		w.sizes[f] = n
	}
	if n >= largeFileMin {
		p.large[f] = true
	}
}

// noteArgs records the arguments of the process that name a file of the run's directory.
func (w *procWatcher) noteArgs(p *procSeenSets, dir string, argv []string) {
	if w.root == "" || len(argv) < 2 {
		return
	}
	cwd, _ := os.Readlink(filepath.Join(dir, "cwd"))
	for _, a := range argv[1:] {
		f := a
		if !filepath.IsAbs(f) {
			if cwd == "" {
				continue
			}
			f = filepath.Join(cwd, f)
		}
		if !strings.HasPrefix(f, w.root+"/") {
			continue
		}
		if st, err := os.Stat(f); err == nil && st.Mode().IsRegular() {
			p.wsArgs[f] = true
		}
	}
}

type procSeenSets struct {
	cmdline string
	wsArgs  map[string]bool
	large   map[string]bool
	first   time.Time
	last    time.Time
	exe     string
	weights map[string]bool
	libs    map[string]bool
}

func (w *procWatcher) proc(pid int) *procSeenSets {
	p := w.procs[pid]
	if p == nil {
		p = &procSeenSets{weights: map[string]bool{}, libs: map[string]bool{}, wsArgs: map[string]bool{}, large: map[string]bool{}, first: time.Now()}
		w.procs[pid] = p
	}
	p.last = time.Now()
	return p
}

// watchProcTree watches the tree rooted at the pid written to pidFile until stop is called.
func watchProcTree(pidFile string) (stop func() ProcTrace) { return watchProcTreeIn(pidFile, "") }

// watchProcTreeIn is watchProcTree with the run's directory, whose shared objects a process
// maps are recorded with it (ProcSeen.WorkspaceLibs).
func watchProcTreeIn(pidFile, root string) (stop func() ProcTrace) {
	if root != "" {
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
	}
	w := &procWatcher{devRe: gpuDeviceRe(), devices: map[string]bool{}, weights: map[string]bool{}, exes: map[string]bool{},
		cmdlines: map[string]bool{}, seen: map[int]bool{}, root: root, procs: map[int]*procSeenSets{}, sizes: map[string]int64{}}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		root := 0
		t := time.NewTicker(procWatchTick)
		defer t.Stop()
		for {
			if root == 0 {
				if b, err := os.ReadFile(pidFile); err == nil {
					root, _ = strconv.Atoi(strings.TrimSpace(string(b)))
				}
			}
			if root > 0 {
				w.scan(root)
			}
			select {
			case <-done:
				if root > 0 {
					w.scan(root)
				}
				return
			case <-t.C:
			}
		}
	}()
	return func() ProcTrace {
		close(done)
		<-finished
		return w.trace()
	}
}

func (w *procWatcher) scan(root int) {
	children := map[int][]int{}
	ents, _ := os.ReadDir(procRoot)
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if ppid := procPPid(pid); ppid > 0 {
			children[ppid] = append(children[ppid], pid)
		}
	}
	queue := []int{root}
	w.mu.Lock()
	defer w.mu.Unlock()
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		queue = append(queue, children[pid]...)
		dir := filepath.Join(procRoot, strconv.Itoa(pid))
		ps := w.proc(pid) // seen running now: its First and Last
		// read every time: a process that execs keeps its pid and changes what it runs
		if exe, err := os.Readlink(filepath.Join(dir, "exe")); err == nil {
			w.seen[pid] = true
			w.exes[strings.TrimSuffix(exe, " (deleted)")] = true
			ps.exe = strings.TrimSuffix(exe, " (deleted)")
		}
		if b, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil && len(b) > 0 {
			cl := strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
			w.cmdlines[cl] = true
			if cl != ps.cmdline {
				ps.cmdline = cl
				w.noteArgs(ps, dir, strings.Split(strings.TrimRight(string(b), "\x00"), "\x00"))
			}
		}
		fds, _ := os.ReadDir(filepath.Join(dir, "fd"))
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
			if err != nil {
				continue
			}
			if w.devRe.MatchString(target) {
				w.devices[target] = true
			}
			if weightFileRe.MatchString(target) {
				w.weights[target] = true
				ps.weights[target] = true
			} else {
				w.noteLarge(ps, target)
			}
		}
		// a loader that maps the weights can close their descriptors at once: the mappings stay
		if b, err := os.ReadFile(filepath.Join(dir, "maps")); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				i := strings.IndexByte(line, '/')
				if i < 0 {
					continue
				}
				f := strings.TrimSuffix(line[i:], " (deleted)")
				if weightFileRe.MatchString(f) {
					w.weights[f] = true
					ps.weights[f] = true
				} else {
					w.noteLarge(ps, f)
				}
				if w.root != "" && strings.HasPrefix(f, w.root+"/") && strings.Contains(filepath.Base(f), ".so") {
					w.proc(pid).libs[f] = true
				}
			}
		}
	}
}

// procPPid reads the parent pid from /proc/<pid>/stat (the field after the ")" of the name).
func procPPid(pid int) int {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(f[1])
	return ppid
}

func (w *procWatcher) trace() ProcTrace {
	w.mu.Lock()
	defer w.mu.Unlock()
	keys := func(m map[string]bool) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	var procs []ProcSeen
	for pid, p := range w.procs {
		if p.exe == "" && len(p.weights) == 0 {
			continue // listed before it ran, or gone before it was read
		}
		procs = append(procs, ProcSeen{PID: pid, Exe: p.exe, Weights: keys(p.weights), WorkspaceLibs: keys(p.libs),
			Cmdline: truncStr(p.cmdline, 200), WorkspaceArgs: keys(p.wsArgs), Large: keys(p.large), First: p.first, Last: p.last})
	}
	sort.Slice(procs, func(i, j int) bool { return procs[i].PID < procs[j].PID })
	return ProcTrace{Devices: keys(w.devices), Weights: keys(w.weights), Exes: keys(w.exes), Cmdlines: keys(w.cmdlines), Procs: len(w.seen), Processes: procs}
}

// watchedCommand wraps a shell command so its process tree is the watched one: the shell
// writes its pid, then execs the command (same pid).
func watchedCommand(pidFile, command string) string {
	return fmt.Sprintf("echo $$ > %s; exec bash -c %s", shq(pidFile), shq(command))
}

// weightFileRe: a model weight file — the formats a model directory holds (hasModel)
var weightFileRe = regexp.MustCompile(`\.(safetensors|gguf)$`)
