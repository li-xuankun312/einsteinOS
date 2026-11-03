package kernel

import (
	"strings"
	"testing"
)

// 11/03: every conversation appeared twice ("child" with no repo, then its role), and
// reviews, the planner and the probe stayed "running" after they had finished.
func TestOneRowPerConversationAndDoneWhenItEnds(t *testing.T) {
	conv := "aa11bb22-0000-4000-8000-registryrows"
	RegisterConv(5, conv, "", "child", 0)                        // at fork, role unknown
	RegisterConv(5, conv, "Niko1221/Strata", "review_unit_3", 3) // the caller knows it
	RegisterConv(0, conv, "", "child", 0)                        // a late generic registration changes nothing

	convRegistryMu.Lock()
	var rows []ConvRecord
	for _, r := range convRegistry {
		if r.ConvID == conv {
			rows = append(rows, r)
		}
	}
	convRegistryMu.Unlock()
	if len(rows) != 1 {
		t.Fatalf("%d rows for one conversation", len(rows))
	}
	if r := rows[0]; r.Repo != "Niko1221/Strata" || r.Role != "review_unit_3" || r.Phase != 3 || r.ParentPid != 5 || r.Status != "running" {
		t.Fatalf("%+v", r)
	}
	ConvDone(conv, "done")
	if !strings.Contains(ConvRegistryStatus(), "aa11bb22 Niko1221/Strata      review_unit_3        3   done") {
		t.Fatalf("%s", ConvRegistryStatus())
	}
}
