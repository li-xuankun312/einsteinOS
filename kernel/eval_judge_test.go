package kernel

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeWheel: a transformers wheel holding the given model directories and import_utils.py.
func fakeWheel(t *testing.T, models []string, torchMin string) []byte {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, m := range models {
		w, _ := z.Create("transformers/models/" + m + "/modeling_" + m + ".py")
		w.Write([]byte("# " + m))
	}
	w, _ := z.Create("transformers/utils/import_utils.py")
	if torchMin != "" {
		w.Write([]byte(`logger.warning_once(f"Disabling PyTorch because PyTorch >= ` + torchMin + ` is required but found {torch_version}")`))
	}
	z.Close()
	return b.Bytes()
}

// fakePyPI serves /pypi/transformers/json and the wheels it names; it counts the downloads.
func fakePyPI(t *testing.T, rel map[string]struct {
	models   []string
	reqPy    string
	torchMin string
}) (*httptest.Server, *int) {
	downloads := 0
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/pypi/transformers/json", func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{"releases": map[string]any{}}
		for v, x := range rel {
			out["releases"].(map[string]any)[v] = []map[string]any{{
				"filename": "transformers-" + v + "-py3-none-any.whl", "requires_python": x.reqPy,
				"url": srv.URL + "/files/" + v + ".whl"}}
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) {
		v := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/files/"), ".whl")
		downloads++
		w.Write(fakeWheel(t, rel[v].models, rel[v].torchMin))
	})
	srv = httptest.NewServer(mux)
	return srv, &downloads
}

// The transformers a model's judge needs is measured, not assumed: the first release whose
// wheel ships models/<model_type>/, and from it on the newest whose Requires-Python this
// machine's python3 meets, with the torch it requires. 11/20 on cc-1c97a71e python3 is 3.10
// with the GPU stack's torch 2.1 and transformers 4.51.3, which has no Qwen3_5MoeForCausalLM:
// qwen3_5_moe first ships in 5.2.0 (Requires-Python >=3.10.0, PyTorch >= 2.4), the newest,
// 5.23.0, requires Python >=3.11.0.
func TestTheJudgesTransformersIsMeasuredFromTheReleases(t *testing.T) {
	type r = struct {
		models   []string
		reqPy    string
		torchMin string
	}
	srv, downloads := fakePyPI(t, map[string]r{
		"4.51.3": {[]string{"qwen3_moe"}, ">=3.9.0", "2.1"},
		"4.57.6": {[]string{"qwen3_moe"}, ">=3.9.0", "2.2"},
		"5.1.0":  {[]string{"qwen3_moe"}, ">=3.10.0", "2.4"},
		"5.2.0":  {[]string{"qwen3_moe", "qwen3_5_moe"}, ">=3.10.0", "2.4"},
		"5.4.0":  {[]string{"qwen3_moe", "qwen3_5_moe"}, ">=3.10.0", "2.4"},
		"5.23.0": {[]string{"qwen3_moe", "qwen3_5_moe"}, ">=3.11.0", "2.4"},
	})
	defer srv.Close()
	old := judgePyPI
	judgePyPI = srv.URL
	defer func() { judgePyPI = old }()

	spec, err := measureJudgeSpec("qwen3_5_moe", "3.10.12")
	if err != nil {
		t.Fatal(err)
	}
	if spec.FirstWithModel != "5.2.0" || spec.Transformers != "5.4.0" || spec.Torch != "2.4" {
		t.Fatalf("%+v: want first 5.2.0, the newest for python 3.10 (5.4.0), torch >= 2.4", spec)
	}
	if *downloads > 5 {
		t.Fatalf("%d wheels downloaded for six releases", *downloads)
	}
	if _, err := measureJudgeSpec("qwen9_moe", "3.10.12"); err == nil || !strings.Contains(err.Error(), "qwen9_moe") {
		t.Fatalf("a model no release ships: %v", err)
	}
	if s, err := measureJudgeSpec("qwen3_5_moe", "3.8.10"); err == nil {
		t.Fatalf("python 3.8 meets none of them, got %+v", s)
	}
}

// What config.json says the model is: model_type at its top.
func TestTheModelTypeIsReadFromConfig(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "config.json"), []byte(`{"architectures":["Qwen3_5MoeForCausalLM"],"model_type":"qwen3_5_moe","text_config":{"model_type":"qwen3_5_moe_text"}}`), 0o644)
	if m := modelType(d); m != "qwen3_5_moe" {
		t.Fatalf("%q", m)
	}
}

// The judge's environment is a venv of its own under the kernel's state — never the system
// python's packages, where the GPU stack's torch (2.1 on cc-1c97a71e) has to stay as it is —
// with CPU torch at least the version measured and that transformers; each install goes
// directly, then with the machine's proxy; built once, the next judge reuses it.
func TestTheJudgeEnvironmentIsBuiltApartAndReused(t *testing.T) {
	home := t.TempDir()
	oldHome, oldRun := judgeHome, judgeRunFn
	judgeHome = func() string { return home }
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1080")
	defer func() { judgeHome, judgeRunFn = oldHome, oldRun }()
	var calls []string
	torchTries := 0
	judgeRunFn = func(env []string, name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		proxied := false
		for _, e := range env {
			if strings.HasPrefix(strings.ToUpper(e), "ALL_PROXY=") {
				proxied = true
			}
		}
		calls = append(calls, fmt.Sprintf("%v %s", proxied, line))
		switch {
		case strings.Contains(line, "-m venv"):
			os.MkdirAll(filepath.Join(args[len(args)-1], "bin"), 0o755)
		case strings.Contains(line, "whl/cpu"):
			torchTries++
			if !proxied {
				return "ERROR: Could not find a version that satisfies the requirement torch>=2.6", fmt.Errorf("exit status 1")
			}
		case strings.Contains(line, "transformers.__version__"):
			return "transformers 5.20.2 torch 2.6.0+cpu", nil
		}
		return "", nil
	}
	spec := judgeSpec{ModelType: "qwen3_5_moe", FirstWithModel: "5.2.0", Transformers: "5.20.2", RequiresPython: ">=3.10.0", Torch: "2.6", Python: "3.10.12"}
	py, versions, err := buildJudgeEnv(spec, "Qwen3_5MoeForCausalLM")
	if err != nil {
		t.Fatal(err, calls)
	}
	if !strings.HasPrefix(py, home) || versions != "transformers 5.20.2 torch 2.6.0+cpu" {
		t.Fatalf("%s %q", py, versions)
	}
	all := strings.Join(calls, "\n")
	for _, want := range []string{"python3 -m venv", "torch>=2.6", "transformers==5.20.2", "safetensors", "accelerate"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in\n%s", want, all)
		}
	}
	var torchCalls []string
	for _, c := range calls {
		if strings.Contains(c, "whl/cpu") {
			torchCalls = append(torchCalls, c)
		}
	}
	if torchTries != 2 || !strings.HasPrefix(torchCalls[0], "false") || !strings.HasPrefix(torchCalls[1], "true") {
		t.Fatalf("torch: directly, then with the proxy:\n%s", all)
	}
	calls = nil
	if _, _, err := buildJudgeEnv(spec, "Qwen3_5MoeForCausalLM"); err != nil || len(calls) != 1 || !strings.Contains(calls[0], "transformers.__version__") {
		t.Fatalf("built once, then reused: %v\n%s", err, strings.Join(calls, "\n"))
	}
}

// The measurement is kept beside the environments: the next judge of the same model type on
// the same python reads it instead of downloading the wheels again (five for 22 s against PyPI
// on 11/20).
func TestTheJudgeSpecIsMeasuredOncePerModelTypeAndPython(t *testing.T) {
	type r = struct {
		models   []string
		reqPy    string
		torchMin string
	}
	srv, downloads := fakePyPI(t, map[string]r{
		"5.1.0": {[]string{"qwen3_moe"}, ">=3.10.0", "2.4"},
		"5.2.0": {[]string{"qwen3_5_moe"}, ">=3.10.0", "2.4"},
	})
	defer srv.Close()
	oldP, oldH := judgePyPI, judgeHome
	home := t.TempDir()
	judgePyPI, judgeHome = srv.URL, func() string { return home }
	defer func() { judgePyPI, judgeHome = oldP, oldH }()
	a, err := judgeSpecFor("qwen3_5_moe", "3.10.12")
	first := *downloads
	b, err2 := judgeSpecFor("qwen3_5_moe", "3.10.12")
	if err != nil || err2 != nil || a != b || a.Transformers != "5.2.0" || *downloads != first {
		t.Fatalf("%+v %+v %v %v downloads %d then %d", a, b, err, err2, first, *downloads)
	}
}

// 11/22 16:31-16:58 the judge's environment took 27 minutes to fail, and the next
// verification began the same: a failure is not tried again for judgeFailHold
func TestAJudgeEnvironmentThatFailedIsNotTriedAgainForAWhile(t *testing.T) {
	key := "/m|XForCausalLM"
	t0 := time.Date(2026, 11, 22, 16, 31, 53, 0, time.Local)
	if _, ok := judgeFailedRecently(key, t0); ok {
		t.Fatal("a failure before any attempt")
	}
	judgeNoteFailed(key, fmt.Errorf("through socks5h://127.0.0.1:1080: EOF"), t0, t0.Add(27*time.Minute))
	err, ok := judgeFailedRecently(key, t0.Add(30*time.Minute))
	if !ok || !strings.Contains(err.Error(), "EOF (the attempt at 16:31:53, 27m0s, failed so; not tried again before 17:31:53)") {
		t.Fatalf("%v %v", ok, err)
	}
	if _, ok := judgeFailedRecently(key, t0.Add(61*time.Minute)); ok {
		t.Fatal("still held after judgeFailHold")
	}
}

func TestAFileTheJudgeCannotFetchIsReadFromItsWheelDirectory(t *testing.T) {
	home := t.TempDir()
	old := judgeHome
	judgeHome = func() string { return home }
	defer func() { judgeHome = old }()
	if n := judgeLocalName("https://files.pythonhosted.org/packages/03/b8/x/transformers-4.57.6-py3-none-any.whl"); n != "transformers-4.57.6-py3-none-any.whl" {
		t.Fatal(n)
	}
	if n := judgeLocalName("https://pypi.org/pypi/transformers/json"); n != "transformers.json" {
		t.Fatal(n)
	}
	u := "http://127.0.0.1:1/packages/transformers-9.9.9-py3-none-any.whl"
	_, err := judgeGet(u)
	if err == nil || !strings.Contains(err.Error(), "the file put at "+filepath.Join(home, "wheels", "transformers-9.9.9-py3-none-any.whl")+" is read instead") {
		t.Fatalf("the failure does not say where the file may be put: %v", err)
	}
	os.MkdirAll(filepath.Join(home, "wheels"), 0o755)
	os.WriteFile(filepath.Join(home, "wheels", "transformers-9.9.9-py3-none-any.whl"), []byte("PK"), 0o644)
	if b, err := judgeGet(u); err != nil || string(b) != "PK" {
		t.Fatalf("the file put there was not read: %q %v", b, err)
	}
}

// 11/22 the judge's fetch said only why the proxy failed; the direct route had failed first
func TestAFetchThatFailsSaysWhyOnEveryRoute(t *testing.T) {
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1")
	_, err := judgeFetch("http://127.0.0.1:1/x.whl")
	if err == nil || !strings.Contains(err.Error(), "directly: ") || !strings.Contains(err.Error(), "through socks5h://127.0.0.1:1: ") {
		t.Fatalf("%v", err)
	}
}
