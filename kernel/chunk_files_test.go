package kernel

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const od = TheoryOutputsDir

func jsonBlock(m map[string]any) json.RawMessage {
	b, _ := json.Marshal(m)
	return b
}

// a conversation export like the ones the server gives: the child wrote some modules
// with create_file, some with a bash heredoc after a cd, some with a python script,
// and finished with `ls`.
func fakeConvJSON() []byte {
	blocks := []json.RawMessage{
		// read examples from a cloned repo: outside the outputs dir, must never be a candidate
		jsonBlock(map[string]any{"type": "tool_use", "name": "bash_tool", "input": map[string]any{
			"command": "cd /tmp/openai_math/lean/OAI; head -50 Algorithms/Sorting.lean"}}),
		// create_file: the path counts, the BODY (which mentions Phantom.lean) does not
		jsonBlock(map[string]any{"type": "tool_use", "name": "create_file", "input": map[string]any{
			"path": od + "/ViaCreateFile.lean", "file_text": "-- see Phantom.lean and " + od + "/AlsoPhantom.lean\ntheorem t : True := trivial"}}),
		// heredoc after cd: relative name resolves against the cd
		jsonBlock(map[string]any{"type": "tool_use", "name": "bash_tool", "input": map[string]any{
			"command": "mkdir -p " + od + " && cd " + od + "\ncat > ViaHeredoc.lean <<'EOF'\n/-! # MODULE: heredoc -/\nEOF\necho ok"}}),
		// relative name WITHOUT a cd: the default directory is the outputs dir
		jsonBlock(map[string]any{"type": "tool_use", "name": "bash_tool", "input": map[string]any{
			"command": "python3 - <<'EOF'\nopen('ViaPython.lean','w').write('x')\nEOF"}}),
		// a cd elsewhere: a relative name there is NOT in the outputs dir
		jsonBlock(map[string]any{"type": "tool_use", "name": "bash_tool", "input": map[string]any{
			"command": "cd /tmp/devrepo/.einsteinOS/chunks/theorize_chunk_1 && head -70 ElsewhereOnly.lean"}}),
		// a cd to a shell variable: later relative names are unresolvable, skipped
		jsonBlock(map[string]any{"type": "tool_use", "name": "bash_tool", "input": map[string]any{
			"command": "cd $WORK && cat Unresolvable.lean"}}),
		// a glob is not a name
		jsonBlock(map[string]any{"type": "tool_use", "name": "bash_tool", "input": map[string]any{
			"command": "for f in " + od + "/*.lean; do echo $f; done"}}),
		// a name a script computed in a loop only appears in the manifest the prompt asks for
		jsonBlock(map[string]any{"type": "tool_use", "name": "bash_tool", "input": map[string]any{"command": "ls " + od}}),
		jsonBlock(map[string]any{"type": "tool_result", "content": []map[string]any{{"type": "text",
			"text": "ViaCreateFile.lean\nViaHeredoc.lean\nViaPython.lean\nComputedInLoop.lean\nNeverWritten.lean\n"}}}),
	}
	conv := map[string]any{"chat_messages": []map[string]any{{"content": blocks}}}
	b, _ := json.Marshal(conv)
	return b
}

func TestDiscoverOutputPathsFindsEveryWayOfWritingAFile(t *testing.T) {
	got := discoverOutputPaths(fakeConvJSON(), od)
	want := map[string]bool{
		od + "/ViaCreateFile.lean":  true,
		od + "/ViaHeredoc.lean":     true,
		od + "/ViaPython.lean":      true,
		od + "/ComputedInLoop.lean": true,
		od + "/NeverWritten.lean":   true, // a candidate; the sandbox will say it does not exist
	}
	gotSet := map[string]bool{}
	for _, p := range got {
		gotSet[p] = true
		if !want[p] {
			t.Errorf("unexpected candidate %s", p)
		}
	}
	for p := range want {
		if !gotSet[p] {
			t.Errorf("missing candidate %s (got %v)", p, got)
		}
	}
	// order of first appearance, no duplicates
	if len(got) != len(gotSet) {
		t.Errorf("duplicates in %v", got)
	}
	if got[0] != od+"/ViaCreateFile.lean" {
		t.Errorf("first candidate should be the first one mentioned inside the outputs dir: %v", got)
	}
}

func TestCandidatesInTextCdHandling(t *testing.T) {
	cases := []struct{ text, want string }{
		{"cd " + od + " && cat A.lean", od + "/A.lean"},
		{"cd sub && cat A.lean", od + "/sub/A.lean"},
		{"cd /tmp/x && cat A.lean", ""},
		{"cd /tmp/x; cd " + od + "; cat A.lean", od + "/A.lean"},
		{"cat " + od + "/B.lean", od + "/B.lean"},
		{"cat ../outside/C.lean", ""},
		{"cd \"" + od + "\" && ls D.lean", od + "/D.lean"},
		{"echo ~/E.lean", ""},
		{"cat .hidden.lean", ""},
	}
	for _, c := range cases {
		got := candidatesInText(c.text, od, false)
		g := strings.Join(got, ",")
		if g != c.want {
			t.Errorf("%q → %q want %q", c.text, g, c.want)
		}
	}
}

func stubDownloads(t *testing.T, existing map[string]string) *int {
	calls := 0
	old := downloadFileFn
	SetDownloadFileFn(func(convID, p string) ([]byte, error) {
		calls++
		if c, ok := existing[p]; ok {
			return []byte(c), nil
		}
		return nil, fmt.Errorf("404")
	})
	t.Cleanup(func() { downloadFileFn = old })
	return &calls
}

func TestDownloadChunkFilesKeepsOnlyWhatTheSandboxHas(t *testing.T) {
	stubDownloads(t, map[string]string{
		od + "/ViaCreateFile.lean":  "theorem a : True := trivial -- patched later by a script",
		od + "/ViaHeredoc.lean":     "/-! # MODULE: heredoc -/",
		od + "/ViaPython.lean":      "theorem py : True := trivial\n-- provertools_session=abcdefabcdefabcdefabcdefabcdef",
		od + "/ComputedInLoop.lean": "theorem loop : True := trivial",
		od + "/Empty.lean":          "",
	})
	files := downloadChunkFiles("c0nv", fakeConvJSON(), od)
	if len(files) != 4 {
		t.Fatalf("want the 4 files that exist, got %d: %+v", len(files), files)
	}
	for _, f := range files {
		if f.Name == "NeverWritten.lean" {
			t.Fatal("a candidate the sandbox does not have must be dropped")
		}
	}
	// the final state, not what create_file first wrote, and secrets redacted
	var py chunkFile
	for _, f := range files {
		if f.Name == "ViaPython.lean" {
			py = f
		}
	}
	if strings.Contains(string(py.Content), "abcdefabcdefabcdefabcdefabcdef") {
		t.Fatal("secret reached a file that lands in a git-tracked tree")
	}
}

func TestRenderChunkFilesMatchesWhatDevExtractionReads(t *testing.T) {
	out := renderChunkFiles([]chunkFile{
		{RemotePath: od + "/Alpha.lean", Name: "Alpha.lean", Content: []byte("theorem a : True := trivial")},
		{RemotePath: od + "/Beta.lean", Name: "Beta.lean", Content: []byte("theorem b : True := trivial")},
	})
	secs := dev_extract_file_sections(out)
	if len(secs) != 2 {
		t.Fatalf("dev_extract_file_sections must see 2 sections, got %d from:\n%s", len(secs), out)
	}
	mods := devModulesFromSections(secs)
	if mods[0].Name == "" || mods[1].Name == "" || mods[0].Name == mods[1].Name {
		t.Fatalf("modules %+v", mods)
	}
	for _, m := range mods {
		if m.Bytes > 200 {
			t.Fatalf("each module must hold only its own file, not the others: %+v", m)
		}
	}
}

func TestChunkFilesForDownloadsOncePerConversation(t *testing.T) {
	calls := stubDownloads(t, map[string]string{od + "/ViaHeredoc.lean": "x"})
	rawCalls := 0
	old := downloadConvRawFn
	SetDownloadConvRawFn(func(string) ([]byte, error) { rawCalls++; return fakeConvJSON(), nil })
	t.Cleanup(func() { downloadConvRawFn = old })

	a := chunkFilesFor("conv-once")
	first := *calls
	b := chunkFilesFor("conv-once") // ref_store_chunk asks again
	if rawCalls != 1 || *calls != first || len(a) != len(b) {
		t.Fatalf("second ask must hit the memo: raw=%d downloads %d→%d", rawCalls, first, *calls)
	}
	forgetChunkFiles("conv-once")
	chunkFilesFor("conv-once")
	if rawCalls != 2 {
		t.Fatal("after forget the conversation is fetched again")
	}
}

func TestPlaceholdersAndFindOutputAreNotFiles(t *testing.T) {
	// "$f.lean" in a shell loop is a template
	for _, text := range []string{
		"for f in A B; do s=open('$f.lean').read(); done",
		"printf '%s.lean' x",
		"echo {name}.lean",
		"ls *.lean",
	} {
		if got := candidatesInText(text, od, false); len(got) != 0 {
			t.Errorf("%q → %v: a template is not a file", text, got)
		}
	}
	// the output of a find in the examples repo: relative paths with directories
	find := "Algorithms/Sorting/MergeSort.lean\nAlgorithms/Search/Binary.lean\nCombinatorics/Choose.lean\n"
	if got := candidatesInText(find, od, true); len(got) != 0 {
		t.Errorf("find output resolved against the outputs dir: %v", got)
	}
	// but the same text as a COMMAND the child wrote is resolved (cd-aware)
	if got := candidatesInText("cat Algorithms/X.lean", od, false); len(got) != 1 {
		t.Errorf("a command's relative path is still a candidate: %v", got)
	}
	// the manifest the prompt asks for: bare names, and absolute paths, are believed
	ls := "ExpertCacheResidency.lean\nExpertSourceDispatch.lean\n" + od + "/Third.lean\n"
	if got := candidatesInText(ls, od, true); len(got) != 3 {
		t.Errorf("ls output of the outputs dir: %v", got)
	}
}
