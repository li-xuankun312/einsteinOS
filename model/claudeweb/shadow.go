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
	if !s.Enabled || inputJSON == "" {
		return ""
	}

	for _, driver := range s.drivers {
		if driver.Match(toolName) {
			return driver.Execute(toolName, inputJSON, s.WorkDir)
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
		// theorem signature guard: on .lean files, check if the edit changes
		// theorem signatures (everything before := by). changing proof bodies
		// is fine, changing the statement is not — it breaks downstream semantics.
		if strings.HasSuffix(input.Path, ".lean") {
			oldSig := extractTheoremSig(input.OldStr)
			newSig := extractTheoremSig(input.NewStr)
			if oldSig != "" && newSig != "" && oldSig != newSig {
				log.Printf("[shadow] BLOCKED: theorem signature changed in %s\n  old: %s\n  new: %s",
					input.Path, truncateCmd(oldSig, 120), truncateCmd(newSig, 120))
				return fmt.Sprintf("[pid 0 exit=1] ERROR: you changed the theorem statement, not just the proof. "+
					"theorem signatures (name + parameters + conclusion) must not change. "+
					"the proofs under %s show how statements like this are proved.\nold sig: %s\nnew sig: %s",
					kernel.ProofReferencePath(), truncateCmd(oldSig, 200), truncateCmd(newSig, 200))
			}
		}
		escaped_old := strings.ReplaceAll(input.OldStr, "'", "'\\''")
		escaped_new := strings.ReplaceAll(input.NewStr, "'", "'\\''")
		command = fmt.Sprintf("python3 -c '\nimport sys\nwith open(\"%s\",\"r\") as f: c=f.read()\nold=\"\"\"%s\"\"\"\nnew=\"\"\"%s\"\"\"\nif old not in c:\n    print(\"old_str not found\",file=sys.stderr)\n    sys.exit(1)\nc=c.replace(old,new,1)\nwith open(\"%s\",\"w\") as f: f.write(c)\nprint(\"ok\")\n'", input.Path, escaped_old, escaped_new, input.Path)

	default:
		return ""
	}

	if command == "" {
		return ""
	}

	// Axle proof verification runs on the REAL MACHINE, not remote sandbox.
	// remote sandbox has no network access to axle.axiommath.ai.
	// every Axle request in the command is parsed like a shell would (see
	// axle_extract.go) and sent via Go http.Post — no shell, no escaping issues,
	// and a command with several curls no longer falls into the slow curl path.
	if isAxleCommand(command) {
		if set, ok := extractAxleRequests(command); ok {
			log.Printf("[shadow] axle: using Go HTTP path (%d request(s), payload=%d bytes, session=%d, csrf=%d)",
				len(set.Payloads), len(set.Payloads[0]), len(set.Session), len(set.CSRF))
			return runAxleRequests(set)
		}
		// couldn't safely parse the command — run the raw curl, bounded and killable
		log.Printf("[shadow] axle: using curl fallback (extraction failed): %s",
			truncateCmd(redactAxle(strings.Join(strings.Fields(command), " ")), 300))
		result := axleExecWithRetryCurl(command, s.WorkDir)
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
		kernel.GitTwinSync(command, s.WorkDir)
		log.Printf("[shadow] skip git (remote has full output)")
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

	pid, err := kernel.ForkExec(command, s.WorkDir)
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
	return false
}

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
	return false
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
)

// axleScriptGuard: a command with no curl in it is a script (for example a loop
// calling /tmp/axle.sh on every file). It may legitimately run for minutes, and
// retrying it cannot change a deterministic failure. Sized for ~a dozen files at
// Axle's own per-proof limit.
const axleScriptGuard = 10 * time.Minute

// redactAxle removes the session cookie and csrf token before a command is logged.
func redactAxle(cmd string) string {
	cmd = sessionRedact.ReplaceAllString(cmd, "provertools_session=<redacted>")
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
