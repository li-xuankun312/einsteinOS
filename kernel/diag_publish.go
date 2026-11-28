package kernel

// diag_publish.go — an abnormal exit's record, saved here and pushed from a clone of its own
// (see diag.go for why not the state branch's clone).

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func diagFileName(rec DiagRecord) string {
	conv := rec.ConvID
	if len(conv) > 8 {
		conv = conv[:8]
	}
	ts := strings.NewReplacer("-", "", ":", "", "T", "-", "Z", "").Replace(rec.Time)
	if len(ts) > 15 {
		ts = ts[:15]
	}
	return ts + "-" + conv + ".json"
}

// SaveDiagLocal writes the redacted record under the temp directory, so an exit's record
// is not lost with the network. Returns the path.
func SaveDiagLocal(branch string, rec DiagRecord) (string, error) {
	b, err := json.MarshalIndent(rec.Redacted(), "", " ")
	if err != nil {
		return "", err
	}
	dir := filepath.Join(os.TempDir(), "einsteinos_diag")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, strings.ReplaceAll(branch, "/", "_")+".json")
	return path, os.WriteFile(path, append(b, '\n'), 0o600)
}

// diagPush is GitPushRetry; a variable for tests.
var diagPush = GitPushRetry

// diagGit runs git in dir and fails on any non-zero exit (runLocalGit does not).
func diagGit(dir string, args ...string) (string, error) {
	c := exec.Command("git", append([]string{"-c", "user.name=einsteinOS", "-c", "user.email=einsteinOS@kernel"}, args...)...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, truncStr(strings.TrimSpace(string(out)), 200))
	}
	return string(out), nil
}

// PublishDiag keeps rec on a branch of ownerRepo named branch: one commit with no parent,
// holding .einsteinOS/diag/<time>-<conv>.json, made in a clone of its own. The record is
// saved on this machine first; an error says where.
func PublishDiag(ownerRepo, branch string, rec DiagRecord) error {
	local, lerr := SaveDiagLocal(branch, rec)
	if lerr != nil {
		local = "(not saved: " + lerr.Error() + ")"
	}
	if ownerRepo == "" {
		return fmt.Errorf("no repository to push to; the record is at %s", local)
	}
	dir, err := os.MkdirTemp("", "einsteinos_diag_clone_")
	if err != nil {
		return fmt.Errorf("%v; the record is at %s", err, local)
	}
	defer os.RemoveAll(dir)
	rec = rec.Redacted()
	b, err := json.MarshalIndent(rec, "", " ")
	if err != nil {
		return fmt.Errorf("%v; the record is at %s", err, local)
	}
	rel := filepath.Join(".einsteinOS", "diag", diagFileName(rec))
	steps := []func() error{
		func() error { _, e := diagGit(dir, "init", "-q"); return e },
		func() error { _, e := diagGit(dir, "symbolic-ref", "HEAD", "refs/heads/"+branch); return e },
		func() error {
			if e := os.MkdirAll(filepath.Join(dir, filepath.Dir(rel)), 0o755); e != nil {
				return e
			}
			return os.WriteFile(filepath.Join(dir, rel), append(b, '\n'), 0o644)
		},
		func() error { _, e := diagGit(dir, "add", "--", rel); return e },
		func() error {
			_, e := diagGit(dir, "commit", "-q", "-m", fmt.Sprintf("diag: %s %s conv %s", rec.Role, rec.Exit, truncStr(rec.ConvID, 8)))
			return e
		},
	}
	for _, step := range steps {
		if e := step(); e != nil {
			return fmt.Errorf("%v; the record is at %s", e, local)
		}
	}
	if err := diagPush(dir, ownerRepo, branch, 3); err != nil {
		return fmt.Errorf("%v; the record is at %s", err, local)
	}
	log.Printf("diag: %s:%s pushed (%s)", ownerRepo, branch, filepath.Base(rel))
	return nil
}
