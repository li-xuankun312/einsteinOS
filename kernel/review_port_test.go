package kernel

import (
	"fmt"
	"strings"
	"testing"
)

func TestTheReviewerIsGivenTheReferenceAndThePortMap(t *testing.T) {
	req := UnitRequirements{Sources: []string{"src/spec/controller.cpp"}, Symbols: []RefSymbol{{File: "src/spec/controller.cpp", Name: "Controller::choose"}}}
	st := reviewStatus(map[string]string{"compile": "pass"}, "Niko1221/Strata", "01529aaa82b9abcd", "u0e60a9f272", req)
	if st["port_map"] != ".einsteinOS/port/u0e60a9f272.tsv" || st["reference"] != "Niko1221/Strata@01529aaa82b9" || st["compile"] != "pass" {
		t.Fatalf("%v", st)
	}
	step := fmt.Sprintf(reviewPortStepFmt, st["port_map"], st["reference"], st["reference_sources"])
	if !strings.Contains(step, "src/spec/controller.cpp") || !strings.Contains(step, ".einsteinOS/port/u0e60a9f272.tsv") {
		t.Fatal(step)
	}
	if got := reviewStatus(map[string]string{}, "r", "s", "u", UnitRequirements{}); got["port_map"] != "" {
		t.Fatal("nothing measured: nothing added")
	}
}
