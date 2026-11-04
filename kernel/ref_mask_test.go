package kernel

import (
	"strings"
	"testing"
)

// the dev conversation never sees the reference's name — in the task, the port checklist or
// the integration list — and paths and executable names stay valid paths and names
func TestTheReferenceNameIsMaskedEverywhereAConversationLooks(t *testing.T) {
	task := newRefMask(refMaskText, "Niko1221/Strata").Replace("直接参考github.com/Niko1221/Strata  Niko1221/Strata 。收集github Strata 仓库")
	if strings.Contains(task, "Strata") || strings.Contains(task, "Niko1221") || !strings.Contains(task, "(reference)") || !strings.Contains(task, "(ref)") {
		t.Fatalf("%s", task)
	}
	req := UnitRequirements{
		Sources: []string{"include/strata/kernels/ngram.hpp"},
		Symbols: []RefSymbol{{File: "include/strata/kernels/ngram.hpp", Kind: "type", Name: "strata::PleTable"}},
		Tests:   []RefTest{{Name: "cuda_device_selftest", Command: "strata-device --selftest"}},
		Repo:    "Niko1221/Strata",
	}
	m := maskRequirements(req, newRefMask(refMaskIdent, "Niko1221/Strata"))
	if m.Symbols[0].File != "include/ref/kernels/ngram.hpp" || m.Symbols[0].Name != "ref::PleTable" || m.Tests[0].Command != "ref-device --selftest" {
		t.Fatalf("%+v", m)
	}
	if req.Sources[0] != "include/strata/kernels/ngram.hpp" || m.Sources[0] != req.Sources[0] {
		t.Fatal("the kernel keeps measuring with the real paths")
	}
	all := m.PortChecklist("(reference)", "01529aaa82b9", "u1") + m.IntegrationBlock("(reference)", "01529aaa82b9", "u1") + integrationScript(m.Repo, "01529aaa82b9", m.Tests)
	if strings.Contains(strings.ToLower(all), "strata") || strings.Contains(all, "Niko1221") {
		t.Fatalf("leak:\n%s", all)
	}
}
