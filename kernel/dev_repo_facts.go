package kernel

// dev_repo_facts.go — what a dev conversation is told about its repository, as FACTS
// the kernel measured, rendered through format arguments.
//
// The dev prompt used to say the repository was "already" at /home/claude/<repo> and
// "already contains the branch". That is true on the machine that runs the commands and
// false in the conversation's OWN sandbox, which is where its tools run first. The
// 2026/11/02 overnight run (six conversations compared) split exactly on this: the two
// that ran `git clone` themselves wrote code; the four that took the prompt at its word
// searched a filesystem that did not have the repository, and when the kernel wrote the
// local machine's result back they would not believe it ("that output didn't come from
// my tool calls"). The repository was public and the clone would have worked.
//
// So the block now states, from state: the repository, the directory on the machine that
// runs the commands, and — only if anonymous read access to that branch was measured just
// now — the command that makes the conversation's own sandbox match it. Where a fact is
// unknown its argument is empty and its line is not there; nothing is asserted from
// memory (an earlier notice here called the repository private, which nobody had checked).

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// DevRepoFacts is everything the block needs.
type DevRepoFacts struct {
	Repo   string // owner/name
	Dir    string // where it lives on the machine that runs the commands
	Branch string // the branch the conversation builds on
	URL    string // anonymous clone URL; "" unless it was measured readable
}

const (
	devRepoLineFmt  = "  %[1]s → %[2]s (the machine that runs your commands)\n"
	devRepoCloneFmt = "  in your own sandbox: git clone --branch %[3]s %[4]s %[2]s\n"
)

// Block renders the [DEV REPO] block.
func (f DevRepoFacts) Block() string {
	var b strings.Builder
	b.WriteString("[DEV REPO]\n")
	if f.Repo == "" {
		return b.String()
	}
	b.WriteString(fmt.Sprintf(devRepoLineFmt, f.Repo, f.Dir, f.Branch, f.URL))
	if f.URL != "" && f.Branch != "" {
		b.WriteString(fmt.Sprintf(devRepoCloneFmt, f.Repo, f.Dir, f.Branch, f.URL))
	}
	return b.String()
}

// devRepoProbe reports whether url can be read WITHOUT credentials and has the branch.
// A variable so tests do not need the network.
var devRepoProbe = func(url, branch string) bool {
	if url == "" || branch == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "git", "ls-remote", "--exit-code", url, "refs/heads/"+branch)
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true", "GIT_CONFIG_NOSYSTEM=1", "HOME=/nonexistent")
	return c.Run() == nil
}

// anonymousURL is the public form of a repository's clone URL (no credentials).
func anonymousURL(ownerRepo string) string { return "https://github.com/" + ownerRepo + ".git" }

// newDevRepoFacts measures what can be said about devRepo at branch now.
func newDevRepoFacts(devRepo, branch string) DevRepoFacts {
	f := DevRepoFacts{Repo: devRepo, Dir: devMirrorDir(devRepo), Branch: branch}
	if u := anonymousURL(devRepo); devRepoProbe(u, branch) {
		f.URL = u
	}
	return f
}
