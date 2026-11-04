package kernel

import (
	"fmt"
	"strings"
	"testing"
)

func TestTheReviewerIsGivenThePortMapNotTheReference(t *testing.T) {
	req := UnitRequirements{Sources: []string{"src/spec/controller.cpp"}, Symbols: []RefSymbol{{File: "src/spec/controller.cpp", Name: "Controller::choose"}}}
	st := reviewStatus(map[string]string{"compile": "pass"}, "u0e60a9f272", req)
	if st["port_map"] != ".einsteinOS/port/u0e60a9f272.tsv" || st["compile"] != "pass" {
		t.Fatalf("%v", st)
	}
	for k, v := range st {
		if strings.Contains(k, "reference") || strings.Contains(v, "Strata") {
			t.Fatalf("the reviewer does not see the reference: %s=%s", k, v)
		}
	}
	if step := fmt.Sprintf(reviewPortStepFmt, st["port_map"]); !strings.Contains(step, ".einsteinOS/port/u0e60a9f272.tsv") {
		t.Fatal(step)
	}
	if got := reviewStatus(map[string]string{}, "u", UnitRequirements{}); got["port_map"] != "" {
		t.Fatal("nothing measured: nothing added")
	}
}
