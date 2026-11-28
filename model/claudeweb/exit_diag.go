package claudeweb

// exit_diag.go — what a conversation's abnormal exit leaves.
//
// 11/16 12:07:39 unit u253a75befe's dev_fix_1 ended with do_exit(error) one second after a
// Continue; the log held the exit's size, not its text. The record of the exit — its text
// and the request it answered — goes to a branch of the dev repository, and the exit's log
// line carries only that branch's name (kernel/diag.go). Nothing is printed.

import (
	"log"
	"strings"
	"time"

	"google.golang.org/adk/v2/kernel"
)

// exitDiagKind is what the request an exit answered was.
func exitDiagKind(newConversation bool, continues int) string {
	switch {
	case newConversation:
		return "new conversation"
	case continues > 0:
		return "continue"
	default:
		return "writeback"
	}
}

// exitDiagRecord is the record of one exit; text is what doExit was given.
func exitDiagRecord(role, convID, exit string, rounds, tools, continues int, text string, req kernel.DiagRequest) kernel.DiagRecord {
	return kernel.DiagRecord{
		Time: time.Now().UTC().Format(time.RFC3339), ConvID: convID, Role: role, Exit: exit,
		Rounds: rounds, Tools: tools, Continues: continues,
		Text: strings.TrimPrefix(text, "\n"), Request: req,
	}
}

// exitDiagPublish pushes one record; a variable for tests.
var exitDiagPublish = func(repo, branch string, rec kernel.DiagRecord) {
	if err := kernel.PublishDiag(repo, branch, rec); err != nil {
		log.Printf("claudeweb: diag %s not pushed: %v", branch, err)
	}
}

// startExitDiag names the branch the record goes to and starts pushing it — the push is
// slow and the exit must not wait for it — and returns the name for the exit's log line.
// With no repository named yet (no dev run) the record is saved on this machine and the
// name is "local:" and its path.
func startExitDiag(rec kernel.DiagRecord) string {
	branch := kernel.DiagBranchName()
	repo := kernel.DiagRepo()
	if repo == "" {
		if path, err := kernel.SaveDiagLocal(branch, rec); err == nil {
			return "local:" + path
		}
		return ""
	}
	go exitDiagPublish(repo, branch, rec)
	return branch
}

// diagSuffix is what the exit's log line gets: " diag=<branch>", or nothing.
func diagSuffix(diag string) string {
	if diag == "" {
		return ""
	}
	return " diag=" + diag
}
