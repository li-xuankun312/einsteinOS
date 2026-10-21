package kernel

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

type SplitNode struct {
	Pid      int64
	Parent   int64
	Depth    int
	Files    []string
	SHA      string
	Owner    string
	Repo     string
	Result   string
	Children []int64
}

var (
	split_table [NR_SPLITS]SplitNode
	nr_splits   int
	split_mu    sync.Mutex
)

const NR_SPLITS = 256
const SPLIT_CHUNK = 40

func ref_split(parent_pid int64, owner, repo, sha string, files []string, task string, depth int) string {
	if len(files) <= SPLIT_CHUNK {
		return ref_split_leaf(parent_pid, owner, repo, sha, files, task, depth)
	}

	// interleave files by directory so each chunk gets a mix
	// (not all cuda in chunk1 and all cpp in chunk3).
	// group by parent dir, then round-robin across chunks.
	nChunks := (len(files) + SPLIT_CHUNK - 1) / SPLIT_CHUNK
	chunks := make([][]string, nChunks)

	// group by directory
	dirOrder := []string{}
	dirFiles := map[string][]string{}
	for _, f := range files {
		d := f
		if idx := strings.LastIndex(f, "/"); idx > 0 {
			d = f[:idx]
		}
		if _, ok := dirFiles[d]; !ok {
			dirOrder = append(dirOrder, d)
		}
		dirFiles[d] = append(dirFiles[d], f)
	}

	// round-robin: distribute each dir's files across chunks
	ci := 0
	for _, d := range dirOrder {
		for _, f := range dirFiles[d] {
			chunks[ci%nChunks] = append(chunks[ci%nChunks], f)
			ci++
		}
	}

	log.Printf("ref_split: %s/%s@%s depth=%d %d files -> %d chunks",
		owner, repo, short_sha(sha), depth, len(files), len(chunks))

	node := split_alloc(parent_pid, depth, owner, repo, sha, files)

	var child_pids []int64
	var results []string
	for i, chunk := range chunks {
		child_pid := ref_split_fork(parent_pid, owner, repo, sha, chunk, task, depth+1, i, len(chunks), chunks, results)
		if child_pid <= 0 {
			continue
		}
		child_pids = append(child_pids, child_pid)
		Wait(child_pid)

		// After Wait, goroutine is done — ConvID is now in TaskMeta.
		// Register with proper repo/role for FindConv lookups.
		role := fmt.Sprintf("theorize_chunk_%d", i+1)
		if m := getMeta(child_pid); m != nil && m.ConvID != "" {
			RegisterConv(parent_pid, m.ConvID, owner+"/"+repo, role, 0)
		}

		result := GetResult(child_pid)

		// always try DownloadConv — child writes theory to create_file
		// (e.g. strata_chunk2_analysis.md with ## FILE: headers),
		// which is NOT in GetResult stdout. only the full conversation
		// download captures create_file content.
		convID := ""
		if m := getMeta(child_pid); m != nil {
			convID = m.ConvID
		}
		if convID == "" {
			if rec := FindConv(owner+"/"+repo, role, 0); rec != nil {
				convID = rec.ConvID
			}
		}
		if convID != "" {
			downloaded, err := DownloadConv(convID)
			if err == nil && len(downloaded) > len(result) {
				log.Printf("ref_split: chunk %d/%d GetResult=%d, downloaded=%d, using download",
					i+1, len(chunks), len(result), len(downloaded))
				result = downloaded
			} else if err != nil {
				log.Printf("ref_split: chunk %d/%d download failed (using GetResult=%d): %v",
					i+1, len(chunks), len(result), err)
			}

			// DownloadFile: theory is in create_file outputs, not conversation text.
			// scan GetResult for /mnt/user-data/outputs/*.md paths and download them.
			// these files have ## FILE: headers that dev_split_theory needs.
			for _, line := range strings.Split(GetStderr(child_pid), "\n") {
				// shadow exec logs: [pid N] exec: mkdir -p ... && cat > '/mnt/user-data/outputs/xxx.md'
				if strings.Contains(line, "/mnt/user-data/outputs/") && strings.Contains(line, ".md") {
					// extract path
					start := strings.Index(line, "/mnt/user-data/outputs/")
					if start < 0 {
						continue
					}
					rest := line[start:]
					// find end of path (quote, space, or EOL)
					end := len(rest)
					for _, delim := range []byte{'\'', '"', ' ', '\t', '\n', '\r'} {
						if idx := strings.IndexByte(rest, delim); idx > 0 && idx < end {
							end = idx
						}
					}
					path := rest[:end]
					if !strings.HasSuffix(path, ".md") {
						continue
					}

					fileBytes, err := DownloadFile(convID, path)
					if err != nil {
						log.Printf("ref_split: chunk %d/%d download-file %s failed: %v", i+1, len(chunks), path, err)
						continue
					}
					fileStr := string(fileBytes)
					if strings.Contains(fileStr, "## FILE:") || strings.Contains(fileStr, "LAYER 1") {
						log.Printf("ref_split: chunk %d/%d download-file %s: %d bytes (has ## FILE headers!)",
							i+1, len(chunks), path, len(fileStr))
						if len(fileStr) > len(result) {
							result = fileStr
						} else {
							result = result + "\n\n" + fileStr
						}
					}
				}
			}
		}

		// theory completeness: count ## FILE: headers vs assigned files.
		// if missing, ContinueConv on the same theorize conversation.
		// the conversation has prior file context → theories stay connected.
		if convID != "" && len(result) >= 500 {
			covered := map[string]bool{}
			for _, line := range strings.Split(result, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "## FILE:") {
					path := strings.TrimSpace(trimmed[len("## FILE:"):])
					covered[path] = true
				} else if strings.Contains(trimmed, "## FILE:") {
					idx := strings.Index(trimmed, "## FILE:")
					path := strings.TrimSpace(trimmed[idx+len("## FILE:"):])
					covered[path] = true
				}
			}
			var missing []string
			for _, f := range chunk {
				if !covered[f] {
					missing = append(missing, f)
				}
			}
			if len(missing) > 0 && len(missing) < len(chunk) {
				log.Printf("ref_split: chunk %d/%d has %d/%d files covered, %d missing — ContinueConv to recover",
					i+1, len(chunks), len(covered), len(chunk), len(missing))
				var prompt strings.Builder
				prompt.WriteString(fmt.Sprintf("Continue. %d files remaining:\n", len(missing)))
				for _, f := range missing {
					prompt.WriteString(f + "\n")
				}
				contResult, _ := ContinueConv(convID, prompt.String())
				if len(contResult) > 100 {
					// re-download full conversation to get everything including create_file
					fullDownload, err := DownloadConv(convID)
					if err == nil && len(fullDownload) > len(result) {
						log.Printf("ref_split: chunk %d/%d ContinueConv + re-download: %d bytes (was %d)",
							i+1, len(chunks), len(fullDownload), len(result))
						result = fullDownload
					} else {
						log.Printf("ref_split: chunk %d/%d ContinueConv appended %d bytes",
							i+1, len(chunks), len(contResult))
						result = result + "\n\n" + contResult
					}
				}
			}
		}

		// detect bad results: too thin OR child refused (social engineering detection).
		needsRetry := false
		if len(result) < 500 {
			log.Printf("ref_split: chunk %d/%d result too thin (%d bytes)", i+1, len(chunks), len(result))
			needsRetry = true
		}
		if strings.Contains(result, "don't have any prior context") ||
			strings.Contains(result, "social engineering") ||
			strings.Contains(result, "no earlier tool calls") ||
			strings.Contains(result, "don't have context") {
			log.Printf("ref_split: chunk %d/%d child refused (writeback confusion), retrying fresh",
				i+1, len(chunks))
			needsRetry = true
		}

		if needsRetry && convID != "" {
			// SSE broke but remote may still be generating. the conversation
			// on the server keeps running even after our SSE connection drops.
			// poll every 3min: download conversation, compare size to previous.
			// if size grows → remote still working, keep waiting.
			// if size stable and > 500 → remote finished, use it.
			// if size stable and <= 500 after 30min → give up, retry fresh.
			log.Printf("ref_split: chunk %d/%d too thin, polling remote conv %s (3min intervals, max 30min)",
				i+1, len(chunks), convID[:8])
			prevSize := len(result)
			stableCount := 0
			for poll := 0; poll < 10; poll++ { // 10 × 3min = 30min max
				time.Sleep(3 * time.Minute)
				redownloaded, err := DownloadConv(convID)
				curSize := 0
				if err == nil {
					curSize = len(redownloaded)
				}
				log.Printf("ref_split: chunk %d/%d poll %d/10: %d bytes (prev %d)",
					i+1, len(chunks), poll+1, curSize, prevSize)

				if curSize == 0 && prevSize == 0 {
					// conversation is dead/empty — don't waste 30min polling nothing
					log.Printf("ref_split: chunk %d/%d poll %d: 0 bytes, conversation dead — retry now",
						i+1, len(chunks), poll+1)
					break
				}
				if curSize > 500 {
					if curSize == prevSize {
						stableCount++
					} else {
						stableCount = 0
					}
					// stable for 2 consecutive polls (6min) = remote is done
					if stableCount >= 2 {
						log.Printf("ref_split: chunk %d/%d remote finished: %d bytes stable for 6min",
							i+1, len(chunks), curSize)
						result = redownloaded
						needsRetry = false
						break
					}
					// size grew but already big enough — remote still going, update result
					if curSize > len(result) {
						result = redownloaded
					}
				}
				prevSize = curSize
			}
			// if we exited the loop with a good result, needsRetry is already false.
			// if result grew past 500 during polling but never stabilized, use what we have.
			if needsRetry && len(result) > 500 {
				log.Printf("ref_split: chunk %d/%d polling timeout but got %d bytes, using partial",
					i+1, len(chunks), len(result))
				needsRetry = false
			}
		}

		// retry up to 3 times — SSE breaks are network issues (proxy, TLS),
		// same chunk can hit it twice in a row when the network is bad.
		for attempt := 1; attempt <= 3 && needsRetry; attempt++ {
			log.Printf("ref_split: chunk %d/%d retry %d/3 with fresh conversation", i+1, len(chunks), attempt)
			retryPid := ref_split_fork(parent_pid, owner, repo, sha, chunk, task, depth+1, i, len(chunks), chunks, results)
			if retryPid <= 0 {
				break
			}
			Wait(retryPid)
			retryResult := GetResult(retryPid)
			retryConvID := ""
			if m := getMeta(retryPid); m != nil && m.ConvID != "" {
				retryConvID = m.ConvID
				RegisterConv(parent_pid, m.ConvID, owner+"/"+repo, fmt.Sprintf("%s_retry%d", role, attempt), 0)
			}
			// also download the retry conversation
			if retryConvID != "" {
				if dl, err := DownloadConv(retryConvID); err == nil && len(dl) > len(retryResult) {
					retryResult = dl
				}
			}
			if len(retryResult) > len(result) {
				log.Printf("ref_split: chunk %d/%d retry %d/3 got %d bytes (was %d)",
					i+1, len(chunks), attempt, len(retryResult), len(result))
				result = retryResult
			}
			if len(result) >= 500 {
				needsRetry = false
			}
		}

		results = append(results, result)
		log.Printf("ref_split: %s/%s@%s chunk %d/%d done (%d bytes)",
			owner, repo, short_sha(sha), i+1, len(chunks), len(result))
	}

	if node >= 0 {
		split_mu.Lock()
		split_table[node].Children = child_pids
		split_mu.Unlock()
	}

	merged := strings.Join(results, "\n\n")
	log.Printf("ref_split: %s/%s@%s depth=%d merged %d children -> %d bytes",
		owner, repo, short_sha(sha), depth, len(child_pids), len(merged))
	return merged
}

func ref_split_fork(parent_pid int64, owner, repo, sha string, files []string, task string, depth int, chunk_idx int, chunk_total int, allChunks [][]string, priorResults []string) int64 {
	dir := fmt.Sprintf("/tmp/ref_%s_%s.git", owner, repo)

	var src strings.Builder
	src.WriteString(fmt.Sprintf("[TASK] %s\n\n", task))
	src.WriteString(fmt.Sprintf("[REFERENCE] %s/%s commit %s\n", owner, repo, short_sha(sha)))
	src.WriteString(fmt.Sprintf("[CHUNK %d/%d] %d files (depth %d)\n", chunk_idx+1, chunk_total, len(files), depth))
	src.WriteString("produce LAYER 1/2 ONLY for YOUR files below.\n\n")

	// prior chunks: give child the conversation links to download
	if len(priorResults) > 0 {
		hasPrior := false
		for j := range priorResults {
			role := fmt.Sprintf("theorize_chunk_%d", j+1)
			rec := FindConv(owner+"/"+repo, role, 0)
			if rec == nil {
				continue
			}
			if !hasPrior {
				src.WriteString("[PRIOR CHUNKS — already completed by sibling conversations]\n")
				hasPrior = true
			}
			src.WriteString(fmt.Sprintf("  chunk %d/%d: conv=%s (%d bytes output)\n",
				j+1, chunk_total, rec.ConvID, len(priorResults[j])))
		}
		if hasPrior {
			src.WriteString("download their artifacts if you need context. produce theory for YOUR files only.\n\n")
		}
	}

	for _, f := range files {
		if isBinaryFile(f) {
			continue
		}
		content, err := RefFileAt(owner, repo, sha, f)
		if err != nil {
			continue
		}
		if len(content) > 30000 {
			content = content[:30000]
		}
		src.WriteString(fmt.Sprintf("=== %s (%d lines) ===\n", f, strings.Count(content, "\n")+1))
		src.WriteString(content)
		src.WriteString("\n\n")
	}

	src_text := src.String()
	if len(src_text) > 100000 {
		src_text = src_text[:100000]
	}

	// INSTRUCTION FIRST, source code second.
	// if source comes first, the child reads 2000+ lines of CUDA and starts
	// "porting" it instead of writing mathematical theory. instruction must
	// be the first thing the child sees after the task header.
	prompt := theorize_instruction +
		"\nCRITICAL RULES:\n" +
		"- do NOT write .cu, .cpp, .py, or any implementation code\n" +
		"- do NOT create build scripts, Makefiles, or test files\n" +
		"- do NOT push to any git repository\n" +
		"- ONLY output: ## FILE headers + LAYER 1a (LaTeX) + LAYER 1b (Lean 4) + LAYER 2 (form template)\n" +
		"- write theory as TEXT in your response, or use create_file for .md files only\n\n" +
		"you may use git to read other files for CONTEXT (understanding types, interfaces, dependencies).\n" +
		"the bare repo is at " + dir + " — use `git --git-dir=" + dir + " show " + short_sha(sha) + ":<path>`.\n" +
		"but produce LAYER 1/2 ONLY for YOUR files listed above. other chunks handle the rest.\n" +
		"if any single file is too large to fully analyze, " +
		"list its functions and produce both layers for the most important ones.\n\n" +
		"[SOURCE CODE for your chunk — read these, then write THEORY (not code) for them]\n\n" +
		src_text

	pid, err := Fork(prompt, ".")
	if err != nil {
		log.Printf("ref_split_fork: %v", err)
		return 0
	}

	split_alloc(parent_pid, depth, owner, repo, sha, files)

	log.Printf("ref_split_fork: pid=%d chunk=%d/%d depth=%d files=%d",
		pid, chunk_idx+1, chunk_total, depth, len(files))
	return pid
}

func ref_split_leaf(parent_pid int64, owner, repo, sha string, files []string, task string, depth int) string {
	dir := fmt.Sprintf("/tmp/ref_%s_%s.git", owner, repo)

	var src strings.Builder
	src.WriteString(fmt.Sprintf("[TASK] %s\n\n", task))
	src.WriteString(fmt.Sprintf("[REFERENCE] %s/%s commit %s\n", owner, repo, short_sha(sha)))
	src.WriteString(fmt.Sprintf("[LEAF] %d files (depth %d)\n\n", len(files), depth))

	for _, f := range files {
		if isBinaryFile(f) {
			continue
		}
		content, err := RefFileAt(owner, repo, sha, f)
		if err != nil {
			continue
		}
		if len(content) > 50000 {
			content = content[:50000]
		}
		src.WriteString(fmt.Sprintf("=== %s (%d lines) ===\n", f, strings.Count(content, "\n")+1))
		src.WriteString(content)
		src.WriteString("\n\n")
	}

	src_text := src.String()
	if len(src_text) > 120000 {
		src_text = src_text[:120000]
	}

	prompt := src_text + theorize_instruction +
		"the repo is at " + dir + " if you need git.\n\n" +
		"CRITICAL: output your LAYER 1 and LAYER 2 directly as text in your response. " +
		"do NOT use create_file, Artifact, or present_files. " +
		"do NOT write to /mnt/user-data/outputs/. " +
		"your text response IS the output. just print the math and templates."

	pid, err := Fork(prompt, ".")
	if err != nil {
		return fmt.Sprintf("ref_split_leaf: %v", err)
	}

	log.Printf("ref_split_leaf: pid=%d depth=%d files=%d", pid, depth, len(files))
	Wait(pid)
	result := GetResult(pid)
	theorem_store(owner+"/"+repo, sha, result, pid)
	return result
}

func split_alloc(parent int64, depth int, owner, repo, sha string, files []string) int {
	split_mu.Lock()
	defer split_mu.Unlock()
	if nr_splits >= NR_SPLITS {
		return -1
	}
	idx := nr_splits
	split_table[idx] = SplitNode{
		Parent: parent,
		Depth:  depth,
		Owner:  owner,
		Repo:   repo,
		SHA:    sha,
		Files:  files,
	}
	nr_splits++
	return idx
}

func ref_split_commit(parent_pid int64, owner, repo string, commits []RefCommit, task string) string {
	if len(commits) <= 1 {
		if len(commits) == 0 {
			return ""
		}
		c := commits[0]
		files, _ := RefTreeAt(owner, repo, c.SHA)
		if len(files) > SPLIT_CHUNK {
			return ref_split(parent_pid, owner, repo, c.SHA, files, task, 0)
		}
		return ref_split_leaf(parent_pid, owner, repo, c.SHA, files, task, 0)
	}

	log.Printf("ref_split_commit: %s/%s %d commits", owner, repo, len(commits))

	var pids []int64
	for _, c := range commits {
		_, changed_files, _ := RefReadCommit(owner, repo, c.SHA)
		if len(changed_files) == 0 {
			files, _ := RefTreeAt(owner, repo, c.SHA)
			changed_files = files
		}

		if len(changed_files) > SPLIT_CHUNK {
			go func(commit RefCommit, files []string) {
				ref_split(parent_pid, owner, repo, commit.SHA, files, task, 0)
			}(c, changed_files)
			continue
		}

		pid := ref_split_fork(parent_pid, owner, repo, c.SHA, changed_files, task, 0, 0, 1, nil, nil)
		if pid > 0 {
			pids = append(pids, pid)
		}
	}

	var results []string
	for _, pid := range pids {
		Wait(pid)
		results = append(results, GetResult(pid))
	}

	merged := strings.Join(results, "\n\n")
	log.Printf("ref_split_commit: %s/%s merged %d commits -> %d bytes",
		owner, repo, len(commits), len(merged))
	return merged
}

func SplitStatus() string {
	split_mu.Lock()
	defer split_mu.Unlock()
	if nr_splits == 0 {
		return "no splits"
	}
	out := fmt.Sprintf("  %-6s %-6s %-5s %-30s %-6s %s\n",
		"IDX", "PARENT", "DEPTH", "REPO", "FILES", "CHILDREN")
	for i := 0; i < nr_splits; i++ {
		s := split_table[i]
		out += fmt.Sprintf("  %-6d %-6d %-5d %-30s %-6d %v\n",
			i, s.Parent, s.Depth,
			s.Owner+"/"+s.Repo, len(s.Files), s.Children)
	}
	return out
}
