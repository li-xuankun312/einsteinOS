package kernel

import "testing"

// The shell's %[4]s is the estimate of the work a conversation was started with; it is read
// back from that first prompt so the messages sent into the conversation keep it.
func TestTheShellsEstimateIsReadBackFromThePromptItWasWrittenIn(t *testing.T) {
	// dev_unit_8 (u77baf5bde3) was started with its 43 definitions to port
	dev := Bagu("分支 einsteinOS/dev-c880662efd 上最近的提交", "有 7 个 unit 已经通过", "[UNIT] …", "43", "zeng-bin3123/baloon")
	if c, ok := BaguCommitsOf(dev); !ok || c != "43" {
		t.Fatalf("%q %v", c, ok)
	}
	proof := BaguFor(WorkProof, "where", "what", "content", "7", "einsteinOS/theory")
	if c, ok := BaguCommitsOf(proof); !ok || c != "7" {
		t.Fatalf("theory: %q %v", c, ok)
	}
	// two shells in one message (a writeback with a verification after it): the first
	two := dev + Bagu("w", "x", "y", "1", "z")
	if c, _ := BaguCommitsOf(two); c != "43" {
		t.Fatalf("first shell: %q", c)
	}
	if _, ok := BaguCommitsOf("项目在 zeng-bin3123/baloon，分支是 main"); ok {
		t.Fatal("a person's own message has no estimate")
	}
}

// 11/17 08aaed5a L1112: the verification report sent into dev_fix_2 said "这次估计要开1个commit"
// right after a writeback; every shell sent into a conversation gives the estimate it was
// started with.
func TestAVerificationReportKeepsTheConversationsEstimate(t *testing.T) {
	report := Bagu(preVerifyWhere, "compile=pass smoke=pass gate=lean_unproved", "[KERNEL VERIFICATION] …", "1", "zeng-bin3123/baloon")
	got := BaguKeepCommits(report, "43")
	if c, _ := BaguCommitsOf(got); c != "43" {
		t.Fatalf("%q", got)
	}
	both := BaguKeepCommits(Bagu("a", "b", "c", "2", "这台机器")+report, "43")
	if n := len(baguCommitsRe.FindAllStringSubmatch(both, -1)); n != 2 {
		t.Fatalf("%d shells", n)
	}
	for _, m := range baguCommitsRe.FindAllStringSubmatch(both, -1) {
		if m[1] != "43" {
			t.Fatalf("a shell kept %q", m[1])
		}
	}
	if BaguKeepCommits("no shell here", "43") != "no shell here" {
		t.Fatal("text without a shell changed")
	}
}
