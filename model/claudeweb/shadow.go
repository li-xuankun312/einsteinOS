package claudeweb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
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
					"if you cannot prove it, use sorry instead.\nold sig: %s\nnew sig: %s",
					truncateCmd(oldSig, 200), truncateCmd(newSig, 200))
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
	// extract JSON payload from the curl command and call Axle via Go http.Post
	// to avoid shell escaping issues ("Request body must be valid JSON").
	if strings.Contains(command, "axle.axiommath.ai") || strings.Contains(command, "verify_proof") {
		payload, session, csrf := extractAxlePayload(command)
		if payload != "" {
			// validate: payload should be parseable JSON
			if !json.Valid([]byte(payload)) {
				log.Printf("[shadow] axle: extracted payload is not valid JSON (%d bytes): %s",
					len(payload), truncateCmd(payload, 150))
				// fall through to curl fallback
				payload = ""
			}
		}
		if payload != "" {
			log.Printf("[shadow] axle: using Go HTTP path (payload=%d bytes, session=%d, csrf=%d)",
				len(payload), len(session), len(csrf))
			result := axleExecWithRetry(payload, session, csrf)
			if result != "" {
				log.Printf("[shadow] axle verify_proof: got %d bytes", len(result))
				return result
			}
			log.Printf("[shadow] axle verify_proof: all retries failed, letting remote result stand")
			return ""
		}
		// couldn't extract payload — try raw curl as fallback
		log.Printf("[shadow] axle: using curl fallback (extraction failed)")
		result := axleExecWithRetryCurl(command, s.WorkDir)
		if result != "" {
			log.Printf("[shadow] axle verify_proof (curl fallback): got %d bytes", len(result))
			return result
		}
		log.Printf("[shadow] axle verify_proof: all retries failed, letting remote result stand")
		return ""
	}

	// don't shadow-exec git commands locally.
	// remote sandbox runs them and gets full output; local shadow truncates
	// stdout to 2000 chars, and writeback overrides the remote's complete
	// result with the truncated version. this causes children to re-read
	// the same file 10+ times in a loop trying to get untruncated content.
	// git state is shared via GitHub — no local side-effects needed.
	if isGitCommand(command) {
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

	pid, err := kernel.ForkExec(command, s.WorkDir)
	if err != nil {
		return fmt.Sprintf("[pid -1] fork: %v", err)
	}

	log.Printf("[pid %d] exec: %s", pid, truncateCmd(command, 120))

	childPid, exitCode, err := kernel.Wait(pid)
	if err != nil {
		return fmt.Sprintf("[pid %d] wait: %v", pid, err)
	}

	result := kernel.GetResult(childPid)
	if len(result) > 2000 {
		result = result[:2000] + "\n..."
	}

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
		return fmt.Sprintf("[pid %d exit=%d] %s\nstderr: %s", childPid, exitCode, result, truncateCmd(errStr, 500))
	}
	return fmt.Sprintf("[pid %d exit=0] %s", childPid, result)
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

// extractAxlePayload parses the child's curl command to extract the JSON body,
// session cookie, and CSRF token. returns ("","","") if it can't parse.
func extractAxlePayload(command string) (payload, session, csrf string) {
	// extract --data-raw or -d payload.
	// JSON may contain single quotes (Lean syntax: 'a), so find the closing
	// quote from the END: the last }' or }" in the command is the end of the payload.
	for _, prefix := range []string{"--data-raw '", "--data-raw \"", "-d '", "-d \""} {
		idx := strings.Index(command, prefix)
		if idx < 0 {
			continue
		}
		start := idx + len(prefix)
		quote := string(prefix[len(prefix)-1])
		// find last occurrence of }<quote> (JSON object end + shell close)
		closer := "}" + quote
		end := strings.LastIndex(command[start:], closer)
		if end < 0 {
			// maybe no trailing }, try just the last quote
			end = strings.LastIndex(command[start:], quote)
			if end < 0 {
				continue
			}
		} else {
			end += 1 // include the }
		}
		payload = command[start : start+end]
		break
	}
	// extract session cookie
	if idx := strings.Index(command, "provertools_session="); idx >= 0 {
		rest := command[idx+len("provertools_session="):]
		end := strings.IndexAny(rest, "'; \"")
		if end > 0 {
			session = rest[:end]
		}
	}
	// extract CSRF
	if idx := strings.Index(command, "x-browser-api-csrf: "); idx >= 0 {
		rest := command[idx+len("x-browser-api-csrf: "):]
		end := strings.IndexAny(rest, "' \"")
		if end > 0 {
			csrf = rest[:end]
		}
	}
	return
}

// axleExecWithRetry calls Axle via Go http.Post — no shell, no escaping issues.
func axleExecWithRetry(payload, session, csrf string) string {
	type round struct {
		name  string
		proxy string // "" = use env proxy, "direct" = no proxy
		tries int
		guard time.Duration
	}
	rounds := []round{
		{"proxy", "", 5, 15 * time.Second},
		{"direct", "direct", 1, 15 * time.Second},
		{"proxy", "", 5, 20 * time.Second},
	}

	for _, r := range rounds {
		for attempt := 1; attempt <= r.tries; attempt++ {
			t0 := time.Now()
			result := axleHTTPOnce(payload, session, csrf, r.proxy == "direct", r.guard)
			elapsed := time.Since(t0)
			trimmed := strings.TrimSpace(result)
			if trimmed == "" {
				log.Printf("[shadow] axle %s %d/%d: timeout (%s, took %s)", r.name, attempt, r.tries, r.guard, elapsed.Round(time.Second))
				// if request returned instantly (connection refused, not timeout),
				// wait to avoid 11 attempts in 1 second
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
			return fmt.Sprintf("[pid 0 exit=0] %s", result)
		}
	}
	log.Printf("[shadow] axle: all rounds exhausted (proxy 5 + direct 1 + proxy 5)")
	return ""
}

// axleHTTPOnce sends a single Axle verification request via Go net/http.
func axleHTTPOnce(payload, session, csrf string, directNoProxy bool, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var transport *http.Transport
	if directNoProxy {
		transport = &http.Transport{
			Proxy: nil, // no proxy
			TLSHandshakeTimeout: timeout,
		}
	} else {
		// use default transport which inherits env proxy (socks5://127.0.0.1:1080)
		transport = &http.Transport{
			TLSHandshakeTimeout: timeout,
		}
	}
	client := &http.Client{Transport: transport, Timeout: timeout}

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://axle.axiommath.ai/api/v1/verify_proof",
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

// axleExecWithRetryCurl is the fallback: runs the raw curl command via shell.
// used when extractAxlePayload can't parse the child's curl command.
// axleExecWithRetryCurl is the fallback when extractAxlePayload can't parse the curl.
// runs the raw curl command via exec.CommandContext with retry.
func axleExecWithRetryCurl(command string, workDir string) string {
	type round struct {
		name     string
		envUnset bool
		tries    int
		guard    time.Duration
	}
	rounds := []round{
		{"proxy", false, 5, 15 * time.Second},
		{"direct", true, 1, 15 * time.Second},
		{"proxy", false, 5, 20 * time.Second},
	}

	for _, r := range rounds {
		for attempt := 1; attempt <= r.tries; attempt++ {
			t0 := time.Now()
			result := axleExecOnce(command, workDir, r.envUnset, r.guard)
			elapsed := time.Since(t0)
			trimmed := strings.TrimSpace(result)
			if trimmed == "" {
				log.Printf("[shadow] axle-curl %s %d/%d: timeout (%s, took %s)", r.name, attempt, r.tries, r.guard, elapsed.Round(time.Second))
				if elapsed < 3*time.Second {
					time.Sleep(3*time.Second - elapsed)
				}
				continue
			}
			if strings.HasPrefix(trimmed, "Usage: curl") || strings.HasPrefix(trimmed, "<!DOCTYPE") ||
				strings.HasPrefix(trimmed, "<html") {
				log.Printf("[shadow] axle-curl %s %d/%d: garbage (%d bytes)", r.name, attempt, r.tries, len(result))
				continue
			}
			log.Printf("[shadow] axle-curl %s %d/%d: got response (%d bytes)", r.name, attempt, r.tries, len(result))
			return fmt.Sprintf("[pid 0 exit=0] %s", result)
		}
	}
	log.Printf("[shadow] axle-curl: all rounds exhausted")
	return ""
}

// axleExecOnce runs a curl command with a Go-native timeout. no shell sleep, no ForkExec.
func axleExecOnce(command string, workDir string, unsetProxy bool, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	if workDir != "" {
		cmd.Dir = workDir
	}
	// inherit env, optionally strip proxy
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

	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return "" // timeout — caller logs it
	}
	if err != nil && len(out) == 0 {
		return "" // failed with no output
	}
	return string(out)
}

func truncateCmd(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
