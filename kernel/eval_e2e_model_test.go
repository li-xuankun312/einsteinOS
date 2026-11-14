package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The end-to-end run loads its weights from the model the kernel gave it and only from there.
// 11/10 /tmp/model did not exist and e2e.sh picked a checkpoint itself ("e2e: /tmp/model has no
// weights; using /root/public-storage/models/Qwen/Qwen3.6-35B-A3B/").
func TestTheEndToEndRunLoadsTheModelItIsGiven(t *testing.T) {
	bootKernel() // the run is forked through the kernel's process table
	given, other := t.TempDir(), t.TempDir()
	for _, d := range []string{given, other} {
		os.WriteFile(filepath.Join(d, "model-00001-of-00026.safetensors"), []byte("w"), 0o644)
	}
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, ".einsteinOS/verify/u"), 0o755)
	put := func(open string) {
		os.WriteFile(filepath.Join(ws, ".einsteinOS/verify/u/e2e.sh"), []byte(open+"echo 'prefill: 57.54 tok/s'; echo 'decode: 11.20 tok/s'\n"), 0o755)
	}
	oldModel, oldLib := evalModelDir, e2eProbeLibFn
	defer func() { evalModelDir, e2eProbeLibFn = oldModel, oldLib }()
	evalModelDir = given
	e2eProbeLibFn = func() (string, error) { return "", os.ErrNotExist }

	put(`exec 3< "` + filepath.Join(other, "model-00001-of-00026.safetensors") + `"; sleep 0.4; `)
	if k, txt := e2eModelGate(evalRunE2E(ws, "u"), given); k != "integration_e2e_other_model" || !strings.Contains(txt, other) {
		t.Fatalf("weights from another directory: %q %s", k, txt)
	}
	put("")
	if k, txt := e2eModelGate(evalRunE2E(ws, "u"), given); k != "integration_e2e_other_model" || !strings.Contains(txt, "opened none") {
		t.Fatalf("no weights at all: %q %s", k, txt)
	}
	put(`exec 3< "$MODEL_DIR/model-00001-of-00026.safetensors"; sleep 0.4; `)
	if k, txt := e2eModelGate(evalRunE2E(ws, "u"), given); k != "" {
		t.Fatalf("the given model: %q %s", k, txt)
	}
}
