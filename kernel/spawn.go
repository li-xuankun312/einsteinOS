package kernel

// spawn.go — a conversation starting other conversations, as a kernel syscall.
//
// The child never opens a web page or calls an API. It runs a bash command
//
//	einsteinos spawn work <<'EOF'
//	...prompt for the new conversation...
//	EOF
//
// The claudeweb layer recognises the command (EinsteinCommandFrom), the kernel
// executes it HERE — policy check, fork, parent edge in the registry — and the
// answer goes back through the same channel Axle's answers use: the local
// result that is written back to the child. The child gets an ADDRESS
// immediately and carries on; later it asks `einsteinos conv <addr>` for the
// state of that conversation and of everything that one started.
//
// Two kinds of conversation, treated differently on purpose:
//
//   - work: writes code or theory. Costs a conversation (and every round of it)
//     out of the account quota, so it is bounded by depth / width / tree size.
//   - judge: answers questions and returns. ONE judge conversation can answer
//     many questions at once (`spawn judge` takes a list), so a child with fifty
//     small decisions to make asks once, not fifty times. Judge and plan
//     conversations are leaves: they never start conversations themselves.
//
// "Whether to split" is the kernel's rule (numbers: dev_node_needs_split);
// "how to split" is a plan conversation's job (dev_plan_io.go) — the one doing
// the work is the worst judge of how much work is too much.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SpawnPolicy bounds the tree. Quota is the tightest constraint in this system
// (the Strata run spent 264 of 354 dev minutes waiting for it), so every spawn
// is a deliberate spend.
type SpawnPolicy struct {
	MaxDepth    int // parent edges allowed above a new conversation
	MaxChildren int // conversations one conversation may start
	MaxTree     int // all descendants of one kernel-forked conversation
}

func DefaultSpawnPolicy() SpawnPolicy {
	p := SpawnPolicy{MaxDepth: 2, MaxChildren: 6, MaxTree: 12}
	if v, err := strconv.Atoi(os.Getenv("EINSTEINOS_SPAWN_DEPTH")); err == nil && v >= 0 {
		p.MaxDepth = v
	}
	if v, err := strconv.Atoi(os.Getenv("EINSTEINOS_SPAWN_CHILDREN")); err == nil && v >= 0 {
		p.MaxChildren = v
	}
	if v, err := strconv.Atoi(os.Getenv("EINSTEINOS_SPAWN_TREE")); err == nil && v >= 0 {
		p.MaxTree = v
	}
	return p
}

var spawnKinds = map[string]string{"work": "spawn_work", "judge": "spawn_judge", "plan": "spawn_plan"}

// CheckSpawn decides whether parentConv may start a conversation of kind.
func CheckSpawn(parentConv, kind string, pol SpawnPolicy) error {
	if _, ok := spawnKinds[kind]; !ok {
		return fmt.Errorf("unknown kind %q (work | judge | plan)", kind)
	}
	if parentConv == "" {
		return fmt.Errorf("no parent conversation")
	}
	convRegistryMu.Lock()
	rec, known := mergedConvLocked(parentConv)
	convRegistryMu.Unlock()
	role := rec.Role
	if pid := FindPidByConvID(parentConv); pid > 0 {
		if m := getMeta(pid); m != nil && m.Role != "" {
			role = m.Role
		}
	}
	if !known && role == "" {
		return fmt.Errorf("conversation %s is not registered: only a conversation the kernel started may start others", shortID(parentConv))
	}
	{
		if k := convKindForRole(role); k != "work" {
			return fmt.Errorf("a %s conversation (%s) does not start conversations", k, role)
		}
		if rec.Kind == "judge" || rec.Kind == "plan" {
			return fmt.Errorf("a %s conversation does not start conversations", rec.Kind)
		}
	}
	if d := ConvDepth(parentConv) + 1; d > pol.MaxDepth {
		return fmt.Errorf("depth %d exceeds the limit %d", d, pol.MaxDepth)
	}
	node := ConvManifest(parentConv)
	if len(node.Children) >= pol.MaxChildren {
		return fmt.Errorf("this conversation already started %d (limit %d)", len(node.Children), pol.MaxChildren)
	}
	if n := len(ConvManifest(convTreeRoot(parentConv)).Flatten()); n >= pol.MaxTree {
		return fmt.Errorf("the tree already has %d conversations below its root (limit %d)", n, pol.MaxTree)
	}
	return nil
}

// SpawnResult is what the starter gets back at once.
type SpawnResult struct {
	Addr string `json:"addr"`
	Kind string `json:"kind"`
	Role string `json:"role"`
	Note string `json:"note,omitempty"`
}

var (
	spawnMu    sync.Mutex
	spawnCache = map[string]SpawnResult{} // idempotency: parent|kind|prompt -> result

	// the three seams that need a booted kernel and a model; variables so the
	// policy, registry and command handling can be tested without either.
	spawnForkFn = func(role, prompt string) (int64, error) { return ForkWithRole(prompt, ".", role) }
	spawnConvID = func(pid int64) string { return getMeta(pid).ConvID }
	spawnWaitFn = func(pid int64) { Wait(pid) }
	spawnFinal  = func(pid int64) string { return GetResult(pid) }
	spawnPollIn = 200 * time.Millisecond
	spawnPollTo = 45 * time.Second
)

func spawnKey(parent, kind, prompt string) string {
	h := sha256.Sum256([]byte(parent + "\x00" + kind + "\x00" + prompt))
	return hex.EncodeToString(h[:8])
}

// SpawnConv starts a conversation under parentConv and returns its address.
// The same request from the same parent returns the same address: a replayed
// tool call (SSE recovery re-runs rounds) must not start a second one.
func SpawnConv(parentConv, kind, prompt string) (SpawnResult, error) {
	spawnMu.Lock()
	defer spawnMu.Unlock()
	key := spawnKey(parentConv, kind, prompt)
	if r, ok := spawnCache[key]; ok {
		r.Note = "already started by this exact request"
		return r, nil
	}
	if err := CheckSpawn(parentConv, kind, DefaultSpawnPolicy()); err != nil {
		return SpawnResult{}, err
	}
	role := spawnKinds[kind]
	pid, err := spawnForkFn(role, prompt)
	if err != nil {
		return SpawnResult{}, fmt.Errorf("fork: %w", err)
	}
	// the model layer publishes the conversation id a moment after the fork starts
	var addr string
	for deadline := time.Now().Add(spawnPollTo); time.Now().Before(deadline); time.Sleep(spawnPollIn) {
		if addr = spawnConvID(pid); addr != "" {
			break
		}
	}
	if addr == "" {
		return SpawnResult{}, fmt.Errorf("the new conversation did not report an address within %s", spawnPollTo)
	}
	RegisterConvChild(parentConv, addr, "", role, kind, FindPidByConvID(parentConv))
	res := SpawnResult{Addr: addr, Kind: kind, Role: role}
	spawnCache[key] = res

	// nobody Waits on a spawned conversation but this watcher. The functions are
	// read here, under spawnMu, not inside the goroutine: it outlives this call.
	waitFn, finalFn := spawnWaitFn, spawnFinal
	go func() {
		waitFn(pid)
		SetConvResult(addr, finalFn(pid))
		ConvDone(addr, "done")
	}()
	return res, nil
}

// ---- batched judgement --------------------------------------------------------------

// JudgeItem is one question for a judge conversation.
type JudgeItem struct {
	ID       string
	Question string
}

// JudgeAnswer is one verdict.
type JudgeAnswer struct {
	ID      string `json:"id"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason,omitempty"`
}

// BuildJudgePrompt asks ONE conversation to answer every item. context is
// shared background; items are numbered 1..n.
func BuildJudgePrompt(context string, items []JudgeItem) string {
	var b strings.Builder
	b.WriteString("[TASK] answer EVERY question below. you are a judge, not a developer: do not write or change code,\n")
	b.WriteString("do not start other conversations, do not search for more work. read what you need, decide, answer.\n\n")
	if strings.TrimSpace(context) != "" {
		b.WriteString("[CONTEXT]\n" + strings.TrimSpace(context) + "\n\n")
	}
	b.WriteString(fmt.Sprintf("[QUESTIONS] (%d)\n", len(items)))
	for i, it := range items {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, strings.TrimSpace(it.Question)))
	}
	b.WriteString("\n[OUTPUT FORMAT]\none line per question, in order, exactly:\n<number>: <VERDICT>: <one-line reason>\n")
	b.WriteString("VERDICT is one word in capitals (PASS, FAIL, YES, NO, or a label the question asked for).\nnothing else after the last answer.\n")
	b.WriteString(DoneInstruction())
	return b.String()
}

var judgeLineRe = regexp.MustCompile(`^\s*(\d+)\s*[:.)]\s*([A-Za-z_]+)\s*(?:[:\-–]\s*(.*))?$`)

// ParseJudgeAnswers maps a judge's text back onto the items. A question with no
// answer line is NO_ANSWER — never silently PASS.
func ParseJudgeAnswers(text string, items []JudgeItem) []JudgeAnswer {
	got := map[int]JudgeAnswer{}
	for _, line := range strings.Split(text, "\n") {
		m := judgeLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		if n < 1 || n > len(items) {
			continue
		}
		if _, dup := got[n]; dup {
			continue // first answer to a question stands
		}
		got[n] = JudgeAnswer{ID: items[n-1].ID, Verdict: strings.ToUpper(m[2]), Reason: strings.TrimSpace(m[3])}
	}
	out := make([]JudgeAnswer, len(items))
	for i, it := range items {
		if a, ok := got[i+1]; ok {
			out[i] = a
		} else {
			out[i] = JudgeAnswer{ID: it.ID, Verdict: "NO_ANSWER"}
		}
	}
	return out
}

// ---- the `einsteinos` command ----------------------------------------------------------

// EinsteinCommandFrom returns the command line if this tool call is an
// `einsteinos …` command to the kernel.
func EinsteinCommandFrom(toolName, inputJSON string) (string, bool) {
	switch strings.ToLower(toolName) {
	case "bash_tool", "bash", "repl", "terminal", "shell", "execute_command", "run_command":
	default:
		return "", false
	}
	var in struct {
		Command string `json:"command"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal([]byte(inputJSON), &in); err != nil {
		return "", false
	}
	cmd := in.Command
	if cmd == "" {
		cmd = in.Code
	}
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, "einsteinos ") || cmd == "einsteinos" {
		return cmd, true
	}
	return "", false
}

// splitArgs splits a command line shell-style: whitespace separates, single and
// double quotes group. No expansion.
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	started := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, started = r, true
		case r == ' ' || r == '\t':
			if started || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if started || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

var heredocRe = regexp.MustCompile(`<<-?\s*(['"]?)([A-Za-z_][A-Za-z0-9_]*)(['"]?)\s*$`)

// splitCommand returns the first line's words and the heredoc body, if any.
func splitCommand(cmd string) (args []string, body string) {
	cmd = strings.ReplaceAll(cmd, "\r\n", "\n")
	lines := strings.Split(cmd, "\n")
	first := lines[0]
	tag := ""
	if m := heredocRe.FindStringSubmatchIndex(first); m != nil {
		sub := heredocRe.FindStringSubmatch(first)
		tag = sub[2]
		first = strings.TrimSpace(first[:m[0]])
	}
	args = splitArgs(first)
	if tag == "" {
		return args, ""
	}
	var b []string
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == tag {
			break
		}
		b = append(b, l)
	}
	return args, strings.Join(b, "\n")
}

func jsonOut(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}

func errOut(msg string) string { return jsonOut(map[string]string{"error": msg}) }

const einsteinHelp = `einsteinos spawn work <<'EOF'      start a conversation that does work; the heredoc is its prompt. returns {"addr":...} at once.
einsteinos spawn judge <<'EOF'     ONE conversation answers many questions: put context, then one question per line starting with "- ".
einsteinos spawn plan <<'EOF'      a conversation that only decides how to split something.
einsteinos conv <addr>             state of that conversation and of everything it started.
einsteinos conv <addr> result      what it returned, once it is done.
judge and plan conversations never start conversations. limits: depth, width and tree size are enforced by the kernel.`

// HandleEinsteinCommand runs one `einsteinos …` command for the conversation
// parentConv and returns the text the child will read as the command's output.
func HandleEinsteinCommand(parentConv, cmd string) string {
	args, body := splitCommand(cmd)
	if len(args) < 2 || args[0] != "einsteinos" {
		return einsteinHelp
	}
	switch args[1] {
	case "help":
		return einsteinHelp

	case "spawn":
		if len(args) < 3 {
			return errOut("usage: einsteinos spawn <work|judge|plan> <<'EOF' … EOF")
		}
		kind := args[2]
		var scope string
		for i := 3; i+1 < len(args); i++ {
			if args[i] == "--scope" {
				scope = args[i+1]
			}
		}
		if strings.TrimSpace(body) == "" {
			return errOut("no prompt: give it as a heredoc after the command")
		}
		prompt := body
		switch kind {
		case "judge":
			var items []JudgeItem
			var ctx []string
			for _, l := range strings.Split(body, "\n") {
				if strings.HasPrefix(strings.TrimSpace(l), "- ") {
					q := strings.TrimSpace(strings.TrimSpace(l)[2:])
					items = append(items, JudgeItem{ID: strconv.Itoa(len(items) + 1), Question: q})
				} else {
					ctx = append(ctx, l)
				}
			}
			if len(items) == 0 {
				return errOut(`a judge needs questions: one per line starting with "- "`)
			}
			prompt = BuildJudgePrompt(strings.Join(ctx, "\n"), items)
		case "work", "plan":
			prompt = spawnPreamble(parentConv, kind, scope) + body
		}
		res, err := SpawnConv(parentConv, kind, prompt)
		if err != nil {
			log.Printf("spawn: %s refused for %s: %v", kind, shortID(parentConv), err)
			return errOut(err.Error())
		}
		return jsonOut(res)

	case "conv":
		if len(args) < 3 {
			return errOut("usage: einsteinos conv <addr> [result]")
		}
		addr := resolveAddr(args[2])
		if len(args) >= 4 && args[3] == "result" {
			text, ok := GetConvResult(addr)
			if !ok {
				return jsonOut(map[string]string{"addr": addr, "status": ConvManifest(addr).Status, "result": ""})
			}
			if len(text) > 8000 {
				text = text[:8000] + "\n…(truncated)"
			}
			return jsonOut(map[string]string{"addr": addr, "status": ConvManifest(addr).Status, "result": text})
		}
		return jsonOut(ConvManifest(addr))
	}
	return einsteinHelp
}

// resolveAddr lets a child pass the 8-character prefix it sees in logs.
func resolveAddr(a string) string {
	if len(a) >= 32 {
		return a
	}
	convRegistryMu.Lock()
	defer convRegistryMu.Unlock()
	var match string
	for _, r := range convRegistry {
		if strings.HasPrefix(r.ConvID, a) {
			if match != "" && match != r.ConvID {
				return a // ambiguous: leave it, the lookup will say unknown
			}
			match = r.ConvID
		}
	}
	if match != "" {
		return match
	}
	return a
}

// spawnPreamble is what a started work/plan conversation is told about itself.
func spawnPreamble(parent, kind, scope string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("[YOU WERE STARTED BY CONVERSATION %s — kind=%s]\n", shortID(parent), kind))
	if scope != "" {
		b.WriteString("[YOUR SCOPE] " + scope + "\ntouch only files that belong to this scope: other conversations are editing the same tree.\n")
	}
	b.WriteString("do NOT git push: the kernel verifies and pushes. write your files, then finish with a short summary of what you did.\n")
	if kind == "plan" {
		b.WriteString("you decide how to split, you write no code and you start no conversations.\n")
	}
	b.WriteString(DoneInstruction())
	b.WriteString("\n")
	return b.String()
}
