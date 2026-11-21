package claudeweb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/v2/kernel"
)

// 11/17 13:57:17 dev_fix_1 (8941cbd1), on the mirror (no .git): a Python script appended a port
// line, called git add and stopped at "fatal: not a git repository"; the sandbox's run went on.
// The script's own git add and git commit succeed here and change nothing, so the script runs
// to its end on both machines.
func TestAScriptsGitAddAndCommitDoNotStopItOnTheMirror(t *testing.T) {
	kernelInitOnce.Do(kernel.Init)
	mirror := t.TempDir() // the branch's tree, without .git
	tsv := filepath.Join(mirror, "u7991572426.tsv")
	os.WriteFile(tsv, []byte("src/prefill/kernels.cu\tN\tsrc/prefill/kernels.cu\tN\n"), 0o644)
	script := "cd " + mirror + "\n\npython3 << 'PYEOF'\nimport subprocess\n" +
		"open('u7991572426.tsv','a').write('src/prefill/kernels.cu\\twarp_sum\\tsrc/prefill/kernels.cu\\twarp_sum\\n')\n" +
		"subprocess.run(['git', 'add', 'u7991572426.tsv'], check=True)\n" +
		"subprocess.run(['git', '-C', '" + mirror + "', 'commit', '-m', 'port warp_sum'], check=True)\n" +
		"lines = open('u7991572426.tsv').read().splitlines()\n" +
		"open('u7991572426.tsv','w').write('\\n'.join(sorted(set(lines))) + '\\n')\n" +
		"print('done', len(lines))\nPYEOF"
	s := &ShadowExecutor{WorkDir: t.TempDir(), Enabled: true}
	out := s.Execute("bash_tool", jsonCommand(script))
	if !strings.Contains(out, "exit=0") || !strings.Contains(out, "done 2") {
		t.Fatalf("the script did not run to its end here: %q", out)
	}
	b, _ := os.ReadFile(tsv)
	if got := strings.Count(string(b), "\n"); got != 2 || !strings.Contains(string(b), "warp_sum") {
		t.Fatalf("the mirror's port map: %q", b)
	}
	// any other git command is the real git's: on the mirror it still says what it says
	out = s.Execute("bash_tool", jsonCommand("cd "+mirror+" && python3 -c \"import subprocess,sys; sys.exit(subprocess.run(['git','status']).returncode)\""))
	if strings.Contains(out, "exit=0") {
		t.Fatalf("git status on a tree without .git succeeded: %q", out)
	}
}
