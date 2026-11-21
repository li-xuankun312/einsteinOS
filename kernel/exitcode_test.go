package kernel

import (
	"testing"
	"time"
)

func TestForkExecReportsRealExitCode(t *testing.T) {
	bootKernel()
	for _, want := range []int{0, 1, 2, 42, 127} {
		pid, err := ForkExec("echo out; exit "+itoa(want), "")
		if err != nil {
			t.Fatal(err)
		}
		cpid, code, err := WaitTimeout(pid, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if code != want {
			t.Fatalf("exit %d reported as %d", want, code)
		}
		if got := GetResult(cpid); got != "out\n" {
			t.Fatalf("stdout %q", got)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
