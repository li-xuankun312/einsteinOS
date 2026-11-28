package claudeweb

import (
	"os"
	"strings"
	"testing"

	"google.golang.org/adk/v2/kernel"
)

// What this machine prints goes to a conversation whose prompt is masked with the reference
// masked the same way: a listing of /tmp here names the reference's clone.
func TestWhatThisMachinePrintsIsMaskedForAMaskedConversation(t *testing.T) {
	kernelInitOnce.Do(kernel.Init)
	if _, err := kernel.PRFork("o/dev", "b", "t", []kernel.RefRepo{{Owner: "o2", Name: "Maskref"}}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.MkdirAll(dir+"/ref_o2_Maskref.git", 0o755)
	os.WriteFile(dir+"/maskref.hpp", []byte("#define MASKREF_HD 1\n"), 0o644)

	m := &Model{Shadow: &ShadowExecutor{WorkDir: t.TempDir(), Enabled: true}}
	convRoles.Store("c-dev2", "dev_unit_4")
	convRoles.Store("c-theory2", "theorize_1")
	defer convRoles.Delete("c-dev2")
	defer convRoles.Delete("c-theory2")

	cmd := jsonCommand("ls " + dir + " && cat " + dir + "/maskref.hpp")
	out := m.shadowExecute("c-dev2", "bash_tool", cmd)
	if strings.Contains(strings.ToLower(out), "maskref") || strings.Contains(out, "o2") {
		t.Fatalf("the reference's name reached a dev conversation: %q", out)
	}
	if !strings.Contains(out, "exit=0") {
		t.Fatalf("the command's result is gone: %q", out)
	}
	if out := m.shadowExecute("c-theory2", "bash_tool", cmd); !strings.Contains(out, "MASKREF_HD") {
		t.Fatalf("a theory conversation is shown the reference as it is: %q", out)
	}
}
