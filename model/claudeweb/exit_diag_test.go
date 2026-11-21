package claudeweb

import (
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/kernel"
)

func TestExitDiagKindSaysWhatWasSent(t *testing.T) {
	for _, c := range []struct {
		newConv   bool
		continues int
		want      string
	}{{true, 0, "new conversation"}, {false, 1, "continue"}, {false, 0, "writeback"}} {
		if got := exitDiagKind(c.newConv, c.continues); got != c.want {
			t.Fatalf("%v %d: %q, want %q", c.newConv, c.continues, got, c.want)
		}
	}
}

func TestExitDiagRecordHoldsTheTextAsTheExitGaveIt(t *testing.T) {
	req := kernel.NewDiagRequest("m", "medium", "continue", "Continue")
	rec := exitDiagRecord("dev_fix_1", "f1cac064-e9b2", "error", 4, 85, 1, "\n[Error] {\"type\":\"error\"}", req)
	if rec.Text != "[Error] {\"type\":\"error\"}" || rec.Role != "dev_fix_1" || rec.ConvID != "f1cac064-e9b2" ||
		rec.Exit != "error" || rec.Rounds != 4 || rec.Tools != 85 || rec.Continues != 1 || rec.Request.Kind != "continue" {
		t.Fatalf("%+v", rec)
	}
	if _, err := time.Parse(time.RFC3339, rec.Time); err != nil {
		t.Fatal(err)
	}
}

// the push is slow: the exit gets the branch's name at once and the push runs after it
func TestStartExitDiagReturnsTheBranchWhilePushing(t *testing.T) {
	oldRepo, oldPub := kernel.DiagRepo(), exitDiagPublish
	defer func() { kernel.SetDiagRepo(oldRepo); exitDiagPublish = oldPub }()
	kernel.SetDiagRepo("zeng-bin3123/baloon")
	release := make(chan struct{})
	got := make(chan [3]string, 1)
	exitDiagPublish = func(repo, branch string, rec kernel.DiagRecord) {
		<-release // the push has not finished when startExitDiag returns
		got <- [3]string{repo, branch, rec.Text}
	}
	branch := startExitDiag(kernel.DiagRecord{Text: "boom"})
	if !regexp.MustCompile(`^einsteinOS/diag_[0-9a-f]{8}$`).MatchString(branch) {
		t.Fatalf("branch %q", branch)
	}
	close(release)
	select {
	case g := <-got:
		if g[0] != "zeng-bin3123/baloon" || g[1] != branch || g[2] != "boom" {
			t.Fatalf("%v", g)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was pushed")
	}
}

func TestStartExitDiagWithoutARepositoryKeepsTheRecordHere(t *testing.T) {
	oldRepo, oldPub := kernel.DiagRepo(), exitDiagPublish
	defer func() { kernel.SetDiagRepo(oldRepo); exitDiagPublish = oldPub }()
	kernel.SetDiagRepo("")
	var mu sync.Mutex
	called := false
	exitDiagPublish = func(string, string, kernel.DiagRecord) { mu.Lock(); called = true; mu.Unlock() }
	name := startExitDiag(kernel.DiagRecord{Text: "boom"})
	if !strings.HasPrefix(name, "local:") {
		t.Fatalf("%q", name)
	}
	path := strings.TrimPrefix(name, "local:")
	defer os.Remove(path)
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "boom") {
		t.Fatalf("%v %q", err, b)
	}
	mu.Lock()
	defer mu.Unlock()
	if called {
		t.Fatal("pushed with no repository")
	}
}

func TestDiagSuffix(t *testing.T) {
	if diagSuffix("") != "" || diagSuffix("einsteinOS/diag_ab12cd34") != " diag=einsteinOS/diag_ab12cd34" {
		t.Fatal("suffix")
	}
}
