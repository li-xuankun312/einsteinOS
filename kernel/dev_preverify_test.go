package kernel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 11/05 02:47 a fix ended and then failed the kernel's port gate; at 02:55 one ended on an
// exit code a pipe had taken from tail. The kernel's verdict is given at the done line.
func TestTheDoneLineIsCheckedByTheKernelsOwnVerification(t *testing.T) {
	if ran, _, _ := PreVerify("dev_unit_x"); ran {
		t.Fatal("nothing registered: nothing runs")
	}
	fail := UnitVerifyOutcome{Verified: true, Result: VerifyResult{Compile: "pass", Smoke: "pass", Gate: "port_incomplete",
		GateText: "port_incomplete: 2 of the 86 definitions have no counterpart:\n  src/x.cu k1 …"}}
	RegisterPreVerify("dev_unit_x", func() (bool, string) { return false, preVerifyReport(fail) })
	ran, ok, rep := PreVerify("dev_unit_x")
	if !ran || ok || !strings.Contains(rep, "gate=port_incomplete") || !strings.Contains(rep, "2 of the 86") || !strings.Contains(rep, preVerifyTailFmt) || !strings.HasPrefix(rep, "这些问题的每一句话") {
		t.Fatalf("%v %v %s", ran, ok, rep)
	}
	UnregisterPreVerify("dev_unit_x")
	if ran, _, _ := PreVerify("dev_unit_x"); ran {
		t.Fatal("unregistered")
	}

	m := t.TempDir()
	os.WriteFile(filepath.Join(m, "a.cu"), []byte("x"), 0o644)
	d1 := deltaDigest(m, []string{"a.cu"}, nil)
	if d1 != deltaDigest(m, []string{"a.cu"}, nil) {
		t.Fatal("stable")
	}
	os.WriteFile(filepath.Join(m, "a.cu"), []byte("y"), 0o644)
	if d1 == deltaDigest(m, []string{"a.cu"}, nil) {
		t.Fatal("a changed file changes the digest: the verification is not reused")
	}
}

// the check is registered for as long as the conversation runs, not until the fork returns
func TestThePreVerifyCheckOutlivesTheForkCall(t *testing.T) {
	src, err := os.ReadFile("dev_run.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	fork := strings.Index(s, "child, ferr = ForkWithTermination(prompt")
	if fork < 0 {
		t.Fatal("no fork")
	}
	after := s[fork:]
	wait, unreg := strings.Index(after, "\t\tWait(child)\n"), strings.Index(after, "\t\tUnregisterPreVerify(role)\n\t\tif m := getMeta(child)")
	if wait < 0 || unreg < wait {
		t.Fatal("UnregisterPreVerify must come after Wait(child) (and after waitAbandoned)")
	}
}

// 11/25 04:34:25 the log of a failed compile gave the error lines twice: once as the
// verification put them first in build.sh's output, once again over compile_error and that output
func TestTheLogOfAFailedCompileGivesItsErrorLinesOnce(t *testing.T) {
	raw := "a\nx.cu:(.text+0x5): undefined reference to `f()'\nclang-16: error: linker command failed\nb\n"
	put := fmt.Sprintf(compileErrorsFmt, 2, compileErrorLines(raw, compileErrorLinesMax)) + raw
	if got := failedCompileLogText(put); strings.Count(got, compileErrorsHead) != 1 || strings.Count(got, "undefined reference to `f()'") != 2 {
		t.Fatalf("%q", got) // once in the error lines, once in the output's end
	}
	if got := failedCompileLogText(raw); !strings.HasPrefix(got, "x.cu:(.text+0x5)") {
		t.Fatalf("errors not first: %q", got)
	}
}

// 11/25 04:34:25 the same link failure was in build.sh's output twice: it is given once, with
// how many times it was there
func TestAnErrorTheOutputHoldsAgainIsGivenOnce(t *testing.T) {
	one := "x.cu:(.text+0x5): undefined reference to `f()'\nclang-16: error: linker command failed\n\n"
	got := compileErrorLines("a\n"+one+"b\n"+one+"y.cu:3:1: error: z\nq\nr\n", compileErrorLinesMax)
	if strings.Count(got, "undefined reference") != 1 || !strings.Contains(got, fmt.Sprintf(compileErrorRepeatFmt, 2)) || !strings.Contains(got, "y.cu:3:1: error: z\nq\nr\n") {
		t.Fatalf("%q", got)
	}
	if strings.Contains(got, "y.cu:3:1: error: z\nq\nr\n(the") {
		t.Fatalf("a window there once said twice: %q", got)
	}
}

// 11/25 08:08:14 the error lines held "linker command failed" alone: the linker's multiple
// definitions are error lines too
func TestALinkersMultipleDefinitionIsAnErrorLine(t *testing.T) {
	out := "[cuda-pic] src/baloon_e2e_shim.cu\n" +
		"/usr/bin/ld: x/pic_e2e_moe.o: in function `ix_se::__device_stub__swiglu_kernel(float const*, float const*, float*, int)':\n" +
		"moe_parity.cu:(.text+0x0): multiple definition of `ix_se::__device_stub__swiglu_kernel(float const*, float const*, float*, int)'; x/pic_e2e_se.o:test_shared_expert.cu:(.text+0x0): first defined here\n" +
		"clang-16: error: linker command failed with exit code 1\n"
	got := compileErrorLines(out, compileErrorLinesMax)
	if !strings.HasPrefix(got, "moe_parity.cu:(.text+0x0): multiple definition of") {
		t.Fatalf("%q", got)
	}
}
