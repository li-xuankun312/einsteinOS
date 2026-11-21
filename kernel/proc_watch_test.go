package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A real process tree: the script's child holds a "device" open and runs a compiler-like
// command line. The watcher must see the device, the executables and the command lines.
func TestProcWatchSeesDevicesExesAndCommandLines(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc")
	}
	dir := t.TempDir()
	dev := filepath.Join(dir, "iluvatar0") // stands in for /dev/iluvatar0
	os.WriteFile(dev, nil, 0o644)
	old := gpuDeviceRe
	t.Cleanup(func() { gpuDeviceRe = old })
	gpuDeviceRe = func() *regexp.Regexp { return regexp.MustCompile(regexp.QuoteMeta(dir) + `/(iluvatar|nvidia)[0-9]+$`) }

	pidFile := filepath.Join(dir, "pid")
	script := "( exec 3<" + dev + "; sleep 0.4 ) & bash -c 'sleep 0.3 # clang++ -x cuda --cuda-gpu-arch=ivcore10 src/k.cu' ; wait"
	stop := watchProcTree(pidFile)
	if out, err := exec.Command("bash", "-c", watchedCommand(pidFile, script)).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	tr := stop()

	if len(tr.Devices) != 1 || tr.Devices[0] != dev {
		t.Fatalf("devices %v", tr.Devices)
	}
	found := false
	for _, c := range tr.Cmdlines {
		found = found || strings.Contains(c, "--cuda-gpu-arch=ivcore10 src/k.cu")
	}
	if !found {
		t.Fatalf("compiler command line not seen: %v", tr.Cmdlines)
	}
	sleepSeen := false
	for _, e := range tr.Exes {
		sleepSeen = sleepSeen || strings.HasSuffix(e, "/sleep")
	}
	if !sleepSeen || tr.Procs < 3 {
		t.Fatalf("exes %v procs %d", tr.Exes, tr.Procs)
	}
}

// Nothing outside the tree is recorded, and a tree that opens no device has none.
func TestProcWatchIsBoundToItsTree(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc")
	}
	dir := t.TempDir()
	outside := exec.Command("bash", "-c", "exec 3</dev/null; sleep 0.6 # outsider-marker")
	outside.Start()
	defer outside.Wait()
	pidFile := filepath.Join(dir, "pid")
	stop := watchProcTree(pidFile)
	exec.Command("bash", "-c", watchedCommand(pidFile, "sleep 0.3")).Run()
	tr := stop()
	for _, c := range tr.Cmdlines {
		if strings.Contains(c, "outsider-marker") {
			t.Fatal("a process outside the tree was recorded")
		}
	}
	if len(tr.Devices) != 0 {
		t.Fatalf("devices %v", tr.Devices)
	}
	if !gpuDeviceRe().MatchString("/dev/iluvatar3") || gpuDeviceRe().MatchString("/dev/iluvatarctl") || gpuDeviceRe().MatchString("/dev/nvidiactl") {
		t.Fatal("device pattern from gpuVendors")
	}
}
