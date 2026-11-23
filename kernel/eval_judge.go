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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
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

// judgeWheelDir: where a file the judge fetches (a transformers wheel, PyPI's transformers
// index) may be put by hand when this machine cannot fetch it — read instead of the network,
// and given to pip as --find-links. 11/22 16:58:29 transformers-4.57.6's wheel ended in EOF
// through the proxy, and the judge had no other way to it.
func judgeWheelDir() string { return filepath.Join(judgeHome(), "wheels") }

// judgeLocalName: the name a fetched URL is kept under in judgeWheelDir.
func judgeLocalName(u string) string {
	p := strings.TrimSuffix(u, "/")
	if base := path.Base(p); base != "json" {
		return base
	}
	return path.Base(path.Dir(p)) + ".json"
}

// %[1]v the error, %[2]s where the file may be put
const judgeGetFailedFmt = "%[1]v — the file put at %[2]s is read instead"

// judgeGet: u from judgeWheelDir when it is there, else GET u directly, then through the
// machine's proxy (ref_proxy.go).
func judgeGet(u string) ([]byte, error) {
	local := filepath.Join(judgeWheelDir(), judgeLocalName(u))
	if b, err := os.ReadFile(local); err == nil {
		return b, nil
	}
	b, err := judgeFetch(u)
	if err != nil {
		return nil, fmt.Errorf(judgeGetFailedFmt, err, local)
	}
	return b, nil
}

// judgeFetch: GET u directly, then through the machine's proxy.
func judgeFetch(u string) ([]byte, error) {
	var errs []string
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
			errs = append(errs, fmt.Sprintf("%s: %v", refRouteName(proxy), err))
			continue
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if err != nil || resp.StatusCode != http.StatusOK {
			errs = append(errs, fmt.Sprintf("%s: %s: HTTP %d %v", refRouteName(proxy), u, resp.StatusCode, err))
			continue
		}
		return b, nil
	}
	// every route's failure: 11/22 16:58 and 19:14 only the proxy's was given, and the direct
	// route — which curl took in 2 s at 22:3x — failed unseen
	return nil, errors.New(strings.Join(errs, "; "))
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

// judgeHome: where judge environments are kept (a variable for tests).
var judgeHome = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".einsteinOS", "judge")
}

// judgeTorchIndex: CPU builds of torch — the judge runs on the CPU, beside the GPU stack.
var judgeTorchIndex = "https://download.pytorch.org/whl/cpu"

// judgeRunFn runs one step of building or checking a judge environment with env (a variable
// for tests).
var judgeRunFn = func(env []string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), evalBuildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	setProcGroup(cmd)
	cmd.Cancel = func() error { killProcGroup(cmd); return nil }
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		err = fmt.Errorf("killed after %v", evalBuildTimeout)
	}
	return string(out), err
}

// envWithoutProxy: env with the proxy variables removed, so a step goes directly.
func envWithoutProxy(env []string) []string {
	var out []string
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		switch strings.ToUpper(k) {
		case "ALL_PROXY", "HTTP_PROXY", "HTTPS_PROXY":
			continue
		}
		out = append(out, e)
	}
	return out
}

// judgePipEnv: env without this machine's pip settings — its pip.conf (PIP_CONFIG_FILE is
// /dev/null) and every PIP_ variable — so the judge's pip takes the index its arguments name.
// 11/23 this machine's pip read the Tsinghua mirror through the SOCKS proxy, which gave 32750
// bytes in 3 minutes, while pypi.org and files.pythonhosted.org answered directly in seconds.
func judgePipEnv(env []string) []string {
	var out []string
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(strings.ToUpper(k), "PIP_") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "PIP_CONFIG_FILE="+os.DevNull)
}

// judgeStep runs one network step directly, then with the machine's proxy in its environment,
// never with this machine's pip settings.
func judgeStep(name string, args ...string) error {
	envs := [][]string{judgePipEnv(envWithoutProxy(os.Environ()))}
	if refProxy() != "" {
		envs = append(envs, judgePipEnv(os.Environ()))
	}
	var errs []string
	for i, env := range envs {
		out, err := judgeRunFn(env, name, args...)
		if err == nil {
			return nil
		}
		route := "directly"
		if i > 0 {
			route = "with the proxy"
		}
		errs = append(errs, fmt.Sprintf("%s: %v: %s", route, err, tailText(out, 400)))
	}
	return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), strings.Join(errs, "; "))
}

// judgeCheckScript prints the versions it runs with; exit 4 when transformers has no such class.
const judgeCheckScript = `import sys, transformers, torch
print("transformers %s torch %s" % (transformers.__version__, torch.__version__))
sys.exit(0 if hasattr(transformers, sys.argv[1]) else 4)`

// buildJudgeEnv: a venv of the judge's own under judgeHome — never the system python's
// packages, where the GPU stack's torch (2.1 on cc-1c97a71e) has to stay as it is — with CPU
// torch at least spec.Torch and transformers spec.Transformers; built once, reused while it
// still loads arch. It returns the venv's python and what it runs with.
func buildJudgeEnv(spec judgeSpec, arch string) (python, versions string, err error) {
	dir := filepath.Join(judgeHome(), fmt.Sprintf("transformers-%s-torch-%s-py%s", spec.Transformers, spec.Torch, spec.Python))
	python = filepath.Join(dir, "bin", "python")
	marker := filepath.Join(dir, "einsteinos_judge.json")
	check := func() (string, error) {
		out, err := judgeRunFn(os.Environ(), python, "-c", judgeCheckScript, arch)
		lines := strings.Split(strings.TrimSpace(out), "\n")
		return lines[len(lines)-1], err
	}
	if fileExists(marker) {
		if v, err := check(); err == nil {
			return python, v, nil
		}
	}
	os.MkdirAll(judgeHome(), 0o755)
	if out, err := judgeRunFn(os.Environ(), "python3", "-m", "venv", "--clear", dir); err != nil {
		return "", "", fmt.Errorf("python3 -m venv: %v: %s", err, tailText(out, 400))
	}
	torch := "torch"
	if spec.Torch != "" {
		torch = "torch>=" + spec.Torch
	}
	// pysocks: pip reaches a socks proxy (the machine's is socks5, setup_proxy.sh) only with it
	// what was put in judgeWheelDir by hand is found there first
	links := []string{}
	if ents, err := os.ReadDir(judgeWheelDir()); err == nil && len(ents) > 0 {
		links = []string{"--find-links", judgeWheelDir()}
	}
	pip := func(args ...string) error {
		return judgeStep(python, append(append([]string{"-m", "pip", "install", "-q"}, links...), args...)...)
	}
	// the index each step takes is named, PyPI's own (judgePyPI) where it is not torch's
	pypi := judgePyPI + "/simple"
	if err := pip("--index-url", pypi, "--upgrade", "pip", "pysocks"); err != nil {
		return "", "", err
	}
	if err := pip("--index-url", judgeTorchIndex, torch); err != nil {
		return "", "", err
	}
	if err := pip("--index-url", pypi, "transformers=="+spec.Transformers, "safetensors", "accelerate"); err != nil {
		return "", "", err
	}
	v, err := check()
	if err != nil {
		return "", "", fmt.Errorf("the judge environment does not load %s: %v: %s", arch, err, v)
	}
	b, _ := json.MarshalIndent(map[string]any{"spec": spec, "arch": arch, "runs_with": v}, "", "  ")
	os.WriteFile(marker, b, 0o644)
	return python, v, nil
}

// judgeSpecFor: measureJudgeSpec, kept beside the environments — the next judge of the same
// model type on the same python reads it instead of downloading the wheels again (five, 22 s
// against PyPI on 11/20).
func judgeSpecFor(mt, python string) (judgeSpec, error) {
	path := filepath.Join(judgeHome(), fmt.Sprintf("spec-%s-py%s.json", mt, python))
	if b, err := os.ReadFile(path); err == nil {
		var s judgeSpec
		if json.Unmarshal(b, &s) == nil && s.Transformers != "" {
			return s, nil
		}
	}
	s, err := measureJudgeSpec(mt, python)
	if err != nil {
		return s, err
	}
	os.MkdirAll(judgeHome(), 0o755)
	b, _ := json.MarshalIndent(s, "", "  ")
	os.WriteFile(path, b, 0o644)
	return s, nil
}
