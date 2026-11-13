package kernel

import "testing"

// Every definition of a reference source is a name a port line may give as its counterpart,
// also the ones another unit ports; masked as the conversation sees them.
func TestTheReferenceFilesNamesAreMeasured(t *testing.T) {
	old := refGitFn
	defer func() { refGitFn = old }()
	refGitFn = func(dir string, args ...string) (string, error) {
		if len(args) == 2 && args[0] == "show" && args[1] == "abc:src/kernels/cuda/router_top10.cu" {
			return "static const int RT_MAX_THREADS = 512;\n__global__ void router_top10_kernel(const float* l, int* o) {}\nvoid strata_router(int n) {}\n", nil
		}
		return "", nil
	}
	req := UnitRequirements{Sources: []string{"src/kernels/cuda/router_top10.cu"}, FlagFiles: map[string][]string{}}
	finishRequirements("bare", "abc", &req)
	names := req.FileNames["src/kernels/cuda/router_top10.cu"]
	if !names["RT_MAX_THREADS"] || !names["router_top10_kernel"] || names["u776_router_probe_kernel"] {
		t.Fatalf("%v", req.FileNames)
	}
	m := maskRequirements(req, newRefMask(refMaskIdent, "Niko1221/Strata"))
	if mn := m.FileNames["src/kernels/cuda/router_top10.cu"]; !mn["router_top10_kernel"] || mn["strata_router"] {
		t.Fatalf("masked %v", m.FileNames)
	}
}
