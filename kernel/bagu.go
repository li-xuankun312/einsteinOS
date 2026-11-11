package kernel

import "fmt"

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
const baguFmt = "这些问题的每一句话都要有代码对应的commit解决，\n" +
	"既然这样，就没必要扣剩下的小警告。现在主要是：注意，你可以看看%[1]s，%[2]s，但是，内容如下评价：%[3]s" +
	"每一句话，都要有对应的功能代码，建议一句话一个commit, 这次估计要开%[4]s个commit推到%[5]s, " +
	"prompt工程是不被允许的，实在需要的话最多做.format(arg) 这种插入式\n" +
	"。工具调用次数满了就跟我说，我会帮你重置，一直满就一直重置"

// Bagu puts content into the shell; nothing is written around it.
func Bagu(where, what, content, commits, to string) string {
	return fmt.Sprintf(baguFmt, where, what, content, commits, to)
}
