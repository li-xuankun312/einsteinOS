package kernel

// chunk_files.go — which files did a theory conversation leave in its sandbox?
//
// ref_store_chunk used to learn the answer from ONE place: the paths of
// create_file / write_file tool calls. A conversation that wrote its modules with
// `cat > X.lean <<EOF` or a python script left no such call, so the chunk stored
// "0 files downloaded" and the theory for that chunk became the whole conversation
// transcript — Axle output, curl calls, patch scripts and all (Strata runs: chunks
// 1, 2 and 4; and 2026/11/02 chunks 1 and 2). Even for create_file the transcript
// carries the text as first written, before any later patch script fixed a proof.
//
// The sandbox is the truth about what was written, and it has exactly one file
// interface: download-file by path. So the work is finding candidate paths, and
// validating each by asking the sandbox. A candidate that does not exist fails to
// download and is dropped, so a wrong guess costs nothing; the match is on the
// file suffix and the outputs directory, never on what the task was.
//
// Candidates come from everything the conversation shows:
//   - every string in every tool call's input (a bash command, a python script, a
//     create_file path...), relative names resolved against the `cd` in effect;
//   - every tool result (an `ls` of the outputs directory is the manifest the
//     theory prompt asks for, and it catches names a script computed in a loop).

import (
	"encoding/json"
	"log"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// chunkFile is one file downloaded from the sandbox.
type chunkFile struct {
	RemotePath string
	Name       string
	Content    []byte
}

// leanTokenRe: a path-like token ending in .lean. A glob ("*.lean") has no token
// character before the suffix and does not match.
var leanTokenRe = regexp.MustCompile(`[A-Za-z0-9_./~\-]+\.lean\b`)

// cdRe: a cd and its target.
var cdRe = regexp.MustCompile(`(?:^|[;&|\s(])cd\s+("[^"]*"|'[^']*'|[^\s;&|)]+)`)

// collectStrings gathers every string value inside decoded JSON.
func collectStrings(v any, out *[]string) {
	switch x := v.(type) {
	case string:
		*out = append(*out, x)
	case []any:
		for _, e := range x {
			collectStrings(e, out)
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic order
		for _, k := range keys {
			collectStrings(x[k], out)
		}
	}
}

// candidatesInText returns the .lean paths under dir that text mentions, in order.
// cwd starts at dir and follows the cd commands it passes; a cd to an unresolvable
// target (a shell variable) makes later relative names unresolvable, so they are
// skipped rather than guessed.
func candidatesInText(text, dir string) []string {
	type ev struct {
		pos  int
		cd   string
		path string
	}
	var evs []ev
	for _, m := range cdRe.FindAllStringSubmatchIndex(text, -1) {
		evs = append(evs, ev{pos: m[2], cd: strings.Trim(text[m[2]:m[3]], `"'`)})
	}
	for _, m := range leanTokenRe.FindAllStringIndex(text, -1) {
		evs = append(evs, ev{pos: m[0], path: text[m[0]:m[1]]})
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].pos < evs[j].pos })

	cwd := dir
	var out []string
	for _, e := range evs {
		if e.cd != "" {
			switch {
			case strings.ContainsAny(e.cd, "$`"), e.cd == "-":
				cwd = ""
			case strings.HasPrefix(e.cd, "/"):
				cwd = path.Clean(e.cd)
			case strings.HasPrefix(e.cd, "~"):
				cwd = ""
			case cwd != "":
				cwd = path.Join(cwd, e.cd)
			}
			continue
		}
		p := e.path
		if strings.HasPrefix(p, "~") || strings.HasPrefix(path.Base(p), ".") {
			continue
		}
		var full string
		switch {
		case strings.HasPrefix(p, "/"):
			full = path.Clean(p)
		case cwd != "":
			full = path.Join(cwd, p)
		default:
			continue
		}
		if strings.HasPrefix(full, dir+"/") {
			out = append(out, full)
		}
	}
	return out
}

// fileTools are tool calls whose input is a file operation, not a command.
var fileTools = map[string]bool{
	"create_file": true, "write_file": true, "str_replace": true, "view": true,
	"str_replace_based_edit_tool": true, "present_files": true,
}

var pathKeys = []string{"path", "file_path", "filepaths", "old_path", "new_path"}

// discoverOutputPaths returns every .lean file under dir that a conversation
// export mentions — in a tool call or in a tool result — deduplicated, in order of
// first appearance. create_file / write_file paths are included by the same rule
// (their path field is a string in the tool input).
func discoverOutputPaths(rawJSON []byte, dir string) []string {
	var conv struct {
		ChatMessages []struct {
			Content []json.RawMessage `json:"content"`
		} `json:"chat_messages"`
	}
	if json.Unmarshal(rawJSON, &conv) != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(ps []string) {
		for _, p := range ps {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	for _, msg := range conv.ChatMessages {
		for _, raw := range msg.Content {
			var block struct {
				Type    string          `json:"type"`
				Name    string          `json:"name"`
				Input   json.RawMessage `json:"input"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(raw, &block) != nil {
				continue
			}
			var texts []string
			switch block.Type {
			case "tool_use":
				var in any
				if json.Unmarshal(block.Input, &in) == nil {
					if fileTools[block.Name] {
						// a file tool names its file in a path field; its body is the
						// file's CONTENT, which may mention other .lean names that
						// were never written
						if m, ok := in.(map[string]any); ok {
							for _, k := range pathKeys {
								if v, ok := m[k]; ok {
									collectStrings(v, &texts)
								}
							}
						}
					} else {
						collectStrings(in, &texts)
					}
				}
			case "tool_result":
				var c any
				if json.Unmarshal(block.Content, &c) == nil {
					collectStrings(c, &texts)
				}
			}
			for _, t := range texts {
				add(candidatesInText(t, dir))
			}
		}
	}
	return out
}

const maxChunkFileBytes = 2 << 20

// downloadChunkFiles asks the sandbox for every candidate and keeps those that
// exist: non-empty, valid UTF-8, a sane size. Secrets are redacted the same way
// the conversation JSON is, since these files land in a git-tracked tree.
func downloadChunkFiles(convID string, rawJSON []byte, dir string) []chunkFile {
	cands := discoverOutputPaths(rawJSON, dir)
	var files []chunkFile
	seenName := map[string]bool{}
	var dropped []string
	for _, p := range cands {
		b, err := DownloadFile(convID, p)
		if err != nil || len(b) == 0 || len(b) > maxChunkFileBytes || !utf8.Valid(b) {
			dropped = append(dropped, path.Base(p))
			continue
		}
		name := path.Base(p)
		if seenName[name] {
			continue
		}
		seenName[name] = true
		files = append(files, chunkFile{RemotePath: p, Name: name, Content: []byte(redactSecrets(string(b)))})
	}
	log.Printf("chunk_files: %s: %d candidate path(s), %d exist in the sandbox, %d dropped",
		shortID(convID), len(cands), len(files), len(dropped))
	return files
}

// renderChunkFiles renders files in the form DownloadConv gives create_file
// content ("## FILE: <path>" then the text), so dev_extract_file_sections reads it
// unchanged.
func renderChunkFiles(files []chunkFile) string {
	var b strings.Builder
	for _, f := range files {
		b.WriteString("\n## FILE: " + f.RemotePath + "\n")
		b.Write(f.Content)
		b.WriteString("\n\n")
	}
	return b.String()
}

// ---- one download per conversation --------------------------------------------------

var (
	chunkFilesMu   sync.Mutex
	chunkFilesMemo = map[string][]chunkFile{}
)

// chunkFilesFor returns the conversation's files, downloading them once: ref_split
// asks for them to build the chunk's theory text and ref_store_chunk asks again to
// store them.
func chunkFilesFor(convID string) []chunkFile {
	if convID == "" {
		return nil
	}
	chunkFilesMu.Lock()
	if f, ok := chunkFilesMemo[convID]; ok {
		chunkFilesMu.Unlock()
		return f
	}
	chunkFilesMu.Unlock()

	raw, err := DownloadConvRaw(convID)
	if err != nil {
		log.Printf("chunk_files: %s: download raw failed: %v", shortID(convID), err)
		return nil
	}
	files := downloadChunkFiles(convID, raw, TheoryOutputsDir)

	chunkFilesMu.Lock()
	chunkFilesMemo[convID] = files
	chunkFilesMu.Unlock()
	return files
}

func forgetChunkFiles(convID string) {
	chunkFilesMu.Lock()
	delete(chunkFilesMemo, convID)
	chunkFilesMu.Unlock()
}
