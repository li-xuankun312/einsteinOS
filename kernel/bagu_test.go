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
