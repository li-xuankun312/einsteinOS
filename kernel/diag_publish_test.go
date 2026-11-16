package kernel

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func diagGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// the push goes to a local bare repository instead of GitHub
func withLocalRemote(t *testing.T) (bare string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	bare = filepath.Join(t.TempDir(), "remote.git")
	diagGitOut(t, t.TempDir(), "init", "-q", "--bare", bare)
	old := diagPush
	t.Cleanup(func() { diagPush = old })
	diagPush = func(dir, ownerRepo, branch string, maxRetry int) error {
		c := exec.Command("git", "push", "-q", bare, branch)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Logf("push: %s", out)
			return err
		}
		return nil
	}
	return bare
}

func TestPublishDiagPushesAnOrphanBranchWithOnlyTheRecord(t *testing.T) {
	bare := withLocalRemote(t)
	old := knownSecretValuesFn
	defer func() { knownSecretValuesFn = old }()
	knownSecretValuesFn = func() []string { return []string{"AXLE-SESSION-VALUE-123456"} }
	rec := sampleDiag("[Error] {\"type\":\"overloaded_error\"} AXLE-SESSION-VALUE-123456", "Continue now")
	branch := DiagBranchName()
	if err := PublishDiag("o/r", branch, rec); err != nil {
		t.Fatal(err)
	}
	files := strings.Fields(diagGitOut(t, bare, "ls-tree", "-r", "--name-only", branch))
	if len(files) != 1 || files[0] != ".einsteinOS/diag/20261116-120739-f1cac064.json" {
		t.Fatalf("files: %v", files)
	}
	if parents := strings.TrimSpace(diagGitOut(t, bare, "log", "-1", "--format=%P", branch)); parents != "" {
		t.Fatalf("not an orphan commit: parents %q", parents)
	}
	var got DiagRecord
	if err := json.Unmarshal([]byte(diagGitOut(t, bare, "show", branch+":"+files[0])), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "overloaded_error") || strings.Contains(got.Text, "AXLE-SESSION-VALUE-123456") ||
		got.ConvID != rec.ConvID || got.Request.Kind != "continue" || got.Request.Prompt != "Continue now" {
		t.Fatalf("%+v", got)
	}
}

func TestPublishDiagKeepsTheLocalRecordWhenThePushFails(t *testing.T) {
	withLocalRemote(t)
	diagPush = func(dir, ownerRepo, branch string, maxRetry int) error { return os.ErrDeadlineExceeded }
	branch := DiagBranchName()
	err := PublishDiag("o/r", branch, sampleDiag("[API Error] 502", "p"))
	if err == nil {
		t.Fatal("no error")
	}
	path := filepath.Join(os.TempDir(), "einsteinos_diag", strings.ReplaceAll(branch, "/", "_")+".json")
	defer os.Remove(path)
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("the error does not say where the record is: %v", err)
	}
	if b, rerr := os.ReadFile(path); rerr != nil || !strings.Contains(string(b), "502") {
		t.Fatalf("local record: %v %q", rerr, b)
	}
}

// the record must never be made in the clone the kernel's other git work shares
func TestPublishDiagDoesNotTouchTheSharedClone(t *testing.T) {
	withLocalRemote(t)
	old := devRepoLocalDirHook
	defer func() { devRepoLocalDirHook = old }()
	devRepoLocalDirHook = func(string) (string, error) {
		t.Fatal("PublishDiag asked for the shared clone")
		return "", nil
	}
	if err := PublishDiag("o/r", DiagBranchName(), sampleDiag("x", "y")); err != nil {
		t.Fatal(err)
	}
}

func TestPublishDiagWithoutARepositoryKeepsTheRecordLocally(t *testing.T) {
	branch := DiagBranchName()
	err := PublishDiag("", branch, sampleDiag("x", "y"))
	defer os.Remove(filepath.Join(os.TempDir(), "einsteinos_diag", strings.ReplaceAll(branch, "/", "_")+".json"))
	if err == nil || !strings.Contains(err.Error(), "no repository") {
		t.Fatalf("%v", err)
	}
}
