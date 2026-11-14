package kernel

import (
	"strings"
	"testing"
)

// The shell is the user's text itself: with the user's own insertions of 11/05 it gives back,
// character for character, the message the user wrote.
func TestTheShellIsTheUsersOwnText(t *testing.T) {
	user := "这些问题的每一句话都要有代码对应的commit解决，\n" +
		"既然这样，就没必要扣剩下的小警告。现在主要是：注意，你可以看看最近baloon的提交，有跑过unit 38的集成，但是，内容如下评价：" +
		"每一句话，都要有对应的功能代码，建议一句话一个commit, 这次估计要开几十个commit推到einsteinOS, " +
		"prompt工程是不被允许的，实在需要的话最多做.format(arg) 这种插入式\n" +
		"。工具调用次数满了就跟我说，我会帮你重置，一直满就一直重置"
	if got := Bagu("最近baloon的提交", "有跑过unit 38的集成", "", "几十", "einsteinOS"); got != user {
		t.Fatalf("got\n%s\nwant\n%s", got, user)
	}
}

// More content goes into a shell's content, so the shell is still the whole prompt; a message
// that is not a shell gets it in front.
func TestMoreContentGoesIntoTheShell(t *testing.T) {
	p := Bagu("where", "what", "C", "1", "to")
	got := BaguInsert(p, "M")
	if !strings.HasPrefix(got, "这些问题的每一句话") || !strings.Contains(got, "内容如下评价：MC") || len(got) != len(p)+1 {
		t.Fatalf("%s", got)
	}
	if got := BaguInsert("hello", "M"); got != "Mhello" {
		t.Fatalf("%q", got)
	}
}

// The shell is for the conversations whose work is code or theory; a review, a planner, a judge,
// a search and the hardware probe get plain facts. 11/12 05:53 a review given the shell
// answered it as a developer ("未收到评价正文，本轮无代码修改、无 commit"), and the planner
// started next wrote code for 82 tool calls.
func TestTheShellIsOnlyForTheConversationsThatWriteCode(t *testing.T) {
	shell := "这些问题的每一句话都要有代码对应的commit解决"
	env := newDevEnv(t, theory2("Alpha"))
	env.model.unitWork = func(prompt, unit string, n int) {
		mustWrite(env.mirror, "src/impl.cu", "// implementation\n")
		env.model.writeScripts(unit, 0, 0, "built")
	}
	if _, err := dev_from_theorems(9201, &Step{Owner: "ref", Repo: "proj"}); err != nil {
		t.Fatalf("dev_from_theorems: %v", err)
	}
	units, reviews := 0, 0
	for _, p := range env.model.prompts {
		switch {
		case strings.Contains(p, "[THEORY — unit "):
			units++
			if !strings.HasPrefix(p, shell) {
				t.Fatalf("a dev unit's prompt is the shell:\n%.300s", p)
			}
		case strings.Contains(p, "[VERDICT] the kernel reads the last line"), strings.Contains(p, "[UNITS] the kernel reads lines"):
			reviews++
			if strings.Contains(p, shell) {
				t.Fatalf("a review or planner prompt holds the shell:\n%.300s", p)
			}
		}
	}
	if units == 0 || reviews == 0 {
		t.Fatalf("units=%d reviews/planners=%d", units, reviews)
	}
	for name, p := range map[string]string{
		"judge":      BuildJudgePrompt("", []JudgeItem{{ID: "1", Question: "q"}}),
		"hw_probe":   hwProbePrompt(),
		"search":     refDiscoverPrompt("t", 3, refDiscoverKeepFact),
		"spawn plan": spawnPrompt("p", "plan", "", "body"),
	} {
		if strings.Contains(p, shell) {
			t.Fatalf("%s holds the shell:\n%.300s", name, p)
		}
	}
	for name, p := range map[string]string{
		"spawn work": spawnPrompt("p", "work", "", "body"),
		"theorize":   theorizePrompt("o", "r", "0123456789ab", "w", "t", "src", 1, ""),
	} {
		if !strings.HasPrefix(p, shell) {
			t.Fatalf("%s is the shell:\n%.300s", name, p)
		}
	}
	if !RoleWritesCode("dev_unit_3") || !RoleWritesCode("dev_fix_1") || !RoleWritesCode("theorize_chunk_2") || RoleWritesCode("review_conv") || RoleWritesCode("dev_planner") {
		t.Fatal("RoleWritesCode")
	}
}
