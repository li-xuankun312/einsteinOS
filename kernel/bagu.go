package kernel

import (
	"fmt"
	"strings"
)

// baguFmt is the one shell every prompt the kernel writes is put in: the user's own words,
// the same every time, as in each message of the 11/05–11/11 conversation that built this
// kernel. Only what is inserted differs, and the inserted content is what makes each prompt
// a different piece of work (an eight-legged essay: the form fixed, the content anything):
//
//	%[1]s where to look            (11/05: "最近baloon的提交")
//	%[2]s what has happened there  (11/05: "有跑过unit 38的集成")
//	%[3]s the content itself       (11/05: the review it pasted)
//	%[4]s how many commits         (11/05: "几十")
//	%[5]s where they go            (11/05: "einsteinOS")
//	%[6]s what answers each sentence, %[7]s what it is answered by
//	                               (11/05: "代码", "commit"; 11/15 the user's own form:
//	                               "这些问题的每一句话都要有[].format(arg)对应的.format(arg)解决")
//	%[8]s what each sentence must have (11/05: "功能代码"; %[7]s again for its "一个commit"
//	                               and "开…个commit")
const baguFmt = "这些问题的每一句话都要有%[6]s对应的%[7]s解决，\n" +
	"既然这样，就没必要扣剩下的小警告。现在主要是：注意，你可以看看%[1]s，%[2]s，但是，内容如下评价：%[3]s" +
	"每一句话，都要有对应的%[8]s，建议一句话一个%[7]s, 这次估计要开%[4]s个%[7]s推到%[5]s, " +
	"prompt工程是不被允许的，实在需要的话最多做.format(arg) 这种插入式\n" +
	"。工具调用次数满了就跟我说，我会帮你重置，一直满就一直重置"

// BaguWork: what each sentence of the content is answered with — %[6]s and %[7]s of the
// shell's first sentence, %[8]s and %[7]s of its third.
type BaguWork struct{ Of, By, Need string }

var (
	// WorkCode: a conversation whose work is code, judged by the commits the kernel verifies
	// (the user's words of 11/05)
	WorkCode = BaguWork{"代码", "commit", "功能代码"}
	// WorkProof: a conversation whose work is theory, judged by the proofs Axle checks. 11/14
	// and 11/15 every theory and proof-repair prompt began by asking for code and commits.
	WorkProof = BaguWork{"定理", "证明", "定理"}
)

// WorkFor: the work of a conversation's role — proofs for a theory conversation or a proof
// repair, code for the others.
func WorkFor(role string) BaguWork {
	for _, p := range []string{"theorize_", "proof_fix_"} {
		if strings.HasPrefix(role, p) {
			return WorkProof
		}
	}
	return WorkCode
}

// Bagu puts content into the shell for code; nothing is written around it.
func Bagu(where, what, content, commits, to string) string {
	return BaguFor(WorkCode, where, what, content, commits, to)
}

// BaguFor puts content into the shell for the given work.
func BaguFor(work BaguWork, where, what, content, commits, to string) string {
	return fmt.Sprintf(baguFmt, where, what, content, commits, to, work.Of, work.By, work.Need)
}

// baguContentAt is where the shell's content begins: what is inserted after it is content.
const baguContentAt = "，但是，内容如下评价："

// BaguInsert puts more content into a prompt: into the shell's content, first, when the prompt
// is a shell, so the shell stays the whole prompt; in front of it otherwise (a person's own
// message is not a shell).
func BaguInsert(prompt, more string) string {
	if i := strings.Index(prompt, baguContentAt); i >= 0 {
		j := i + len(baguContentAt)
		return prompt[:j] + more + prompt[j:]
	}
	return more + prompt
}

// Plain is how a conversation that does not write code is given its content: what happened
// and the content, no shell. The shell's first sentence asks for code: 11/12 05:53 a review
// given it answered "未收到评价正文，本轮无代码修改、无 commit" (taken as its FAIL), and the
// planner the kernel then started to split the unit wrote code for 82 tool calls.
func Plain(where, what, content string) string {
	return fmt.Sprintf(plainFmt, where, what, content)
}

// %[1]s where, %[2]s what happened, %[3]s the content
const plainFmt = "[%[1]s] %[2]s\n\n%[3]s"

// RoleAnswersInText: the conversations whose whole answer is a few lines of text about
// what their prompt already holds; a tool call from one of them is not the answer. 11/12
// the dev-repository question (unnamed then) ran the task with the token in it and pushed
// nine commits to the dev repository's main.
func RoleAnswersInText(role string) bool {
	return role == "detect_dev"
}

// RoleWritesCode: the conversations the shell is for — the ones whose work is code (a dev
// unit, its fix, the legacy dev step, a spawned work conversation) or theory (a theory
// conversation, a proof repair). The others give a judgement or a list.
func RoleWritesCode(role string) bool {
	for _, p := range []string{"dev_unit_", "dev_fix", "dev_legacy", "theorize_", "proof_fix_", "spawn_work"} {
		if strings.HasPrefix(role, p) {
			return true
		}
	}
	return false
}
