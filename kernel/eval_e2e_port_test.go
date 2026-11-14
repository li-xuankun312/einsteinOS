package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The end-to-end run is the port only when it launches, for every unit that ported kernels,
// one of them. 11/09 and 11/10 e2e.sh ran build/generate_model, built from the integration
// unit's own src/model/generate_model.cu (13 kernels of its own), and the units' kernels
// were never in the run whose rates were judged.
func TestTheEndToEndRunMustBeThePort(t *testing.T) {
	bootKernel() // the run is forked through the kernel's process table
	ws := t.TempDir()
	put := func(f, body string) {
		p := filepath.Join(ws, filepath.FromSlash(f))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o755)
	}
	// unit 14 ported ple.cu's kernels; unit 15 ported the sampler and only forward-declared
	// the prefill kernels in a .cuh (11/14 kernels_api.cuh: 23 declarations, no body)
	put(".einsteinOS/port/u41a85163f4.tsv", "src/kernels/cuda/ple.cu\tconv_kernel\tsrc/kernels/cuda/ple.cu\tconv_kernel\n"+
		"src/kernels/cuda/ple.cu\tgnorm_kernel\tsrc/kernels/cuda/ple.cu\tgnorm_kernel\n")
	put("src/kernels/cuda/ple.cu", "__global__ void conv_kernel(float* x) { x[0] = 1; }\n__global__ void __launch_bounds__(256) gnorm_kernel(float* x) { x[0] = 2; }\n")
	put(".einsteinOS/port/uaa158d17c5.tsv", "src/kernels/cuda/sampler.cu\tsampler_kernel\tsrc/kernels/cuda/sampler.cu\tsampler_kernel\n"+
		"src/prefill/kernels.cu\tgr_norm_kernel\tsrc/prefill/kernels_api.cuh\tgr_norm_kernel\n")
	put("src/kernels/cuda/sampler.cu", "__global__ void sampler_kernel(int* t) { t[0] = 0; }\n")
	put("src/prefill/kernels_api.cuh", "__global__ void gr_norm_kernel(const float* x, float* out, float eps);\n")
	// the integration unit's own program is not the units' port
	put(".einsteinOS/port/uintegration6801c77ff6f7.tsv", "src/program/generate.cpp\tmain\tsrc/model/generate_model.cu\trms_kernel\n")
	put("src/model/generate_model.cu", "__global__ void rms_kernel(float* x) { x[0] = 3; }\n")

	per := e2ePortKernels(ws, "uintegration6801c77ff6f7")
	if strings.Join(per["u41a85163f4"], " ") != "conv_kernel gnorm_kernel" || strings.Join(per["uaa158d17c5"], " ") != "sampler_kernel" || len(per) != 2 {
		t.Fatalf("ported kernels: %v", per)
	}

	put(".einsteinOS/verify/uintegration6801c77ff6f7/e2e.sh", "echo 'prefill: 57.54 tok/s'; echo 'decode: 11.20 tok/s'\n")
	oldLib, oldLaunches := e2eProbeLibFn, e2eLaunchesFn
	defer func() { e2eProbeLibFn, e2eLaunchesFn = oldLib, oldLaunches }()
	e2eProbeLibFn = func() (string, error) { return "/dev/null", nil }
	launched := []string{"rms_kernel"}
	e2eLaunchesFn = func(string) ([]string, bool) { return launched, true }
	run := func() (string, string) {
		br := evalRunE2E(ws, "uintegration6801c77ff6f7")
		if k, txt := e2eGate(br); k != "" {
			t.Fatalf("the run itself: %q %s", k, txt)
		}
		return e2ePortGate(br, e2ePortKernels(ws, "uintegration6801c77ff6f7"))
	}
	if k, txt := run(); k != "integration_e2e_not_port" || !strings.Contains(txt, "u41a85163f4: conv_kernel gnorm_kernel") || !strings.Contains(txt, "uaa158d17c5: sampler_kernel") || !strings.Contains(txt, "rms_kernel") {
		t.Fatalf("its own program: %q %s", k, txt)
	}
	launched = []string{"conv_kernel", "rms_kernel"}
	if k, txt := run(); k != "integration_e2e_not_port" || strings.Contains(txt, "u41a85163f4") || !strings.Contains(txt, "uaa158d17c5") {
		t.Fatalf("one unit unused: %q %s", k, txt)
	}
	launched = []string{"conv_kernel", "sampler_kernel", "rms_kernel"}
	if k, txt := run(); k != "" {
		t.Fatalf("every unit's kernels in the run: %q %s", k, txt)
	}
	e2eLaunchesFn = func(string) ([]string, bool) { return nil, false }
	if k, _ := run(); k != "integration_e2e_port_unmeasured" {
		t.Fatalf("no process loaded the probe: %q", k)
	}
	e2eProbeLibFn = func() (string, error) { return "", os.ErrNotExist }
	if k, _ := run(); k != "integration_e2e_port_unmeasured" {
		t.Fatalf("no probe: %q", k)
	}
}
