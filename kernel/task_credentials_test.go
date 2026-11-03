package kernel

import (
	"strings"
	"testing"
)

// The 2026/11/02 task, as typed (one line; the token is a well-formed stand-in).
const task1102 = "项目在 zeng-bin3123/baloon，分支是 main，你看这个仓库可以得知，注意看这个项目：github.com/zeng-bin3123/baloon     " +
	"我们要开发的是：ilu级别的底层代码，所以直接参考github.com/Niko1221/Strata  Niko1221/Strata  。 2、开发一个全新版本的肯定需要产品经理 " +
	"api限流就用这个: 20210820209@stu.usc.edu.cn zeng-bin3123  ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789   " +
	"把编译环境（clang）、硬件那些硬信息和shell跑的命令md先从zeng-bin3123/baloon main 拿取"

func TestTheTaskSurvivesWithoutItsCredentials(t *testing.T) {
	got := strip_credentials(task1102)
	for _, keep := range []string{"zeng-bin3123/baloon", "ilu级别的底层代码", "参考github.com/Niko1221/Strata", "编译环境（clang）", "api限流就用这个"} {
		if !strings.Contains(got, keep) {
			t.Errorf("lost %q:\n%s", keep, got)
		}
	}
	for _, gone := range []string{"ghp_ABCDEF", "20210820209", "stu.usc.edu"} {
		if strings.Contains(got, gone) {
			t.Errorf("kept %q:\n%s", gone, got)
		}
	}
	if !strings.Contains(got, "<email>") {
		t.Errorf("email not marked:\n%s", got)
	}
	// key: value credentials keep the key, lose the value; other lines are untouched
	multi := "目标: Iluvatar BI-V100 上跑起来\npassword: hunter2, token=abc123\ncookie：xyz;other line"
	got = strip_credentials(multi)
	if !strings.Contains(got, "目标: Iluvatar BI-V100 上跑起来") || strings.Contains(got, "hunter2") || strings.Contains(got, "abc123") || strings.Contains(got, "xyz") {
		t.Fatalf("%q", got)
	}
	if !strings.Contains(got, "password: <redacted>") || !strings.Contains(got, "token=<redacted>") {
		t.Fatalf("%q", got)
	}
	// a word that only contains "token" is not a credential
	if s := "split the tokenizer into units"; strip_credentials(s) != s {
		t.Fatalf("%q", strip_credentials(s))
	}
}
