package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// A process that launches a kernel reports the device node it holds open right after the
// launch, so a test process too short-lived for the 50 ms /proc sampling still shows its
// device. 11/09 09:57 and 10:15 two GPU units were sent back gpu_unused ("devices opened []")
// while the probe recorded their kernels launching; the same code passed the next time.
// /dev/null stands for the device node here.
func TestTheLaunchingProcessReportsItsDevice(t *testing.T) {
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("no C compiler")
	}
	withProbeDir(t)
	oldRe := gpuDeviceRe
	gpuDeviceRe = func() *regexp.Regexp { return regexp.MustCompile(`^/dev/null$`) }
	defer func() { gpuDeviceRe = oldRe }()

	dir := t.TempDir()
	rt := "typedef struct { unsigned x, y, z; } dim3;\nint cudaLaunchKernel(const void* f, dim3 g, dim3 b, void** a, unsigned long s, void* st) { return 0; }\n"
	main := `#include <fcntl.h>
typedef struct { unsigned x, y, z; } dim3;
int cudaLaunchKernel(const void* f, dim3 g, dim3 b, void** a, unsigned long s, void* st);
void _Z14contract_kernelPf(float* p) { (void)p; }
int main(int argc, char** argv) {
  int fd = open("/dev/null", O_RDWR);
  dim3 g = {1, 1, 1};
  if (argc < 2) cudaLaunchKernel((const void*)&_Z14contract_kernelPf, g, g, 0, 0, 0);
  return fd < 0;
}`
	os.WriteFile(filepath.Join(dir, "rt.c"), []byte(rt), 0o644)
	os.WriteFile(filepath.Join(dir, "main.c"), []byte(main), 0o644)
	for _, args := range [][]string{
		{"-shared", "-fPIC", "-o", filepath.Join(dir, "libcudart.so"), filepath.Join(dir, "rt.c")},
		{"-o", filepath.Join(dir, "t"), filepath.Join(dir, "main.c"), "-L" + dir, "-lcudart", "-Wl,-rpath," + dir},
	} {
		if out, err := exec.Command("cc", args...).CombinedOutput(); err != nil {
			t.Fatalf("cc: %v\n%s", err, out)
		}
	}
	lib, err := launchProbeLib()
	if err != nil {
		t.Fatal(err)
	}
	run := func(arg string) probeLog {
		logPath := filepath.Join(t.TempDir(), "launch.log")
		if out, err := exec.Command("bash", "-c", launchProbeEnv(lib, logPath)+shq(filepath.Join(dir, "t"))+" "+arg).CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return readProbeLog(logPath)
	}
	pl := run("")
	if got := probeGPUDevices(nil, pl); len(got) != 1 || got[0] != "/dev/null" {
		t.Fatalf("devices after the launch: %v (log devices %v)", got, pl.Devices)
	}
	if got := probeGPUDevices(nil, run("nolaunch")); len(got) != 0 {
		t.Fatalf("a process that launched nothing reports nothing: %v", got)
	}
	if got := probeGPUDevices([]string{"/dev/null", "/dev/tty"}, pl); len(got) != 1 {
		t.Fatalf("merged with the sampled devices, once each: %v", got)
	}
}
