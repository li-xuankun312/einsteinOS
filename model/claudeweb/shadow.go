package claudeweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/adk/v2/kernel"
)

type ToolDriver interface {
	Match(toolName string) bool
	Execute(toolName string, inputJSON string, workDir string) string
}

type ShadowExecutor struct {
	WorkDir string
	Enabled bool
	DB      *ShadowDB
	drivers []ToolDriver
}

func (s *ShadowExecutor) RegisterDriver(d ToolDriver) {
	s.drivers = append(s.drivers, d)
}

type MemoryToolHandler interface {
	HandleToolCall(toolName string, args map[string]interface{}) (string, error)
}

type toolInput struct {
	Command  string `json:"command"`
	Code     string `json:"code"`
	Path     string `json:"path"`
	Content  string `json:"content"`
	FileText string `json:"file_text"`
	OldStr   string `json:"old_str"`
	NewStr   string `json:"new_str"`
}

func (s *ShadowExecutor) Execute(toolName string, inputJSON string) string {
	// what this machine printed goes back to the conversation: never with the kernel's
	// secrets in it (11/13 05:01:42 the token came from this machine's bash history)
	return kernel.RedactSecrets(s.execute(toolName, inputJSON))
}

func (s *ShadowExecutor) execute(toolName string, inputJSON string) string {
	if !s.Enabled || inputJSON == "" {
		return ""
	}

	for _, driver := range s.drivers {
		if driver.Match(toolName) {
			return driver.Execute(toolName, inputJSON, s.runDir())
		}
	}

	var input toolInput
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return ""
	}

	var command string

	switch {
	case isBashTool(toolName):
		command = input.Command
		if command == "" {
			command = input.Code
		}

	case isCreateFile(toolName):
		path := input.Path
		content := input.Content
		if content == "" {
			content = input.FileText
		}
		if path == "" || content == "" {
			return ""
		}
		command = fmt.Sprintf("mkdir -p \"$(dirname '%s')\" && cat > '%s' << 'SHADOWEOF'\n%s\nSHADOWEOF", path, path, content)

	case isStrReplace(toolName):
		if input.Path == "" || input.OldStr == "" {
			return ""
		}
		// A changed theorem statement is applied here like any other edit, and logged.
		// It used to be BLOCKED: refused on this machine only — the sandbox had already
		// applied it, so from then on the two copies differed — and the child was told
		// to write sorry instead. On 2026/11/03 06:59:22 (chunk 5, ee6a51e6) that turned
		// a correction Axle had prompted (scratch_640: 680 + 584 → 688 + 592) into a
		// false theorem kept with sorry next to the corrected one. Verify, correct,
		// verify again is the loop the theory conversation is for; the kernel records
		// that a statement changed and keeps this copy equal to the sandbox's.
		if strings.HasSuffix(input.Path, ".lean") {
			oldSig := extractTheoremSig(input.OldStr)
			newSig := extractTheoremSig(input.NewStr)
			if oldSig != "" && newSig != "" && oldSig != newSig {
				log.Printf(statementChangedFmt, input.Path, truncateCmd(oldSig, 160), truncateCmd(newSig, 160))
			}
		}
		command = strReplaceCommand(input.Path, input.OldStr, input.NewStr)

	default:
		return ""
	}

	if command == "" {
		return ""
	}

	// the kernel's credentials and its own directories are not a conversation's to read
	// here: 11/13 04:50–05:39 a dev conversation read ~/.ssh/einsteinos_deploy, the bash
	// history and env, reset the remote of the kernel's twin and walked einsteinOS's source.
	// measured against the directory the command runs from: einsteinOS's tree was left out of
	// the kernel's places whenever the configured working directory lay in it (runDir)
	if what := touchesKernel(command, s.runDir()); what != "" {
		log.Printf("[shadow] not run here: it touches %s (the sandbox's result stands)", what)
		return ""
	}

	// Axle proof verification runs on the REAL MACHINE, not remote sandbox.
	// remote sandbox has no network access to axle.axiommath.ai.
	// every Axle request in the command is parsed like a shell would (see
	// axle_extract.go) and sent via Go http.Post — no shell, no escaping issues,
	// and a command with several curls no longer falls into the slow curl path.
	if isAxleCommand(command) {
		if set, ok := extractAxleRequests(command); ok {
			if fillAxleCredentials(&set) {
				log.Printf("[shadow] axle: the request carried no cookie/CSRF — sent with the kernel's own")
			}
			log.Printf("[shadow] axle: using Go HTTP path (%d request(s), payload=%d bytes, session=%d, csrf=%d)",
				len(set.Payloads), len(set.Payloads[0]), len(set.Session), len(set.CSRF))
			return runAxleRequests(set)
		}
		// couldn't safely parse the command — run the raw curl, bounded and killable
		log.Printf("[shadow] axle: using curl fallback (extraction failed): %s",
			truncateCmd(redactAxle(strings.Join(strings.Fields(command), " ")), 300))
		result := axleExecWithRetryCurl(command, s.runDir())
		// axleExecWithRetryCurl never returns "" on exhaustion — it returns a
		// synthetic explanatory message (axleCurlExhaustedPrefix) so the child sees
		// WHY nothing came back, instead of an empty string making the remote
		// sandbox's own failure look like the real answer. Log that distinction
		// honestly: "got N bytes" previously fired for THIS synthetic message too,
		// reading exactly like a real Axle response had arrived when every attempt
		// had in fact timed out.
		if strings.HasPrefix(result, axleCurlExhaustedPrefix) {
			log.Printf("[shadow] axle verify_proof (curl fallback): all attempts failed, returning explanatory message (%d bytes) to the child", len(result))
		} else if result != "" {
			log.Printf("[shadow] axle verify_proof (curl fallback): got %d bytes", len(result))
		}
		if result != "" {
			return result
		}
		log.Printf("[shadow] axle verify_proof: all retries failed, letting remote result stand")
		return ""
	}

	// don't shadow-exec git commands locally.
	// remote sandbox runs them and gets full output; the local result is cut to
	// kernel.LocalResultCap and writeback overrides the remote's complete result
	// with the cut version. that used to make children re-read the same file 10+
	// times in a loop trying to get the rest.
	// git state is shared via GitHub — no local side-effects needed.
	if isGitCommand(command) {
		// the sandbox's output stands, but the local machine keeps up with what the
		// child cloned (kernel/git_twin.go): a clone is mirrored once, shallow, with
		// the kernel's own credentials; the child's token is never used.
		kernel.GitTwinSync(command, s.runDir())
		if gitCommandWritesFiles(command) {
			// 11/03 21:37:38 unit 11 wrote its build.sh and test.sh in a command that also ran
			// git: the whole command was skipped here, the files never reached this machine,
			// and the kernel's verification found compile=missing. Such a command now runs
			// here with git's network and remote commands as no-ops (gitShim), so the files
			// it writes arrive; the sandbox's output still stands.
			if pid, err := kernel.ForkExec(noPush+kernel.MirrorCloneShellFunc()+gitShimClone+gitShim+withGitPathShim(command), s.runDir()); err == nil {
				childPid, code, _ := kernel.Wait(pid)
				if n := kernel.ReprimeClonedMirror(command, s.runDir()); n != "" {
					log.Print(n)
				}
				// a failure here is this machine's result and the one that counts: the
				// conversation is told it. 11/18 01:47–02:47 dev_fix_1 of uf7cdc0dcca (7ad91890)
				// and spawn_work ea75c5d2 never saw what their clone-and-commit commands did
				// here, took them for "not mirrored", and spent two hours placing calls to be
				// mirrored. An exit 0 here proves nothing about git's network commands (no-ops
				// here), so then the sandbox's output, which has git's, stands.
				if code != 0 {
					log.Printf("[shadow] git command that also writes files: ran here with git clone/fetch/push/pull as no-ops (exit %d); this machine's result goes back", code)
					errStr := kernel.GetStderr(childPid)
					return fmt.Sprintf("[pid %d exit=%d] %s\nstderr: %s", childPid, code,
						kernel.ShapeOutput(kernel.GetResult(childPid), kernel.LocalResultCap()), kernel.ShapeOutput(errStr, kernel.LocalResultCap()/4))
				}
				log.Printf("[shadow] git command that also writes files: ran here with git clone/fetch/push/pull as no-ops (exit %d); the sandbox's output stands", code)
			}
			return ""
		}
		log.Printf("[shadow] skip git (remote has full output)")
		if n := kernel.ReprimeClonedMirror(command, s.runDir()); n != "" {
			log.Print(n)
		}
		return ""
	}

	// don't shadow-exec ssh commands — they hang waiting for fingerprint confirmation.
	if strings.Contains(command, "ssh -T ") || strings.Contains(command, "ssh git@") {
		log.Printf("[shadow] skip ssh (hangs on fingerprint)")
		return ""
	}

	// don't shadow-exec curl/wget — network calls belong to the remote sandbox.
	// local execution wastes minutes on DNS/TLS timeouts (sandbox has its own
	// allowed domains), and results get truncated to 2000 chars anyway.
	// this also prevents children from installing lean/elan locally.
	if isCurlOrWget(command) {
		log.Printf("[shadow] skip curl/wget (remote has network)")
		return ""
	}

	// don't shadow-exec lean/elan install — Axle handles verification remotely.
	if strings.Contains(command, "elan") || strings.Contains(command, "leanprover") ||
		strings.Contains(command, "lean4") || strings.Contains(command, "lean --version") {
		log.Printf("[shadow] skip lean/elan install (Axle verifies remotely)")
		return ""
	}

	// a command that reads inside a git twin: the sandbox has the full output, the
	// local result would be cut at kernel.LocalOutputCap and overwrite it.
	if kernel.GitTwinTouches(command) {
		log.Printf("[shadow] skip: reads inside a git twin (remote has full output)")
		return ""
	}

	pid, err := kernel.ForkExec(noPush+withGitPathShim(command), s.runDir())
	if err != nil {
		return fmt.Sprintf("[pid -1] fork: %v", err)
	}

	log.Printf("[pid %d] exec: %s", pid, truncateCmd(command, 120))

	childPid, exitCode, err := waitWithHeartbeat(func() (int64, int, error) { return kernel.Wait(pid) },
		2*time.Minute, func(elapsed time.Duration) {
			log.Printf("[pid %d] still running after %s: %s", pid, elapsed.Round(time.Second),
				truncateCmd(redactAxle(strings.Join(strings.Fields(command), " ")), 160))
		})
	if err != nil {
		return fmt.Sprintf("[pid %d] wait: %v", pid, err)
	}

	// head AND tail, with the sizes of what was cut (kernel/local_result.go). The old
	// first-2000-characters cut hid the end of every long output (a build's errors, the
	// last verdict of a loop) and said nothing about it, so the child asked again —
	// one file per round.
	result := kernel.ShapeOutput(kernel.GetResult(childPid), kernel.LocalResultCap())

	errStr := kernel.GetStderr(childPid)

	log.Printf("[pid %d] exit=%d stdout=%d stderr=%d", childPid, exitCode, len(result), len(errStr))

	if s.DB != nil {
		filePath := ""
		if isCreateFile(toolName) || isStrReplace(toolName) {
			filePath = input.Path
		}
		s.DB.Insert(ExecRecord{
			Pid:      int(childPid),
			Tool:     toolName,
			Command:  truncateCmd(command, 200),
			ExitCode: exitCode,
			Stdout:   result,
			Stderr:   errStr,
			File:     filePath,
		})
	}

	if exitCode != 0 {
		return fmt.Sprintf("[pid %d exit=%d] %s\nstderr: %s", childPid, exitCode, result, kernel.ShapeOutput(errStr, kernel.LocalResultCap()/4))
	}
	return fmt.Sprintf("[pid %d exit=0] %s", childPid, result)
}

// waitWithHeartbeat runs wait and calls beat every interval until it returns.
//
// kernel.Wait has a 150-minute idle timeout, so a local command that never
// returns used to freeze the child's loop for up to 2.5 hours with nothing in the
// log. Ordinary commands (make, CoreX builds, tests) may legitimately run long, so
// this does not kill anything — it makes a stuck command visible.
func waitWithHeartbeat(wait func() (int64, int, error), every time.Duration, beat func(elapsed time.Duration)) (int64, int, error) {
	type res struct {
		pid  int64
		code int
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		p, c, e := wait()
		ch <- res{p, c, e}
	}()
	start := time.Now()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case r := <-ch:
			return r.pid, r.code, r.err
		case <-tick.C:
			beat(time.Since(start))
		}
	}
}

// matches BOTH "axle" baked into the filename (axle.sh, axle_run.py, verify_axle.py)
// AND "axle" as a path/directory component (/tmp/axle/run.py) — a real case this
// missed: axleScriptRe required "axle" adjacent to the filename, so a script a
// child wrote to /tmp/axle/run.py (axle as a DIRECTORY, not part of the filename)
// fell through to the unbounded generic exec path (only the 150-min idle backstop).
var axleScriptRe = regexp.MustCompile(`(?i)(^|[^a-z0-9])axle(?:[._-]?[a-z0-9_]*|/[a-z0-9_]*)\.(sh|py)\b`)

// statementChangedFmt: a .lean edit that changed a theorem statement (applied).
const statementChangedFmt = "[shadow] statement changed in %s (applied, as in the sandbox)\n  old: %s\n  new: %s"

// isAxleCommand: any command that talks to Axle. The URL and verify_proof were the
// only markers, so a child that wraps the call in /tmp/axle.sh and loops over files
// ("for f in *.lean; do /tmp/axle.sh $f") escaped into the ordinary path, where the
// only bound is Wait's 150 minutes.
func isAxleCommand(command string) bool {
	return strings.Contains(command, "axle.axiommath.ai") ||
		strings.Contains(command, "verify_proof") ||
		axleScriptRe.MatchString(command)
}

func isBashTool(name string) bool {
	n := strings.ToLower(name)
	return n == "bash_tool" || n == "bash" || n == "repl" ||
		n == "computer" || n == "terminal" || n == "shell" ||
		n == "execute_command" || n == "run_command"
}

func isCreateFile(name string) bool {
	n := strings.ToLower(name)
	return n == "create_file" || n == "write_file" || n == "write_to_file"
}

func isStrReplace(name string) bool {
	n := strings.ToLower(name)
	return n == "str_replace" || n == "str_replace_editor" || n == "edit_file"
}

func isMemoryTool(name string) bool {
	n := strings.ToLower(name)
	return n == "search_project_memory" || n == "project_status" || n == "load_memory"
}

func isCurlOrWget(command string) bool {
	trimmed := strings.TrimSpace(command)
	if strings.HasPrefix(trimmed, "curl ") || strings.HasPrefix(trimmed, "wget ") {
		return true
	}
	// cd ... && curl ..., cd ... ; curl ...
	if strings.Contains(trimmed, "&& curl ") || strings.Contains(trimmed, "; curl ") ||
		strings.Contains(trimmed, "&& wget ") || strings.Contains(trimmed, "; wget ") {
		return true
	}
	// curl/wget as subshell: $(curl ...), `curl ...`
	if strings.Contains(command, "$(curl") || strings.Contains(command, "$(wget") {
		return true
	}
	// curl/wget at the start of any line of a script, or after | || && ; ( ` — 11/14 08:49,
	// 08:51 and 15:55 "cd …\n# Try GitHub API with GITHUB_TOKEN env var\ncurl -s -H
	// \"Authorization: token $GITHUB_TOKEN\" https://api.github.com/repos/…" was none of the
	// forms above and ran here, with this machine's environment
	return curlLineRe.MatchString(command)
}

// curlLineRe: curl or wget as a command anywhere in a script
var curlLineRe = regexp.MustCompile("(?m)(^|[|&;(`])\\s*(curl|wget)(\\s|$)")

// gitShimClone finds where "git clone [options] <url> [<dir>]" puts the clone and hands it to
// the kernel's __eos_mirror_clone: a clone into the mirror gives it the branch tip here at that
// point of the command (kernel/dev_mirror_clone.go).
const gitShimClone = `gitShimCloneTarget() { local u="" t="" a=("$@") j=0; while [ $j -lt ${#a[@]} ]; do case "${a[$j]}" in ` +
	`-b|--branch|--depth|-o|--origin|-c|--config|--reference|--reference-if-able|-j|--jobs|--filter|--template|--separate-git-dir|-u|--upload-pack|--shallow-since|--shallow-exclude|--server-option) j=$((j+2));; ` +
	`--) j=$((j+1));; -*) j=$((j+1));; *) if [ -z "$u" ]; then u="${a[$j]}"; elif [ -z "$t" ]; then t="${a[$j]}"; fi; j=$((j+1));; esac; done; ` +
	`[ -z "$u" ] && return 0; if [ -z "$t" ]; then t="${u%/}"; t="${t##*/}"; t="${t##*:}"; t="${t%.git}"; fi; __eos_mirror_clone "$t"; }
`

// gitShim makes git's network and remote commands no-ops for a command run on this machine,
// and init: a repository a conversation starts in the unit's directory is taken for the
// kernel's clone of it (devRepoLocalDir), 11/13 04:21:46 → every push after "not a git
// repository". The sandbox already ran them; lets every other git command touch the local tree, and
// never stops a && chain.
const gitShim = `git() { local a=("$@") i=0; while [ $i -lt ${#a[@]} ]; do case "${a[$i]}" in -C|-c) i=$((i+2));; -*) i=$((i+1));; *) break;; esac; done; ` +
	`case "${a[$i]}" in clone) gitShimCloneTarget "${a[@]:$((i+1))}"; return 0;; fetch|push|pull|remote|ls-remote|submodule|init) return 0;; esac; ` +
	`GIT_AUTHOR_NAME=shadow GIT_AUTHOR_EMAIL=shadow@local GIT_COMMITTER_NAME=shadow GIT_COMMITTER_EMAIL=shadow@local command git "$@" >/dev/null 2>&1; return 0; }
`

var gitWriteRe = regexp.MustCompile(`(^|[^0-9&>])>>?\s*[^&\s/]|\btee\s|\bcp\s|\bmv\s|\bmkdir\s|\bsed\s+-i|\bpython3?\b|\btouch\s|\binstall\s|\bpatch\s|\bunzip\s|\btar\s+-?x`)

// gitCommandWritesFiles: a command that runs git and does anything besides git and cd. Whatever
// follows git after ; && || or a new line runs here (with gitShim), so a file it writes arrives
// wherever it is written: 11/13 00:22:35 unit 7 wrote /tmp/guard.sh after git, the old pattern
// (a redirect not to an absolute path) did not see it, and every later call exited 127 here.
func gitCommandWritesFiles(command string) bool {
	for _, seg := range shellCommands(command) {
		if startsWithGit(seg) && gitChangesTree(seg) {
			return true
		}
		if startsWithGit(seg) || strings.HasPrefix(seg, "GIT_DIR=") || strings.HasPrefix(seg, "cd ") {
			continue
		}
		return true
	}
	return false
}

// gitChangesTree: the git command seg changes the files of the work tree — what the sandbox's
// git did to them has to be done here too, or the mirror keeps what the conversation removed.
// 11/19 12:36:33 p3798e66f3c removed include/ref/ngram/ with git rm alone: skipped here, the
// file stayed, and twice the kernel told the conversation its files had not changed.
func gitChangesTree(seg string) bool {
	f := strings.Fields(envAssignRe.ReplaceAllString(seg, ""))
	i := 0
	for i < len(f) && f[i] != "git" {
		i++
	}
	i++
	for i < len(f) && strings.HasPrefix(f[i], "-") { // git's own options: -C dir, -c k=v, --git-dir=…
		if f[i] == "-C" || f[i] == "-c" {
			i++
		}
		i++
	}
	if i >= len(f) {
		return false
	}
	sub, args := f[i], f[i+1:]
	has := func(opts ...string) bool {
		for _, a := range args {
			for _, o := range opts {
				if a == o {
					return true
				}
			}
		}
		return false
	}
	switch sub {
	case "rm", "mv", "restore", "apply", "clean", "am", "cherry-pick", "revert", "merge", "rebase":
		return true
	case "checkout", "switch":
		return !has("-b", "-B", "-c", "-C", "--orphan") || has("--")
	case "reset":
		return has("--hard", "--merge", "--keep")
	case "stash":
		return len(args) == 0 || !has("list", "show")
	}
	return false
}

// gitSegmentRe: the separators between the commands of a shell line (a pipe stays in its command).
var gitSegmentRe = regexp.MustCompile(`&&|\|\||;|\n`)

func isGitCommand(command string) bool {
	// match any command that starts with git or uses git --git-dir / GIT_DIR=
	// these are read operations the remote sandbox handles; local execution
	// adds nothing (state syncs via GitHub) and the 2000-char truncation
	// destroys the result.
	trimmed := strings.TrimSpace(command)
	if strings.HasPrefix(trimmed, "git ") || strings.HasPrefix(trimmed, "git\t") {
		return true
	}
	if strings.HasPrefix(trimmed, "GIT_DIR=") {
		return true
	}
	// cd /tmp && git ..., cd /tmp/ref_* && git ...
	if strings.Contains(trimmed, "&& git ") || strings.Contains(trimmed, "; git ") {
		return true
	}
	// git clone as a subcommand
	if strings.Contains(command, "git clone") || strings.Contains(command, "git --git-dir") {
		return true
	}
	// any command of the line, the way the shell reads it: 11/13 04:21–04:27 a dev
	// conversation's "cd /home/claude/baloon\n\n# ...\ngit init / commit / push" was not
	// seen as git and ran here whole, and 05:28:58 "cd … && \\\n  git remote set-url …" too
	for _, seg := range shellCommands(command) {
		if startsWithGit(seg) {
			return true
		}
	}
	return false
}

// shellCommands: the commands of a shell line — continuations joined, split at ; && || and
// new lines, comments and empty pieces dropped.
func shellCommands(command string) []string {
	var out []string
	for _, seg := range gitSegmentRe.Split(strings.ReplaceAll(command, "\\\n", " "), -1) {
		seg = strings.TrimSpace(seg)
		if seg == "" || strings.HasPrefix(seg, "#") {
			continue
		}
		out = append(out, seg)
	}
	return out
}

// envAssignRe: VAR=value words in front of a command.
var envAssignRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*=("[^"]*"|'[^']*'|\S*)\s+)+`)

// startsWithGit: the command runs git (after VAR=… assignments, env, command, sudo).
func startsWithGit(seg string) bool {
	for {
		seg = strings.TrimSpace(envAssignRe.ReplaceAllString(seg, ""))
		trimmed := false
		for _, w := range []string{"env ", "command ", "sudo ", "exec "} {
			if strings.HasPrefix(seg, w) {
				seg, trimmed = seg[len(w):], true
			}
		}
		if !trimmed {
			break
		}
	}
	return seg == "git" || strings.HasPrefix(seg, "git ") || strings.HasPrefix(seg, "git\t")
}

// extractTheoremSig extracts the theorem signature from a code block.
// signature = everything from "theorem" to ":= by" (exclusive).
// returns "" if no theorem found in the text.
func extractTheoremSig(code string) string {
	idx := strings.Index(code, "theorem ")
	if idx < 0 {
		return ""
	}
	rest := code[idx:]
	// find := by or := sorry
	for _, sep := range []string{":= by", ":= sorry", ":=\n  sorry", ":= by\n"} {
		end := strings.Index(rest, sep)
		if end > 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	return ""
}

// axleExecWithRetry calls Axle via Go http.Post — no shell, no escaping issues.
func axleExecWithRetry(payload, session, csrf string) string {
	type round struct {
		name   string
		direct bool
		tries  int
		guard  time.Duration
	}
	// Which path first: whichever worked last (set at boot by configureAxleProxy and
	// after every success). The "proxy" rounds really use the environment proxy now;
	// they used to be direct as well (a zero http.Transport has no proxy), which made
	// the Go path useless on a network that can only reach Axle through its proxy.
	direct := round{"direct", true, 2, 15 * time.Second}
	viaProxy := round{"proxy", false, 2, 20 * time.Second}
	rounds := []round{direct, viaProxy, direct}
	if !axleDirectPreferred.Load() {
		rounds = []round{viaProxy, direct, viaProxy}
	}
	hasProxy := envProxyFunc() != nil

	attempts := 0
	for _, r := range rounds {
		if !r.direct && !hasProxy {
			continue // no env proxy: a "proxy" attempt would just be direct again
		}
		for attempt := 1; attempt <= r.tries; attempt++ {
			attempts++
			t0 := time.Now()
			result := axleHTTPOnce(payload, session, csrf, r.direct, r.guard)
			elapsed := time.Since(t0)
			trimmed := strings.TrimSpace(result)
			if trimmed == "" {
				log.Printf("[shadow] axle %s %d/%d: no response (%s, took %s)", r.name, attempt, r.tries, r.guard, elapsed.Round(time.Second))
				// if request returned instantly (connection refused, not timeout),
				// wait to avoid a burst of attempts in one second
				if elapsed < 3*time.Second {
					time.Sleep(3*time.Second - elapsed)
				}
				continue
			}
			if strings.HasPrefix(trimmed, "Usage: curl") || strings.HasPrefix(trimmed, "<!DOCTYPE") ||
				strings.HasPrefix(trimmed, "<html") {
				log.Printf("[shadow] axle %s %d/%d: garbage (%d bytes)", r.name, attempt, r.tries, len(result))
				continue
			}
			log.Printf("[shadow] axle %s %d/%d: got response (%d bytes)", r.name, attempt, r.tries, len(result))
			axleDirectPreferred.Store(r.direct) // remember which path works
			return fmt.Sprintf("[pid 0 exit=0] %s", result)
		}
	}
	log.Printf("[shadow] axle: all %d attempts exhausted", attempts)
	return ""
}

// axleHTTPOnce sends a single Axle verification request via Go net/http.
func axleHTTPOnce(payload, session, csrf string, directNoProxy bool, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	transport := &http.Transport{TLSHandshakeTimeout: timeout, DisableKeepAlives: true}
	if !directNoProxy {
		transport.Proxy = envProxyFunc() // nil when no proxy is configured
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: timeout}

	req, err := http.NewRequestWithContext(ctx, "POST",
		axleURL,
		strings.NewReader(payload))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Source", "web")
	req.Header.Set("Origin", "https://axle.axiommath.ai")
	req.Header.Set("X-Browser-Api-Csrf", csrf)
	req.Header.Set("Cookie", "provertools_session="+session)

	resp, err := client.Do(req)
	if err != nil {
		return "" // timeout or connection error
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

// axleURL is a var so tests can point the Go path at an httptest server.
var axleURL = "https://axle.axiommath.ai/api/v1/verify_proof"

// axleDirectPreferred is set when the last curl-fallback success came from the
// no-proxy path, so the next call tries that first.
var axleDirectPreferred atomic.Bool

// axleRun is what one execution of a fallback command produced. The old code kept
// only stdout (cmd.Output) and collapsed every empty result into "timeout", so a
// command that failed in 0s (missing script, syntax error, errors written only to
// stderr) was logged as "timeout (15s, took 0s)" and retried seven times.
type axleRun struct {
	Stdout   string
	Stderr   string
	ExitCode int // -1: could not start
	TimedOut bool
	Elapsed  time.Duration
}

var (
	curlWordRe    = regexp.MustCompile(`\bcurl\b`)
	sessionRedact = regexp.MustCompile(`provertools_session=[^;'"\s]+`)
	csrfRedact    = regexp.MustCompile(`(?i)(x-browser-api-csrf:\s*)[^'"\s]+`)
	// a cookie given whole, without its name: curl -b '<value>' / --cookie '<value>' or a
	// "cookie:" header. 11/17 13:01:18 review_conv sent -b 'eyJicm93c2VyX2FwaV9jc3Jm…' and the
	// log line printed it as it was
	cookieArgRedact    = regexp.MustCompile(`(\s(?:-b|--cookie)\s+)('[^']*'|"[^"]*"|[^\s'"]+)`)
	cookieHeaderRedact = regexp.MustCompile(`(?i)(cookie:\s*)[^'"\n]+`)
)

// axleScriptGuard: a command with no curl in it is a script (for example a loop
// calling /tmp/axle.sh on every file). It may legitimately run for minutes, and
// retrying it cannot change a deterministic failure. Sized for ~a dozen files at
// Axle's own per-proof limit.
const axleScriptGuard = 10 * time.Minute

// redactAxle removes the session cookie and csrf token before a command is logged.
func redactAxle(cmd string) string {
	cmd = sessionRedact.ReplaceAllString(cmd, "provertools_session=<redacted>")
	cmd = cookieArgRedact.ReplaceAllString(cmd, "${1}<redacted>")
	cmd = cookieHeaderRedact.ReplaceAllString(cmd, "${1}<redacted>")
	return csrfRedact.ReplaceAllString(cmd, "${1}<redacted>")
}

// retryableFailure: only failures that look like the network are worth repeating.
// curl exits 6 (resolve), 7 (connect), 28 (timeout), 35/52/56 (TLS / empty reply /
// recv). A pipeline hides curl's own exit code, so stderr is checked as well.
func retryableFailure(r axleRun) bool {
	if r.TimedOut {
		return true
	}
	switch r.ExitCode {
	case 6, 7, 28, 35, 52, 56:
		return true
	}
	e := strings.ToLower(r.Stderr)
	for _, m := range []string{"could not resolve", "connection refused", "connection reset",
		"timed out", "tls", "ssl", "network is unreachable", "proxy"} {
		if strings.Contains(e, m) {
			return true
		}
	}
	return false
}

// formatAxleRun renders a run the way a shell tool result would look, including
// stderr, so the child sees the real error instead of a silent nothing.
func formatAxleRun(r axleRun) string {
	out := r.Stdout
	if strings.TrimSpace(r.Stderr) != "" {
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += r.Stderr
	}
	const max = 20000
	if len(out) > max {
		out = out[:max] + "\n…(truncated)"
	}
	code := r.ExitCode
	if code < 0 {
		code = 127
	}
	return fmt.Sprintf("[pid 0 exit=%d] %s", code, out)
}

// axleCurlExhaustedPrefix marks axleExecWithRetryCurl's own synthetic
// give-up message so callers can log/handle it as "no real response", not as
// Axle data. It must never itself look like real Axle output, so a child
// parsing JSON out of a response correctly fails to find any — exactly as it
// should when nothing actually answered.
const axleCurlExhaustedPrefix = "\x00AXLE_CURL_EXHAUSTED\x00"

// axleExecWithRetryCurl runs the child's raw command locally when its Axle
// requests could not be parsed (see extractAxleRequests).
// axleCurlRound is one batch of same-path attempts in the curl fallback's retry
// loop.
type axleCurlRound struct {
	name     string
	envUnset bool
	tries    int
	guard    time.Duration
}

// axleCurlRounds builds the retry schedule: total independent connection
// attempts, split into repeating (3 preferred-path, 1 other-path) windows,
// preferred-first. Pure (no I/O, no timing) so the schedule itself — not just
// the eventual outcome of actually running it — is directly testable:
//
//   - a kill-and-restart here IS what a manual Ctrl+C + retry does
//     (axleRunOnce's hardKillOnCancel SIGKILLs the whole process group on
//     timeout, same as the terminal's SIGINT would), so each attempt already
//     opens a genuinely fresh connection — the only real gap was how many
//     times this was willing to do that before giving up (a fixed 6, ~100s,
//     far short of the patience manual retrying needed in practice)
//   - the order still favors whichever path last worked (set by
//     configureAxleProxy at boot and updated on every success), but never
//     fully abandons the other path, since conditions can change over the
//     several minutes this may now run for
func axleCurlRounds(total int, preferDirect bool) []axleCurlRound {
	primary, secondary := "proxy", "direct"
	primaryUnset, secondaryUnset := false, true
	if preferDirect {
		primary, secondary = "direct", "proxy"
		primaryUnset, secondaryUnset = true, false
	}
	var rounds []axleCurlRound
	for remaining := total; remaining > 0; {
		n := 3
		if n > remaining {
			n = remaining
		}
		rounds = append(rounds, axleCurlRound{primary, primaryUnset, n, 15 * time.Second})
		remaining -= n
		if remaining <= 0 {
			break
		}
		n = 1
		if n > remaining {
			n = remaining
		}
		rounds = append(rounds, axleCurlRound{secondary, secondaryUnset, n, 15 * time.Second})
		remaining -= n
	}
	return rounds
}

// axleIdleGuard: how long a multi-request command may print nothing before it counts
// as stalled. Longer than Axle's own per-proof limit (the requests ask for 120 s).
func axleIdleGuard() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv("EINSTEIN_AXLE_IDLE"))); err == nil && d > 0 {
		return d
	}
	return 150 * time.Second
}

var (
	loopWordRe = regexp.MustCompile(`(?m)(^|[;&|({]|\s)(for|while|until)\s|\bxargs\b|\bparallel\b`)
)

// isMultiRequestCommand: a command that makes more than one request — a shell loop, a
// parallel runner, or several curl invocations.
func isMultiRequestCommand(command string) bool {
	return loopWordRe.MatchString(command) || len(curlWordRe.FindAllString(command, -1)) > 1
}

// envIntOr reads a positive integer from the environment.
func envIntOr(name string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && v > 0 {
		return v
	}
	return def
}

// axleCurlPrelude is put in front of a multi-request command so every curl in it gives up
// on a connection that does not come up, and tries again — the way git_guard does for git:
// no sign of life within the start window, kill it and start another, and judge by the
// outcome, not by how long the whole command took.
//
// Measured 2026/11/02 (axle_burst.py, 34 sequential requests from the Iluvatar machine):
// 14 failed, every one the same way — TCP connected in a millisecond, the TLS handshake
// never finished (tls=0.000000), 20 s to the timeout — at random, never more than two in a
// row, healthy requests finished in 1-3 s. The curl in a child's loop has no timeout at all
// (it waits for ever), so a loop of seven requests had a 1 − 0.59⁷ ≈ 97% chance of a hang
// and nothing but the idle guard to end it. --connect-timeout covers the handshake (tested
// against a server that accepts TCP and never answers TLS, on curl 7.68 and 8.5), and
// --retry retries that timeout. It limits only the connection: once the request is sent,
// Axle may take its 120 s.
//
// The numbers are arguments: EINSTEIN_AXLE_CONNECT_TIMEOUT (8 s: about three times the
// slowest healthy handshake measured) and EINSTEIN_AXLE_CURL_RETRIES (5). It reaches only
// curl commands the shell runs itself, not a curl started by a script the child wrote.
func axleCurlPrelude() string {
	return fmt.Sprintf("curl() { command curl --connect-timeout %d --retry %d --retry-delay 1 \"$@\"; }\n",
		envIntOr("EINSTEIN_AXLE_CONNECT_TIMEOUT", 8), envIntOr("EINSTEIN_AXLE_CURL_RETRIES", 5))
}

// axleExecMulti runs a multi-request command once, on the path that last worked.
//   - it finishes: its whole output goes back;
//   - it stalls after printing something: what it printed goes back, with a note, and
//     the child redoes only what is missing (re-running the whole loop is what stalled);
//   - it stalls before printing anything: the network is the suspect, so it is tried
//     again, on the other path first, up to three times in all.
func axleExecMulti(command string, workDir string) string {
	prefer := axleDirectPreferred.Load()
	order := []bool{prefer, !prefer, prefer}
	idle := axleIdleGuard()
	timeouts := 0
	for i, unset := range order {
		path := "proxy"
		if unset {
			path = "direct"
		}
		run := axleRunGuarded(axleCurlPrelude()+command, workDir, unset, axleScriptGuard, idle)
		trimmed := strings.TrimSpace(run.Stdout)
		switch {
		case run.TimedOut && trimmed != "":
			log.Printf("[shadow] axle-multi %s: stalled after %s with output (%d bytes) — returning what it printed",
				path, run.Elapsed.Round(time.Second), len(run.Stdout))
			return fmt.Sprintf("[pid 0 exit=124] axle: this command printed nothing for %s and was stopped. "+
				"Output so far (redo only what is missing, in smaller batches):\n%s%s", idle, run.Stdout, run.Stderr)
		case run.TimedOut:
			timeouts++
			log.Printf("[shadow] axle-multi %s %d/%d: no output at all for %s", path, i+1, len(order), idle)
			continue
		case trimmed == "" && !retryableFailure(run):
			log.Printf("[shadow] axle-multi %s: command failed in %s (exit %d) — not retrying: %s",
				path, run.Elapsed.Round(time.Millisecond), run.ExitCode, truncateCmd(redactAxle(run.Stderr), 200))
			return formatAxleRun(run)
		case trimmed == "":
			log.Printf("[shadow] axle-multi %s %d/%d: network-looking failure (exit %d)", path, i+1, len(order), run.ExitCode)
			continue
		}
		log.Printf("[shadow] axle-multi %s: finished in %s, %d bytes", path, run.Elapsed.Round(time.Second), len(run.Stdout))
		axleDirectPreferred.Store(unset)
		return fmt.Sprintf("[pid 0 exit=0] %s", run.Stdout)
	}
	log.Printf("[shadow] axle-multi: %d attempts, none produced output (%d stalled)", len(order), timeouts)
	return fmt.Sprintf(axleCurlExhaustedPrefix+"[pid 0 exit=28] axle: no response from the local machine after %d attempts (%d stalled). "+
		"This is a network problem, not a proof failure — retry later.", len(order), timeouts)
}

func axleExecWithRetryCurl(command string, workDir string) string {
	// a script, not a curl: one run, long guard, report everything it printed
	if !curlWordRe.MatchString(command) {
		// proxy stripped when direct is the working path (see axle_path.go): through
		// the env proxy the same calls took ~87 s each instead of ~1.6 s
		run := axleRunOnce(command, workDir, axleDirectPreferred.Load(), axleScriptGuard)
		log.Printf("[shadow] axle-script: exit=%d timed_out=%v took=%s stdout=%d stderr=%d",
			run.ExitCode, run.TimedOut, run.Elapsed.Round(time.Second), len(run.Stdout), len(run.Stderr))
		if run.TimedOut {
			return fmt.Sprintf("[pid 0 exit=124] axle script: no result within %s (killed). Output so far:\n%s%s",
				axleScriptGuard, run.Stdout, run.Stderr)
		}
		return formatAxleRun(run)
	}

	// A loop, or more than one curl: many requests in one command. The schedule below
	// is for ONE request (15 s per whole command, up to 30 attempts): a loop over a
	// dozen files needs longer than 15 s as soon as Axle is a little slow, and then every
	// attempt kills it and runs it again from the start — six minutes and more of
	// nothing (2026/11/02 09:25-09:31). Such a command runs once and is judged by
	// whether it is still producing output.
	if isMultiRequestCommand(command) {
		return axleExecMulti(command, workDir)
	}

	// How many independent connection attempts this gets, total, across both
	// paths, and in what order — see axleCurlRounds.
	rounds := axleCurlRounds(axleCurlAttempts(), axleDirectPreferred.Load())

	attempts, timeouts := 0, 0
	for _, r := range rounds {
		for attempt := 1; attempt <= r.tries; attempt++ {
			attempts++
			run := axleRunOnce(command, workDir, r.envUnset, r.guard)
			trimmed := strings.TrimSpace(run.Stdout)
			switch {
			case run.TimedOut:
				timeouts++
				log.Printf("[shadow] axle-curl %s %d/%d: timeout (%s, took %s)", r.name, attempt, r.tries, r.guard, run.Elapsed.Round(time.Second))
			case trimmed == "" && !retryableFailure(run):
				// deterministic: retrying a missing script seven times helps nobody
				log.Printf("[shadow] axle-curl %s %d/%d: command failed in %s (exit %d) — not retrying: %s",
					r.name, attempt, r.tries, run.Elapsed.Round(time.Millisecond), run.ExitCode,
					truncateCmd(redactAxle(run.Stderr), 200))
				return formatAxleRun(run)
			case trimmed == "":
				log.Printf("[shadow] axle-curl %s %d/%d: network-looking failure (exit %d, took %s)",
					r.name, attempt, r.tries, run.ExitCode, run.Elapsed.Round(time.Second))
			case (strings.HasPrefix(trimmed, "<!DOCTYPE") || strings.HasPrefix(trimmed, "<html")) && !strings.Contains(command, "/api/"):
				// a page, asked for as a page: Axle's answer to it is HTML, and the first one is
				// the answer — 11/17 11:28:09 review_conv's "curl -s https://axle.axiommath.ai/"
				// was run 30 times as "garbage" and the conversation was told the network failed
				log.Printf("[shadow] axle-curl %s %d/%d: a page (%d bytes), not an API request — returned as it came", r.name, attempt, r.tries, len(run.Stdout))
				return fmt.Sprintf("[pid 0 exit=0] %s", run.Stdout)
			case strings.HasPrefix(trimmed, "Usage: curl") || strings.HasPrefix(trimmed, "<!DOCTYPE") || strings.HasPrefix(trimmed, "<html"):
				log.Printf("[shadow] axle-curl %s %d/%d: garbage (%d bytes)", r.name, attempt, r.tries, len(run.Stdout))
			default:
				log.Printf("[shadow] axle-curl %s %d/%d: got response (%d bytes)", r.name, attempt, r.tries, len(run.Stdout))
				axleDirectPreferred.Store(r.envUnset) // remember which path works
				return fmt.Sprintf("[pid 0 exit=0] %s", run.Stdout)
			}
			if run.Elapsed < 3*time.Second {
				time.Sleep(3*time.Second - run.Elapsed)
			}
		}
	}
	log.Printf("[shadow] axle-curl: all %d attempts exhausted (%d timed out)", attempts, timeouts)
	// say so explicitly: returning "" made the remote sandbox's own failure ("could
	// not resolve host") stand, which reads as "Axle is down", not "no answer yet".
	// axleCurlExhaustedPrefix lets the caller log this distinctly from a real response.
	return fmt.Sprintf(axleCurlExhaustedPrefix+"[pid 0 exit=28] axle: no response from the local machine after %d attempts (%d timed out). "+
		"This is a network problem, not a proof failure — retry later.", attempts, timeouts)
}

// axleRunOnce runs command under bash with a hard timeout and captures stdout,
// stderr, the exit code and whether the timeout fired.
func axleRunOnce(command string, workDir string, unsetProxy bool, timeout time.Duration) axleRun {
	return axleRunGuarded(command, workDir, unsetProxy, timeout, 0)
}

// activityWriter counts as progress every byte the command prints.
type activityWriter struct {
	buf  *bytes.Buffer
	mu   *sync.Mutex
	last *time.Time
}

func (w activityWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	*w.last = time.Now()
	return w.buf.Write(p)
}

// axleRunGuarded is axleRunOnce with a second guard: if idle > 0, the command is also
// killed when it prints NOTHING for idle (TimedOut is then true, and Stdout holds what
// it had printed). A fixed total time is the wrong measure for a command that makes
// many requests: it fails exactly when each request is slow, and re-running it from
// the start fails the same way.
func axleRunGuarded(command string, workDir string, unsetProxy bool, timeout, idle time.Duration) axleRun {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	hardKillOnCancel(cmd) // kill the whole group, not just bash — see shadow_proc_unix.go
	if workDir != "" {
		cmd.Dir = workDir
	}
	env := os.Environ()
	if unsetProxy {
		filtered := make([]string, 0, len(env))
		for _, e := range env {
			lower := strings.ToLower(e)
			if strings.HasPrefix(lower, "https_proxy=") || strings.HasPrefix(lower, "http_proxy=") ||
				strings.HasPrefix(lower, "all_proxy=") {
				continue
			}
			filtered = append(filtered, e)
		}
		env = filtered
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	var mu sync.Mutex
	last := time.Now()
	cmd.Stdout = activityWriter{&stdout, &mu, &last}
	cmd.Stderr = activityWriter{&stderr, &mu, &last}

	t0 := time.Now()
	if startErr := cmd.Start(); startErr != nil {
		return axleRun{ExitCode: -1, Stderr: startErr.Error(), Elapsed: time.Since(t0)}
	}
	// this command's own process group is isolated from the terminal's (hardKillOnCancel,
	// see shadow_proc_unix.go) so our own context timeout can kill it reliably — but that
	// same isolation means a Ctrl-C to the program would otherwise never reach it. Track it
	// so the program's own signal handler can still kill it (see kernel/proc_registry.go).
	kernel.TrackProcessGroup(cmd.Process.Pid)

	stalled := make(chan struct{})
	var stalledOnce sync.Once
	finished := make(chan struct{})
	if idle > 0 {
		go func() {
			tick := time.NewTicker(250 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-finished:
					return
				case <-tick.C:
					mu.Lock()
					quiet := time.Since(last)
					mu.Unlock()
					if quiet >= idle {
						stalledOnce.Do(func() { close(stalled) })
						cancel()
						return
					}
				}
			}
		}()
	}
	err := cmd.Wait()
	close(finished)
	kernel.UntrackProcessGroup(cmd.Process.Pid)
	mu.Lock()
	run := axleRun{Stdout: stdout.String(), Stderr: stderr.String(), Elapsed: time.Since(t0)}
	mu.Unlock()
	select {
	case <-stalled:
		run.TimedOut = true
	default:
	}
	if ctx.Err() == context.DeadlineExceeded {
		run.TimedOut = true
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			run.ExitCode = ee.ExitCode()
		} else {
			run.ExitCode = -1
			run.Stderr += err.Error()
		}
	}
	return run
}

func truncateCmd(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// kernelPlaces: what a command run here must not touch — the credentials this machine
// holds and the directories the kernel works in (its twins and stores, and einsteinOS).
var kernelPlaces = []string{".ssh", "bash_history", ".git-credentials", ".netrc", ".gh_token",
	"einsteinos_deploy", "/environ", "/home/claude/work", "/tmp/ref_store_",
	// the other places a token is kept or read from — 11/14 08:35 "cat /root/.npmrc" (2215
	// bytes), 09:44 git's credential helper (92 bytes), 16:15 "echo GITHUB_TOKEN=${GITHUB_TOKEN:+SET}"
	// all ran here
	".npmrc", "git credential", "credential.helper", "GITHUB_TOKEN", "GH_TOKEN",
	// the stores a desktop or an agent keeps secrets in — 11/15 12:30–12:32 a dev conversation
	// ran here "ssh-add -l", "cat /etc/credstore", "secret-tool lookup server github.com" and
	// "ps aux | grep -i cred" with a query of the DBus secret service (875 bytes)
	"ssh-add", "secret-tool", "credstore", "org.freedesktop.secrets", "gnome-keyring", "kwallet",
	"gh auth",
	// git's hooks: a script there runs inside the kernel's own git commands — 11/15 19:52
	// dev_unit_33 wrote .git/hooks/post-checkout "so that when the kernel creates its
	// verification worktree the hook fires automatically and ensures locate.py has …", to make
	// the verification see a file other than the one it would check out
	".git/hooks", "core.hooksPath",
	// where a container keeps the secrets mounted into it — 11/18 16:06:09 "find /run/secrets
	// /var/run/secrets" (1148 bytes), 16:07:22 "cat /run/secrets/kubernetes.io/serviceaccount/token
	// | cut -c1-50" (445 bytes) and 16:09:00 its length and first 10 characters ran here
	"/run/secrets",
	// git's and gh's own configuration, where a credential or a token is kept — 11/18 16:08:39
	// "find /root/.config …; cat /home/claude/.config/gh/hosts.yml" (1489 bytes), 16:06:09
	// "cat ~/.gitconfig" (186 bytes) and 17:20:27 "find /root /home -name \".gitconfig\" | xargs
	// grep -l \"credential\|token\|url\"" ran here
	".config/gh", ".gitconfig",
	// the machine's accounts, the configuration of its users and what is mounted into it —
	// 11/19 12:58:06 "cat /etc/passwd | grep -v nologin", 12:58:10 "find /root/.config
	// /home/claude/.config -type f" (1672 bytes), 12:01:55 "ls /mnt/user-data/uploads/ && ls
	// /mnt/transcripts/" (640 bytes) and 12:01:51 "cat /container_info.json" ran here
	"/etc/passwd", "/etc/shadow", "/root/.config", "/home/claude/.config", "~/.config", "$HOME/.config",
	"/mnt/user-data", "/mnt/transcripts", "/container_info.json"}

// envDumpRe: a command that lists the environment — 11/14 08:48 "printenv | grep GIT_CONFIG"
// and 09:34 "env | grep -i \"gh_\\|github_\\|git_token\\|access_tok…\"" ran here (268 and 793
// bytes of this machine's environment). env running a command (env X=1 cmd) is not one.
var envDumpRe = regexp.MustCompile("(?m)(^|[|&;(`])\\s*(printenv\\b|env\\s*($|[|>;&)]))")

// sandboxHome is where a conversation's commands run when the configured working directory
// is einsteinOS's own: the sandbox's home, the directory the mirrors are cloned into.
var sandboxHome = "/home/claude"

// runDir is the directory a conversation's commands run from here: the configured working
// directory, unless it lies in einsteinOS's source tree — the default, the directory the
// kernel was started from — where every relative path of a command is einsteinOS itself.
// 11/17 10:21:37–10:22:01 dev_planner (03223912) ran "cat /home/adk-go/etc/einstein",
// "ls /home/adk-go/kernel/testdata/u617/" and "ls /home/adk-go/" here (pids 65–78): the
// working directory was /home/adk-go, and the tree that holds it was not a kernel place.
func (s *ShadowExecutor) runDir() string {
	for _, root := range einsteinOSRoots() {
		if s.WorkDir == root || strings.HasPrefix(s.WorkDir, root+"/") {
			_ = os.MkdirAll(sandboxHome, 0o755)
			return sandboxHome
		}
	}
	return s.WorkDir
}

// kernelStateDirs are where the kernel keeps its own state on this machine, as a command may
// name them: ~/.einsteinos (the request window, the conversation registry — main.go), and
// ~/.einsteinOS (the reference cursor, the launch probe), and the directories the
// EINSTEINOS_API_WINDOW and EINSTEINOS_CONV_REGISTRY files are put in instead. 11/17 09:50:59
// "cat /root/.einsteinos/baloon_u77b_official" and 10:21:46 "find /root/.einsteinos
// /root/.einsteinOS -type f" ran here.
func kernelStateDirs() []string {
	var out []string
	for _, name := range []string{".einsteinos", ".einsteinOS"} {
		out = append(out, "~/"+name, "$HOME/"+name, "${HOME}/"+name)
		if home, err := os.UserHomeDir(); err == nil && home != "" && home != "/" {
			out = append(out, filepath.Join(home, name))
		}
	}
	// the kernel's temporary directories (the verification trees, the diag records, the eval
	// and recheck checkouts) are all named einsteinos_* or einsteinos-* in the temp dir:
	// 11/17 10:19:37 and 10:21:22 dev_planner read /tmp/einsteinos_diag/einsteinOS_diag_4330757e.json
	// here, and 11/15 16:05–20:06 dev_unit_33 searched /tmp/einsteinos_verify_*
	tmp := strings.TrimSuffix(os.TempDir(), "/")
	out = append(out, tmp+"/einsteinos_", tmp+"/einsteinos-")
	for _, env := range []string{"EINSTEINOS_API_WINDOW", "EINSTEINOS_CONV_REGISTRY"} {
		if p := os.Getenv(env); p != "" {
			if d := filepath.Dir(p); d != "/" && d != "." {
				out = append(out, d)
			}
		}
	}
	return out
}

// touchesKernel: the kernel's place a command names, or "".
func touchesKernel(command, workDir string) string {
	places := kernelPlaces
	if wd, err := os.Getwd(); err == nil && wd != "/" && !strings.HasPrefix(workDir, wd) {
		places = append(places, wd) // where einsteinOS itself runs (11/13: /home/adk-go)
	}
	for _, root := range einsteinOSRoots() {
		if !strings.HasPrefix(workDir, root) {
			places = append(places, root)
		}
	}
	places = append(places, kernelStateDirs()...)
	for _, p := range places {
		if strings.Contains(command, p) {
			return p
		}
	}
	if envDumpRe.MatchString(command) {
		return "the environment"
	}
	if tokenSearchRe.MatchString(command) {
		return "a search for tokens"
	}
	if kernelSearchRe.MatchString(command) {
		return "a search for the kernel"
	}
	return ""
}

// tokenSearchRe: a search of this machine for a token — find by a token-like name, or grep for
// a token's form. 11/14 20:17 "find / -name \"*.token\" -o -name \"gh_tok…\"" (1158 bytes),
// 21:39 "find /root /home -name \"*.json\" … | xargs grep -l \"token\\|access_token\"" and 22:26
// "find /home /root /etc -maxdepth 4 -name \"token*\" -o -name \"*github*\" -o -name \"*ghp_*\""
// (1703 bytes) ran here. grep for a word like "token" in the unit's code is not one.
var tokenSearchRe = regexp.MustCompile(`(?i)\bfind\b[^\n|;&]*-i?name\s+["']?[^"'\s]*(token|ghp_|github|credential|netrc)` +
	`|\bgrep\b[^\n;&]*(ghp_|github_pat|access_token|x-access-token|gh_token)`)

// kernelSearchRe: a search of this machine for the kernel itself — find by its name, or
// which. 11/19 12:56:51 "find / -name 'einsteinos*' -o -name '.einsteinOS'" and 12:57:25
// "find /usr/local/go /usr/local/bin /opt -name 'adk-go' …; which adk-go" ran here.
var kernelSearchRe = regexp.MustCompile(`(?i)\bfind\b[^\n|;&]*-i?name\s+["']?[^"'\s]*(einsteinos|adk-go|adk\b)` +
	`|\b(which|whereis|type)\s+(einsteinos|adk-go|adk)\b`)

// noPush goes in front of every command run here: no git on this machine can push for a
// conversation, however it is called (a path, a subshell, a script), because every push URL
// is rewritten to a scheme git has no helper for and ssh is false. 11/13 04:26:55 a dev
// conversation's "git push" ran here with the deploy key and replaced a wip branch's history.
const noPush = "export GIT_CONFIG_COUNT=3 " +
	"GIT_CONFIG_KEY_0=url.einsteinos-no-push://.pushInsteadOf GIT_CONFIG_VALUE_0=https:// " +
	"GIT_CONFIG_KEY_1=url.einsteinos-no-push://.pushInsteadOf GIT_CONFIG_VALUE_1=git@ " +
	"GIT_CONFIG_KEY_2=url.einsteinos-no-push://.pushInsteadOf GIT_CONFIG_VALUE_2=ssh:// " +
	"GIT_SSH_COMMAND=false GIT_TERMINAL_PROMPT=0\n"

// einsteinOSModule: the first line of einsteinOS's go.mod
const einsteinOSModule = "module google.golang.org/adk"

// einsteinOSRoots: the source trees einsteinOS runs from — the directory holding its go.mod,
// found upward from the working directory and from the executable. 11/14 20:38–20:52 a dev
// conversation ran here "ls /home/adk-go/", "cat /home/adk-go/auth/credential.go" (three
// times) and "cat /home/adk-go/vikingdb/github.go" looking for how the kernel holds its
// token: the working directory alone was not the tree's root.
func einsteinOSRoots() []string {
	var starts []string
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range starts {
		for ; d != "/" && d != "." && d != ""; d = filepath.Dir(d) {
			b, err := os.ReadFile(filepath.Join(d, "go.mod"))
			if err == nil && strings.HasPrefix(string(b), einsteinOSModule) {
				if !seen[d] {
					seen[d] = true
					out = append(out, d)
				}
				break
			}
		}
	}
	return out
}
