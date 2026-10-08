package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/chr_drv"
	"google.golang.org/adk/v2/fs"
	"google.golang.org/adk/v2/kernel"
	"google.golang.org/adk/v2/model/claudeweb"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
)

type appConfig struct {
	baseURL   string
	orgID     string
	cookie    string
	modelName string
	effort    string
	workDir   string
}

var cfg *appConfig

func main() {
	cfg = &appConfig{
		baseURL:   envOr("CLAUDE_WEB_BASE_URL", "https://c.aimonkey.plus"),
		orgID:     envRequired("CLAUDE_WEB_ORG_ID"),
		cookie:    envRequired("CLAUDE_WEB_COOKIE"),
		modelName: envOr("CLAUDE_WEB_MODEL", "claude-opus-4-6"),
		effort:    envOr("CLAUDE_WEB_EFFORT", "medium"),
		workDir:   envOr("CLAUDE_WEB_WORKDIR", mustGetwd()),
	}

	fmt.Println("einsteinOS v0.11-go booting...")

	kernel.Init()
	kernel.SyscallInit()
	initTask := kernel.GetCurrent()
	kernel.SetPwd(initTask, cfg.workDir)
	kernel.SetTokenBudget(0, 500000)
	kernel.SetContextLimit(0, 200000)
	kernel.SetEffort(0, kernel.EFFORT_MEDIUM)
	kernel.SetAgentCaps(0, kernel.CAP_ALL)
	cowCtx0 := kernel.NewCOWContext(0)
	cowCtx0.Append(fmt.Sprintf("system: model=%s effort=%s", cfg.modelName, cfg.effort))
	chr_drv.GetAgentTty(0)
	kernel.GlobalSessionTable.Create("main", 0, "", cfg.modelName)

	fmt.Printf("  task[0]: pid=0 budget=500000 caps=all ctx=%d pages\n", cowCtx0.Len())
	fmt.Printf("  tty0: cooked | model: %s | effort: %s\n", cfg.modelName, cfg.effort)
	fmt.Printf("  cwd: %s\n", cfg.workDir)
	fmt.Println("  ready.")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	var mainLLM *claudeweb.Model
	var mainRunner *runner.Runner
	var mainSessID string
	mainLLM, mainRunner, mainSessID = newConversation("init")

	for {
		fmt.Print("You > ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		switch {
		case input == "/quit" || input == "/exit" || input == "/q":
			fmt.Println("Bye!")
			return

		case input == "/ps":
			kernel.Ps()
			fmt.Println()

		case input == "/new":
			mainLLM, mainRunner, mainSessID = newConversation("init")
			fmt.Println("  ✓ Main conversation reset")
			fmt.Println()

		case strings.HasPrefix(input, "/spawn "):
			prompt := strings.TrimSpace(strings.TrimPrefix(input, "/spawn "))
			if prompt == "" {
				fmt.Println("  Usage: /spawn <prompt>")
				continue
			}
			pid, err := kernel.Fork(prompt, cfg.workDir)
			if err != nil {
				fmt.Printf("  fork() failed: %v\n", err)
				continue
			}
			fmt.Printf("\n┌─ [PID=%d] fork() ok, running...\n", pid)
			go runChildProcess(pid, prompt)
			fmt.Printf("│  (backgrounded — /wait %d to collect result)\n", pid)
			fmt.Printf("└─\n\n")

		case strings.HasPrefix(input, "/wait"):
			args := strings.TrimSpace(strings.TrimPrefix(input, "/wait"))
			var waitPid int64
			if args == "" {
				waitPid = -1
			} else {
				n, err := strconv.ParseInt(args, 10, 64)
				if err != nil {
					fmt.Printf("  Usage: /wait [pid]\n")
					continue
				}
				waitPid = n
			}
			fmt.Printf("  waitpid(%d)...\n", waitPid)
			childPid, exitInfo, err := kernel.AgentWait(waitPid)
			if err != nil {
				fmt.Printf("  waitpid error: %v\n", err)
			} else {
				fmt.Printf("  PID=%d exited: %s (tokens=%d)\n",
					childPid, kernel.ExitCodeName(exitInfo.Code), exitInfo.Tokens)
				if exitInfo.Reason != "" {
					fmt.Printf("  reason: %s\n", exitInfo.Reason)
				}
				if exitInfo.Result != "" {
					fmt.Printf("  Result:\n%s\n", exitInfo.Result)
				}
			}
			fmt.Println()

		case strings.HasPrefix(input, "/kill "):
			args := strings.TrimSpace(strings.TrimPrefix(input, "/kill "))
			n, err := strconv.ParseInt(args, 10, 64)
			if err != nil {
				fmt.Println("  Usage: /kill <pid>")
				continue
			}
			if err := kernel.Kill(n, kernel.SIG_KILL); err != nil {
				fmt.Printf("  kill() error: %v\n", err)
			} else {
				fmt.Printf("  kill(%d, SIGKILL) sent\n", n)
			}
			fmt.Println()

		case input == "/top":
			fmt.Print(kernel.TokenPs())
			fmt.Println()

		case strings.HasPrefix(input, "/budget "):
			parts := strings.Fields(input)
			if len(parts) != 3 {
				fmt.Println("  Usage: /budget <pid> <tokens>")
				continue
			}
			pid, _ := strconv.ParseInt(parts[1], 10, 64)
			budget, _ := strconv.ParseInt(parts[2], 10, 64)
			kernel.SetTokenBudget(pid, budget)
			fmt.Printf("  pid %d budget = %d tokens\n\n", pid, budget)

		case strings.HasPrefix(input, "/effort "):
			parts := strings.Fields(input)
			if len(parts) != 3 {
				fmt.Println("  Usage: /effort <pid> l|m|h")
				continue
			}
			pid, _ := strconv.ParseInt(parts[1], 10, 64)
			var eff uint8 = kernel.EFFORT_MEDIUM
			switch parts[2] {
			case "l", "low":
				eff = kernel.EFFORT_LOW
			case "h", "high":
				eff = kernel.EFFORT_HIGH
			}
			kernel.SetEffort(pid, eff)
			fmt.Printf("  pid %d effort = %d\n\n", pid, eff)

		case strings.HasPrefix(input, "/pipe "):
			parts := strings.Fields(input)
			if len(parts) != 3 {
				fmt.Println("  Usage: /pipe <from_pid> <to_pid>")
				continue
			}
			pipeID := kernel.CreateAgentPipe()
			fmt.Printf("  pipe %d created\n\n", pipeID)

		case strings.HasPrefix(input, "/exec "):
			rest := strings.TrimPrefix(input, "/exec ")
			parts := strings.SplitN(rest, " ", 2)
			if len(parts) != 2 {
				fmt.Println("  Usage: /exec <pid> <new system prompt>")
				continue
			}
			pid, _ := strconv.ParseInt(parts[0], 10, 64)
			ret := kernel.AgentExec(pid, &kernel.AgentImage{
				SystemPrompt: parts[1],
			})
			if ret < 0 {
				fmt.Printf("  exec failed: %d\n\n", ret)
			} else {
				fmt.Printf("  pid %d: system prompt swapped\n\n", pid)
			}

		case strings.HasPrefix(input, "/tty "):
			parts := strings.Fields(input)
			if len(parts) != 3 {
				fmt.Println("  Usage: /tty <pid> raw|cooked")
				continue
			}
			pid, _ := strconv.ParseInt(parts[1], 10, 64)
			mode := chr_drv.AGENT_TTY_COOKED
			if parts[2] == "raw" {
				mode = chr_drv.AGENT_TTY_RAW
			}
			chr_drv.SetAgentTtyMode(pid, mode)
			fmt.Printf("  pid %d tty mode = %s\n\n", pid, parts[2])

		case input == "/cache":
			total, used, dirty := kernel.GlobalPromptCache.Stats()
			fmt.Printf("  prompt cache: %d/%d tokens used, %d dirty\n\n", used, total, dirty)

		case strings.HasPrefix(input, "/ctx"):
			args := strings.TrimSpace(strings.TrimPrefix(input, "/ctx"))
			if args == "" {
				fmt.Println("  Usage: /ctx <pid>")
				continue
			}
			pid, _ := strconv.ParseInt(args, 10, 64)
			ctx := kernel.GetCOWContext(pid)
			if ctx == nil {
				fmt.Printf("  pid %d: no context\n\n", pid)
			} else {
				fmt.Printf("  pid %d: %d pages, %d shared, ~%d tokens\n\n",
					pid, ctx.Len(), ctx.SharedPages(), ctx.TotalTokens())
			}

		case strings.HasPrefix(input, "/artifact "):
			rest := strings.TrimSpace(strings.TrimPrefix(input, "/artifact "))
			switch {
			case rest == "ls":
				names := fs.GlobalArtifactFS.List()
				if len(names) == 0 {
					fmt.Println("  (no artifacts)")
				}
				for name, inum := range names {
					atype, size, nlinks, _ := fs.GlobalArtifactFS.Stat(name)
					fmt.Printf("  %-4d %-8s %6d bytes  nlinks=%d  %s\n",
						inum, artifactTypeName(int(atype)), size, nlinks, name)
				}
				fmt.Println()
			case strings.HasPrefix(rest, "cat "):
				name := strings.TrimPrefix(rest, "cat ")
				inode := fs.GlobalArtifactFS.Open(name)
				if inode == nil {
					fmt.Printf("  artifact %q not found\n\n", name)
				} else {
					data := inode.Read()
					fmt.Printf("%s\n\n", string(data))
				}
			default:
				fmt.Println("  Usage: /artifact ls | /artifact cat <name>")
			}

		default:
			fmt.Println()
			osRunPrompt(mainLLM, mainRunner, mainSessID, input)
			fmt.Println()
		}
	}
}

func runChildProcess(pid int64, prompt string) {
	llm, r, sessID := newConversation(fmt.Sprintf("pid_%d", pid))
	kernel.SetConvID(pid, llm.ConvID())
	kernel.SetupBudgetAlarm(pid)
	kernel.InheritCaps(0, pid)
	kernel.GlobalSessionTable.Create(fmt.Sprintf("pid_%d", pid), pid, llm.ConvID(), cfg.modelName)

	filtered, ok := chr_drv.AgentTtyProcess(pid, prompt)
	if !ok {
		kernel.AgentExit(pid, &kernel.AgentExitInfo{
			Code:   kernel.EXIT_FILTER_BLOCK,
			Reason: "input blocked by tty filter",
			Result: filtered,
		})
		log.Printf("[PID=%d] blocked by tty filter", pid)
		return
	}

	cowCtx := kernel.GetCOWContext(pid)
	if cowCtx != nil {
		cowCtx.Append(fmt.Sprintf("user: %s", filtered))
	}

	var result strings.Builder
	ctx := context.Background()
	t := kernel.GetTask(pid)
	if t != nil {
		ctx = kernel.TaskCtx(t)
	}

	var totalTokens int64
	msg := genai.NewContentFromText(filtered, "user")
	for event, err := range r.Run(ctx, "user", sessID, msg, agent.RunConfig{}) {
		if err != nil {
			result.WriteString(fmt.Sprintf("[Error] %v", err))
			break
		}

		if pending := kernel.AgentCheckSignals(pid); pending != 0 {
			result.WriteString("[interrupted by signal]")
			break
		}

		if event.Content != nil {
			for _, part := range event.Content.Parts {
				if part.Text != "" {
					result.WriteString(part.Text)
					chunkTokens := int64(len(part.Text) / 4)
					totalTokens += chunkTokens
					kernel.AccountTokens(pid, chunkTokens)

					if cowCtx != nil {
						kernel.AccountContext(pid, cowCtx.TotalTokens()+totalTokens)
					}
				}
			}
		}
	}

	resultStr := result.String()
	kernel.SetResult(pid, resultStr)

	if cowCtx != nil {
		cowCtx.Append(fmt.Sprintf("assistant: %s", resultStr))
	}

	kernel.GlobalPromptCache.Put(
		fmt.Sprintf("pid_%d_conv_%s", pid, llm.ConvID()),
		resultStr,
		totalTokens,
	)

	artName := fmt.Sprintf("pid_%d_result", pid)
	fs.GlobalArtifactFS.Create(artName, fs.ARTIFACT_RESULT, pid, []byte(resultStr))

	exitCode := kernel.EXIT_SUCCESS
	exitReason := ""
	if t != nil && t.TokenUsed > t.TokenBudget && t.TokenBudget > 0 {
		exitCode = kernel.EXIT_BUDGET_EXCEED
		exitReason = fmt.Sprintf("used %d > budget %d", t.TokenUsed, t.TokenBudget)
	}

	var ctxTokens int64
	if cowCtx != nil {
		ctxTokens = cowCtx.TotalTokens()
	}
	kernel.AgentExit(pid, &kernel.AgentExitInfo{
		Code:    exitCode,
		Reason:  exitReason,
		Result:  resultStr,
		Tokens:  totalTokens,
		Context: ctxTokens,
	})
	log.Printf("[PID=%d] exited code=%s tokens=%d", pid, kernel.ExitCodeName(exitCode), totalTokens)
}

func osRunPrompt(llm *claudeweb.Model, r *runner.Runner, sessID string, input string) {
	pid := int64(kernel.GetCurrent().Pid)

	filtered, ok := chr_drv.AgentTtyProcess(pid, input)
	if !ok {
		fmt.Printf("[tty filter blocked]\n")
		return
	}

	if !kernel.HasCap(pid, kernel.CAP_TOOL_USE) {
		fmt.Printf("[no capability: tool_use]\n")
		return
	}

	cowCtx := kernel.GetCOWContext(pid)
	if cowCtx != nil {
		cowCtx.Append(fmt.Sprintf("user: %s", filtered))
	}

	ctx := context.Background()
	t := kernel.GetTask(pid)
	if t != nil {
		ctx = kernel.TaskCtx(t)
	}

	var result strings.Builder
	var totalTokens int64
	msg := genai.NewContentFromText(filtered, "user")
	for event, err := range r.Run(ctx, "user", sessID, msg, agent.RunConfig{}) {
		if err != nil {
			fmt.Printf("[Error] %v\n", err)
			break
		}
		if event.Content != nil {
			for _, part := range event.Content.Parts {
				if part.Text != "" {
					fmt.Print(part.Text)
					result.WriteString(part.Text)
					chunkTokens := int64(len(part.Text) / 4)
					totalTokens += chunkTokens
					kernel.AccountTokens(pid, chunkTokens)
				}
			}
		}
	}
	fmt.Println()

	resultStr := result.String()
	if cowCtx != nil {
		cowCtx.Append(fmt.Sprintf("assistant: %s", resultStr))
		kernel.AccountContext(pid, cowCtx.TotalTokens())
	}

	cacheKey := fmt.Sprintf("turn_%d_%d", pid, kernel.GetCurrent().Stime)
	kernel.GlobalPromptCache.Put(cacheKey, resultStr, totalTokens)

	sess := kernel.GlobalSessionTable.GetByPid(pid)
	if sess != nil {
		kernel.GlobalSessionTable.Touch(sess.ID, totalTokens)
	}
}

func newConversation(name string) (*claudeweb.Model, *runner.Runner, string) {
	client := claudeweb.NewClient(claudeweb.ClientConfig{
		BaseURL: cfg.baseURL,
		OrgID:   cfg.orgID,
		Cookie:  cfg.cookie,
	})
	llm := claudeweb.NewModel(client, cfg.modelName, cfg.effort)
	llm.Shadow = &claudeweb.ShadowExecutor{
		WorkDir: cfg.workDir,
		Enabled: true,
		Log:     claudeweb.NewShadowLog(cfg.workDir),
	}
	a, err := llmagent.New(llmagent.Config{
		Name:        fmt.Sprintf("claude_%s", name),
		Model:       llm,
		Description: "Claude with shadow execution",
		Instruction: "You are a helpful assistant.",
	})
	if err != nil {
		log.Fatalf("agent create failed: %v", err)
	}
	sessSvc := session.InMemoryService()
	r, err := runner.New(runner.Config{
		AppName:        name,
		Agent:          a,
		SessionService: sessSvc,
	})
	if err != nil {
		log.Fatalf("runner create failed: %v", err)
	}
	sess, err := sessSvc.Create(context.Background(), &session.CreateRequest{
		AppName: name,
		UserID:  "user",
	})
	if err != nil {
		log.Fatalf("session create failed: %v", err)
	}
	return llm, r, sess.Session.ID()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envRequired(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("set %s", key)
	}
	return v
}

func mustGetwd() string {
	d, _ := os.Getwd()
	return d
}

func artifactTypeName(t int) string {
	switch fs.ArtifactType(t) {
	case fs.ARTIFACT_FILE:
		return "file"
	case fs.ARTIFACT_MEMORY:
		return "memory"
	case fs.ARTIFACT_RESULT:
		return "result"
	case fs.ARTIFACT_LOG:
		return "log"
	default:
		return "?"
	}
}


