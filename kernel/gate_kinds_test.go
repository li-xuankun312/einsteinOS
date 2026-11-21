package kernel

import (
	"strings"
	"testing"
)

// 11/20 the integration unit's verifications were logged gate="kernel_unported" (00:59),
// gate="port_ref_file_unknown" (01:08) and gate="port_extra" (01:20): the line names the first
// gate only, while the text sent to the conversation holds every one that failed, so whether
// the three were one failure at a time or three at once could not be told from the log.
func TestTheVerificationLogNamesEveryGateThatFailed(t *testing.T) {
	in := GateInput{UnitID: "uintegration01529aaa82b9",
		PortRefFiles: []string{"src/kernels/bf16_gemv_parity.cpp\tbf16_gemv\tsrc/nowhere.cpp"},
		PortExtra:    []string{"src/kernels/cpu/expert_parity.cpp\texpert_parity\tsrc/kernels/expert.cpp"}}
	kinds, texts := unitGatesAll(in)
	if len(kinds) != 2 || kinds[0] != "port_ref_file_unknown" || kinds[1] != "port_extra" || len(texts) != 2 {
		t.Fatalf("kinds %v", kinds)
	}
	res := VerifyResult{Gates: kinds}
	res.Gate, res.GateText = kinds[0], strings.Join(texts, "\n")
	got := gateLogText(res)
	if !strings.Contains(got, `gate="port_ref_file_unknown"`) || !strings.Contains(got, "port_extra") {
		t.Fatalf("log %s", got)
	}
	if one := gateLogText(VerifyResult{Gate: "port_extra", Gates: []string{"port_extra"}}); one != `gate="port_extra"` {
		t.Fatalf("one gate: %s", one)
	}
	if none := gateLogText(VerifyResult{}); none != `gate=""` {
		t.Fatalf("no gate: %s", none)
	}
}
