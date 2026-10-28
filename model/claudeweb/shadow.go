package claudeweb

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

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
	// real machine can reach it but TLS is flaky — same start-guard + retry as git.
	if strings.Contains(command, "axle.axiommath.ai") || strings.Contains(command, "verify_proof") {
		result := axleExecWithRetry(command, s.WorkDir)
		if result != "" {
			log.Printf("[shadow] axle verify_proof: got %d bytes", len(result))
			return result
		}
		log.Printf("[shadow] axle verify_proof: all retries failed, letting remote result stand")
		return "" // empty → writeback skip (model.go) → remote's result stands
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

// axleExecWithRetry runs an Axle curl command on the real machine with retry.
// Same strategy as gitStartGuard: background the command, check for output after
// a timeout, kill if TLS hung. Axle proof verification can take 30-120s for complex
// proofs, so the start guard is generous (15s for first bytes) but we kill fast if
// nothing comes back (TLS hang produces zero bytes).
//
// returns the curl output on success, "" if all retries exhausted.
// axleExecWithRetry runs an Axle curl on the real machine.
//
// the real machine may have https_proxy=socks5://127.0.0.1:1080 set, which routes
// curl through a SOCKS proxy. when the proxy is down, every attempt through it hangs
// on TLS — retrying 20 times wastes 300s on a dead path.
//
// strategy: try TWO paths (direct, then proxy), 3 attempts each. if both paths time
// out, the network is genuinely down and retrying won't help. total worst case: 6
// attempts × 15s = 90s, not 300s.
func axleExecWithRetry(command string, workDir string) string {
	// try direct first (bypass proxy), then with proxy
	paths := []struct {
		name   string
		prefix string // prepended to the curl command
	}{
		{"direct", "env -u https_proxy -u http_proxy -u all_proxy "},
		{"proxy", ""}, // inherits the environment's proxy
	}

	for _, path := range paths {
		for attempt := 1; attempt <= 3; attempt++ {
			script := fmt.Sprintf("tmpout=$(mktemp)\n"+
				"%s%s >\"$tmpout\" 2>&1 &\n"+
				"cpid=$!\n"+
				"sleep 15\n"+
				"if [ ! -s \"$tmpout\" ]; then\n"+
				"  kill $cpid 2>/dev/null; wait $cpid 2>/dev/null\n"+
				"  rm -f \"$tmpout\"\n"+
				"  exit 124\n"+
				"fi\n"+
				"wait $cpid\n"+
				"code=$?\n"+
				"cat \"$tmpout\"\n"+
				"rm -f \"$tmpout\"\n"+
				"exit $code", path.prefix, command)

			pid, err := kernel.ForkExec(script, workDir)
			if err != nil {
				log.Printf("[shadow] axle %s attempt %d/3: fork error: %v", path.name, attempt, err)
				continue
			}
			kernel.Wait(pid)
			result := kernel.GetResult(pid)
			if strings.TrimSpace(result) == "" {
				log.Printf("[shadow] axle %s attempt %d/3: timeout (15s)", path.name, attempt)
				continue
			}
			// got output — check if it's a valid Axle response
			if strings.Contains(result, "\"okay\"") || strings.Contains(result, "\"error\"") ||
				strings.Contains(result, "\"timings\"") || strings.Contains(result, "\"user_error\"") {
				log.Printf("[shadow] axle %s attempt %d/3: success (%d bytes)", path.name, attempt, len(result))
				return fmt.Sprintf("[pid %d exit=0] %s", pid, result)
			}
			// got output but not a valid response
			log.Printf("[shadow] axle %s attempt %d/3: unexpected (%d bytes): %s",
				path.name, attempt, len(result), truncateCmd(result, 200))
			return fmt.Sprintf("[pid %d exit=0] %s", pid, result)
		}
		log.Printf("[shadow] axle %s: 3 attempts exhausted, trying next path", path.name)
	}
	log.Printf("[shadow] axle: both direct and proxy paths failed")
	return ""
}

func truncateCmd(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
