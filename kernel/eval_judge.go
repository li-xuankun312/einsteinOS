package kernel

// eval_judge.go — the judge of the integration's tokens (eval_correct.go) runs the class the
// model's config.json names; which transformers ships that class, and what it needs, is
// measured from the releases themselves.
//
// 11/20 on cc-1c97a71e python3 is 3.10 with the GPU stack's torch 2.1 and transformers 4.51.3,
// which has no Qwen3_5MoeForCausalLM: the judge 96a50570 runs in "python3" could not load the
// class the model names, whatever the port generated. qwen3_5_moe first ships in transformers
// 5.2.0 (Requires-Python >=3.10.0; it disables PyTorch below 2.4), the newest release, 5.23.0,
// requires Python >=3.11.0.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// judgePyPI: where the releases are read (a variable for tests).
var judgePyPI = "https://pypi.org"

// judgeHTTPTimeout bounds one request; a wheel is ~10 MB.
var judgeHTTPTimeout = 5 * time.Minute

// judgeSpec: what the judge needs, measured.
type judgeSpec struct {
	ModelType      string `json:"model_type"`
	FirstWithModel string `json:"first_with_model"` // the first release whose wheel ships models/<model_type>/
	Transformers   string `json:"transformers"`     // the newest release from it on this python meets
	RequiresPython string `json:"requires_python"`
	Torch          string `json:"torch"` // "PyTorch >= X is required" in its import_utils.py; "" when it says none
	Python         string `json:"python"`
}

// modelType: the model_type at the top of the model directory's config.json.
func modelType(modelDir string) string {
	b, err := os.ReadFile(modelDir + "/config.json")
	if err != nil {
		return ""
	}
	var c struct {
		ModelType string `json:"model_type"`
	}
	if json.Unmarshal(b, &c) != nil {
		return ""
	}
	return c.ModelType
}

// judgeGet: GET u directly, then through the machine's proxy (ref_proxy.go).
func judgeGet(u string) ([]byte, error) {
	var lastErr error
	for _, proxy := range refFetchRoutes() {
		tr := &http.Transport{}
		if proxy != "" {
			if pu, err := url.Parse(proxy); err == nil {
				tr.Proxy = http.ProxyURL(pu)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), judgeHTTPTimeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		resp, err := (&http.Client{Transport: tr}).Do(req)
		if err != nil {
			cancel()
			lastErr = fmt.Errorf("%s: %v", refRouteName(proxy), err)
			continue
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if err != nil || resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("%s: %s: HTTP %d %v", refRouteName(proxy), u, resp.StatusCode, err)
			continue
		}
		return b, nil
	}
	return nil, lastErr
}

type judgeRelease struct {
	version, url, requiresPython string
	parts                        []int
}

var stableVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

func versionParts(v string) []int {
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

func versionCmp(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// pythonMeets: whether python version v meets a Requires-Python specifier (">=3.10.0",
// ">=3.9,<4", "!=3.0.*"); an empty one is met.
func pythonMeets(v, spec string) bool {
	pv := versionParts(v)
	for _, s := range strings.Split(spec, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		op := strings.TrimRight(s[:len(s)-len(strings.TrimLeft(s, "<>=!~"))], " ")
		want := strings.TrimSpace(strings.TrimLeft(s, "<>=!~"))
		if strings.HasSuffix(want, ".*") {
			prefix := versionParts(strings.TrimSuffix(want, ".*"))
			match := len(pv) >= len(prefix) && versionCmp(pv[:len(prefix)], prefix) == 0
			if (op == "==" && !match) || (op == "!=" && match) {
				return false
			}
			continue
		}
		c := versionCmp(pv, versionParts(want))
		ok := true
		switch op {
		case ">=", "~=":
			ok = c >= 0
		case ">":
			ok = c > 0
		case "<=":
			ok = c <= 0
		case "<":
			ok = c < 0
		case "==":
			ok = c == 0
		case "!=":
			ok = c != 0
		}
		if !ok {
			return false
		}
	}
	return true
}

var torchRequiredRe = regexp.MustCompile(`PyTorch >= ([0-9][0-9.]*) is required`)

// measureJudgeSpec reads transformers' releases: the first whose wheel ships
// models/<mt>/ (releases ship a model from the one that adds it on), and from it on the newest
// whose Requires-Python python meets, with the torch its import_utils.py requires.
func measureJudgeSpec(mt, python string) (judgeSpec, error) {
	spec := judgeSpec{ModelType: mt, Python: python}
	b, err := judgeGet(judgePyPI + "/pypi/transformers/json")
	if err != nil {
		return spec, err
	}
	var idx struct {
		Releases map[string][]struct {
			Filename       string `json:"filename"`
			URL            string `json:"url"`
			RequiresPython string `json:"requires_python"`
		} `json:"releases"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		return spec, err
	}
	var rel []judgeRelease
	for v, files := range idx.Releases {
		if !stableVersionRe.MatchString(v) {
			continue
		}
		for _, f := range files {
			if strings.HasSuffix(f.Filename, ".whl") {
				rel = append(rel, judgeRelease{version: v, url: f.URL, requiresPython: f.RequiresPython, parts: versionParts(v)})
				break
			}
		}
	}
	if len(rel) == 0 {
		return spec, fmt.Errorf("no transformers release with a wheel at %s", judgePyPI)
	}
	sort.Slice(rel, func(i, j int) bool { return versionCmp(rel[i].parts, rel[j].parts) < 0 })
	wheels := map[string]*zip.Reader{}
	wheel := func(r judgeRelease) (*zip.Reader, error) {
		if z, ok := wheels[r.version]; ok {
			return z, nil
		}
		data, err := judgeGet(r.url)
		if err != nil {
			return nil, err
		}
		z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, fmt.Errorf("transformers %s wheel: %v", r.version, err)
		}
		wheels[r.version] = z
		return z, nil
	}
	ships := func(r judgeRelease) (bool, error) {
		z, err := wheel(r)
		if err != nil {
			return false, err
		}
		prefix := "transformers/models/" + mt + "/"
		for _, f := range z.File {
			if strings.HasPrefix(f.Name, prefix) {
				return true, nil
			}
		}
		return false, nil
	}
	newest := rel[len(rel)-1]
	if ok, err := ships(newest); err != nil {
		return spec, err
	} else if !ok {
		return spec, fmt.Errorf("no transformers release ships models/%s/ (the newest, %s, does not)", mt, newest.version)
	}
	lo, hi := 0, len(rel)-1
	for lo < hi {
		mid := (lo + hi) / 2
		ok, err := ships(rel[mid])
		if err != nil {
			return spec, err
		}
		if ok {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	spec.FirstWithModel = rel[lo].version
	for i := len(rel) - 1; i >= lo; i-- {
		if pythonMeets(python, rel[i].requiresPython) {
			spec.Transformers, spec.RequiresPython = rel[i].version, rel[i].requiresPython
			z, err := wheel(rel[i])
			if err != nil {
				return spec, err
			}
			for _, f := range z.File {
				if f.Name == "transformers/utils/import_utils.py" {
					if rc, err := f.Open(); err == nil {
						src, _ := io.ReadAll(rc)
						rc.Close()
						if m := torchRequiredRe.FindSubmatch(src); m != nil {
							spec.Torch = string(m[1])
						}
					}
				}
			}
			return spec, nil
		}
	}
	return spec, fmt.Errorf("transformers %s on ship models/%s/, and none of them accepts python %s (the first requires %q)", spec.FirstWithModel, mt, python, rel[lo].requiresPython)
}
