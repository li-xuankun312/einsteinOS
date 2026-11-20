package kernel

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
