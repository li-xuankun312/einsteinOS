package kernel

// eval_size.go — a searched repository is measured before it is cloned.
//
// 11/10 16:46–22:45 the evaluation search named llvm/llvm-project, NVIDIA/cutlass, ROCm/HIP and
// Deep-Spark/DeepSparkHub; every one was cloned (blobless) until the clone was killed, the
// llvm-project clone six times. GitHub reports a repository's size: one that is larger than
// evalMaxRepoKiB is not taken, without a clone.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// evalMaxRepoKiB: the largest repository the search clones (GitHub's "size", KiB).
const evalMaxRepoKiB = 200 * 1024

// evalSizeFn: GitHub's size of owner/name in KiB (a variable for tests).
var evalSizeFn = func(owner, name string) (int64, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, name), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok, err := os.ReadFile("/tmp/.gh_token"); err == nil && strings.TrimSpace(string(tok)) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("GitHub answered %d", resp.StatusCode)
	}
	var r struct {
		Size int64 `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return 0, err
	}
	return r.Size, nil
}

// evalTooLarge: why owner/name is not cloned, or "" when it may be. A size that cannot be read
// is a measurement that did not happen, and the repository is not cloned on it: 11/21
// 07:06:41 ggerganov/llama.cpp (487 MiB by GitHub's size on 11/14) was cloned and taken, its
// size not having been read, and nothing said why.
func evalTooLarge(owner, name string) string {
	kib, err := evalSizeFn(owner, name)
	switch {
	case err != nil:
		return fmt.Sprintf(evalSizeUnreadFmt, err)
	case kib > evalMaxRepoKiB:
		return fmt.Sprintf("too large to clone here (GitHub size %d MiB, limit %d MiB)", kib/1024, evalMaxRepoKiB/1024)
	}
	return ""
}

// %[1]v why GitHub's size could not be read
const evalSizeUnreadFmt = "its size could not be read from GitHub (%[1]v), so it is not cloned"
