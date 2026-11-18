package claudeweb

import (
	"os"
	"strings"
	"testing"

	"google.golang.org/adk/v2/kernel"
)

// 11/18 dev_fix_1 of uebd9b81357 (54005caf) wrote "The reference files must be from the REF repo
// (<owner>/<name>)" and searched this machine for the reference's sources. The kernel's clone
// of the reference is not read here by a conversation whose prompt is masked; a theory
// conversation, which works from the reference, still reads it.
func TestAConversationNotToldTheReferenceDoesNotReadItsCloneHere(t *testing.T) {
	kernelInitOnce.Do(kernel.Init)
	if _, err := kernel.PRFork("o/dev", "b", "t", []kernel.RefRepo{{Owner: "o1", Name: "Testref"}}); err != nil {
		t.Fatal(err)
	}
	clone := "/tmp/ref_o1_Testref.git"
	os.MkdirAll(clone, 0o755)
	os.WriteFile(clone+"/HEAD", []byte("ref: refs/heads/main\n"), 0o644)
	defer os.RemoveAll(clone)

	m := &Model{Shadow: &ShadowExecutor{WorkDir: t.TempDir(), Enabled: true}}
	convRoles.Store("c-dev", "dev_fix_1")
	convRoles.Store("c-theory", "theorize_3")
	defer convRoles.Delete("c-dev")
	defer convRoles.Delete("c-theory")

	if out := m.shadowExecute("c-dev", "bash_tool", jsonCommand("cat "+clone+"/HEAD")); out != "" {
		t.Fatalf("a dev conversation read the reference's clone here: %q", out)
	}
	if out := m.shadowExecute("c-theory", "bash_tool", jsonCommand("cat "+clone+"/HEAD")); !strings.Contains(out, "refs/heads/main") {
		t.Fatalf("a theory conversation could not read it: %q", out)
	}
}
