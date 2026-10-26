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

	// theory_planner decides how to group files — not static round-robin.
	// planner sees file list with directories, groups related files,
	// keeps each group ≤ 12 files so theory conv can finish within round limit.
	chunks := theory_plan(parent_pid, owner, repo, files)

	log.Printf("ref_split: %s/%s@%s depth=%d %d files -> %d chunks (planner)",
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
					if strings.Contains(fileStr, "## MODULE:") || strings.Contains(fileStr, "## FILE:") || strings.Contains(fileStr, "LAYER 1") {
						log.Printf("ref_split: chunk %d/%d download-file %s: %d bytes (has theory headers!)",
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

		// theory completeness: count ## MODULE: or ## FILE: headers vs assigned files.
		// ## MODULE: uses abstract names so we count modules vs expected file count.
		// ## FILE: (legacy) matches exact paths.
		// if missing, ContinueConv on the same theorize conversation.
		if convID != "" && len(result) >= 500 {
			covered := map[string]bool{}
			moduleCount := 0
			for _, line := range strings.Split(result, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "## MODULE:") || strings.Contains(trimmed, "## MODULE:") {
					moduleCount++
				} else if strings.HasPrefix(trimmed, "## FILE:") {
					path := strings.TrimSpace(trimmed[len("## FILE:"):])
					covered[path] = true
				} else if strings.Contains(trimmed, "## FILE:") {
					idx := strings.Index(trimmed, "## FILE:")
					path := strings.TrimSpace(trimmed[idx+len("## FILE:"):])
					covered[path] = true
				}
			}
			var missing []string
			if moduleCount > 0 {
				// new format: ## MODULE headers. check count vs file count.
				// modules may not be 1:1 with files (e.g. small files grouped),
				// so only flag missing if we have <50% coverage.
				if moduleCount < len(chunk)/2 {
					// estimate missing: files not yet covered
					for idx := moduleCount; idx < len(chunk); idx++ {
						missing = append(missing, chunk[idx])
					}
				}
				log.Printf("ref_split: chunk %d/%d has %d ## MODULE headers for %d files",
					i+1, len(chunks), moduleCount, len(chunk))
			} else {
				// legacy format: ## FILE headers with exact paths
				for _, f := range chunk {
					if !covered[f] {
						missing = append(missing, f)
					}
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
			log.Printf("ref_split: chunk %d/%d too thin, polling remote conv %s (3min intervals, exit after 5 stale)",
				i+1, len(chunks), convID[:8])
			prevSize := len(result)
			maxSeen := len(result)
			stableCount := 0
			staleCount := 0     // consecutive polls with no growth (including errors)
			continueWaste := 0  // consecutive ContinueConv with tiny pure-text response (child says "done")
			for poll := 0; staleCount < 5; poll++ {
				time.Sleep(3 * time.Minute)
				redownloaded, dlErr := DownloadConv(convID)
				curSize := 0
				if dlErr == nil {
					curSize = len(redownloaded)
				}
				if dlErr != nil {
					// download failed — but remote might still be running.
					// probe before counting as stale.
					if busy, probeErr := ProbeConvBusy(convID); probeErr == nil && busy {
						log.Printf("ref_split: chunk %d/%d poll %d: download error but remote still busy, waiting",
							i+1, len(chunks), poll+1)
						// don't increment staleCount — remote is alive
						continue
					}
					staleCount++
					log.Printf("ref_split: chunk %d/%d poll %d: download error (stale %d/5): %v",
						i+1, len(chunks), poll+1, staleCount, dlErr)
					continue
				}

				log.Printf("ref_split: chunk %d/%d poll %d: %d bytes (prev %d, max %d, stale %d/5)",
					i+1, len(chunks), poll+1, curSize, prevSize, maxSeen, staleCount)

				if curSize == 0 {
					if maxSeen <= 500 {
						// probe: maybe remote is generating but hasn't flushed to conversation yet
						if busy, probeErr := ProbeConvBusy(convID); probeErr == nil && busy {
							log.Printf("ref_split: chunk %d/%d poll %d: 0 bytes but remote still busy, waiting",
								i+1, len(chunks), poll+1)
							continue
						}
						log.Printf("ref_split: chunk %d/%d poll %d: 0 bytes, never seen content, remote idle — conversation dead",
							i+1, len(chunks), poll+1)
						break
					}
					staleCount++
					continue
				}

				if curSize > maxSeen {
					maxSeen = curSize
					staleCount = 0 // content grew — reset stale counter
				} else {
					// content didn't grow — probe to check if remote is still running
					if busy, probeErr := ProbeConvBusy(convID); probeErr == nil && busy {
						log.Printf("ref_split: chunk %d/%d poll %d: %d bytes unchanged but remote still busy, waiting",
							i+1, len(chunks), poll+1, curSize)
						// don't increment staleCount
					} else {
						// remote idle + content stale → try ContinueConv to resume
						// (probe no longer sends continue, so we do it properly here)
						if continueWaste >= 2 {
							log.Printf("ref_split: chunk %d/%d poll %d: ContinueConv returned waste %d times, child is done",
								i+1, len(chunks), poll+1, continueWaste)
							staleCount = 5 // force exit
						} else if staleCount == 0 && curSize > 500 {
							log.Printf("ref_split: chunk %d/%d poll %d: remote idle, sending ContinueConv to resume",
								i+1, len(chunks), poll+1)
							contOut, contErr := ContinueConv(convID, "you were interrupted. continue writing ## MODULE sections for YOUR assigned files only. do NOT write theory for files outside your assignment.")
							if len(contOut) < 4000 && len(contErr) == 0 {
								continueWaste++
								log.Printf("ref_split: chunk %d/%d poll %d: ContinueConv waste (stdout=%d stderr=0) %d/2",
									i+1, len(chunks), poll+1, len(contOut), continueWaste)
							} else {
								continueWaste = 0
							}
						} else {
							staleCount++
						}
					}
				}

				if curSize > 500 {
					if curSize == prevSize {
						stableCount++
					} else {
						stableCount = 0
					}
					if stableCount >= 2 {
						log.Printf("ref_split: chunk %d/%d remote finished: %d bytes stable for 6min",
							i+1, len(chunks), curSize)
						result = redownloaded
						needsRetry = false
						break
					}
					if curSize > len(result) {
						result = redownloaded
					}
				}
				prevSize = curSize
			}
			// if we exited the loop with a good result, needsRetry is already false.
			// if result grew past 500 during polling but never stabilized,
			// wait for network recovery then try one final download.
			if needsRetry && len(result) > 500 {
				log.Printf("ref_split: chunk %d/%d polling exhausted, waiting 5min for network recovery then retrying download",
					i+1, len(chunks))
				time.Sleep(5 * time.Minute)
				if recoveredDL, recoveredErr := DownloadConv(convID); recoveredErr == nil && len(recoveredDL) >= len(result) {
					log.Printf("ref_split: chunk %d/%d post-wait download: %d bytes (was %d)",
						i+1, len(chunks), len(recoveredDL), len(result))
					result = recoveredDL
				} else if recoveredErr != nil {
					log.Printf("ref_split: chunk %d/%d post-wait download failed: %v, using previous %d bytes",
						i+1, len(chunks), recoveredErr, len(result))
				}
				needsRetry = false
			}
		}

		// retry up to 5 times — SSE breaks are transient network issues.
		// 5 is enough for proxy/TLS flaps. beyond that, stop and report.
		lastRetryConvID := ""
		for attempt := 1; attempt <= 5 && needsRetry; attempt++ {
			log.Printf("ref_split: chunk %d/%d retry %d/5 with fresh conversation", i+1, len(chunks), attempt)
			retryPid := ref_split_fork(parent_pid, owner, repo, sha, chunk, task, depth+1, i, len(chunks), chunks, results)
			if retryPid <= 0 {
				break
			}
			Wait(retryPid)
			retryResult := GetResult(retryPid)
			retryConvID := ""
			if m := getMeta(retryPid); m != nil && m.ConvID != "" {
				retryConvID = m.ConvID
				RegisterConv(parent_pid, m.ConvID, owner+"/"+repo, fmt.Sprintf("%s_r%d", role, attempt), 0)
			}
			// also download the retry conversation
			if retryConvID != "" {
				if dl, err := DownloadConv(retryConvID); err == nil && len(dl) > len(retryResult) {
					retryResult = dl
				}
			}
			if len(retryResult) > len(result) {
				log.Printf("ref_split: chunk %d/%d attempt %d got %d bytes (was %d)",
					i+1, len(chunks), attempt, len(retryResult), len(result))
				result = retryResult
				lastRetryConvID = retryConvID
			}
			if len(result) >= 500 {
				needsRetry = false
			}
		}

		// theory completeness check on retry result too.
		// retry might also be partial (SSE broke again mid-way).
		if lastRetryConvID != "" && len(result) >= 500 {
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
				log.Printf("ref_split: chunk %d/%d retry result has %d/%d files, %d missing — ContinueConv",
					i+1, len(chunks), len(covered), len(chunk), len(missing))
				var prompt strings.Builder
				prompt.WriteString(fmt.Sprintf("Continue. %d files remaining:\n", len(missing)))
				for _, f := range missing {
					prompt.WriteString(f + "\n")
				}
				contResult, _ := ContinueConv(lastRetryConvID, prompt.String())
				if len(contResult) > 100 {
					fullDownload, err := DownloadConv(lastRetryConvID)
					if err == nil && len(fullDownload) > len(result) {
						result = fullDownload
					} else {
						result = result + "\n\n" + contResult
					}
				}
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

// ---------------------------------------------------------------------------
// theory_plan: fork a theory_planner conv to decide file grouping.
// the planner sees the file list with directory structure and decides
// which files go together. each group ≤ 12 files so theory conv can
// finish (read source + write 3-layer theory + Axle verify) within budget.
// ---------------------------------------------------------------------------

func theory_plan(parent_pid int64, owner, repo string, files []string) [][]string {
	// build file list grouped by directory for the planner
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

	var fileList strings.Builder
	for _, d := range dirOrder {
		fileList.WriteString(fmt.Sprintf("[%s] (%d files)\n", d, len(dirFiles[d])))
		for _, f := range dirFiles[d] {
			fileList.WriteString(fmt.Sprintf("  %s\n", f))
		}
	}

	prompt := fmt.Sprintf(
		"[TASK] group %d source files into chunks for theory analysis.\n\n"+
			"[IMPORTANT] the file list below IS the complete input. just group them.\n\n"+
			"[FILES by directory]\n%s\n"+
			"[RULES]\n"+
			"- each chunk will be analyzed by one conversation with ~50 tool calls budget\n"+
			"- each file needs ~4 tool calls (read source + write theory + verify)\n"+
			"- so each chunk should have roughly 8-12 files — enough to fill one session\n"+
			"- don't split too fine: a chunk with 1-3 files wastes a conversation\n"+
			"- don't lump too much: a chunk with 20+ files won't finish in one session\n"+
			"- files in the same directory should stay together when possible\n"+
			"- if a directory has many files, split it into multiple chunks\n"+
			"- related directories (e.g. include/x/ and src/x/) can go together\n\n"+
			"[OUTPUT FORMAT]\n"+
			"output ONLY lines like:\n"+
			"chunk: path/to/file1.hpp, path/to/file2.cpp, path/to/file3.cu\n"+
			"chunk: path/to/file4.hpp, path/to/file5.cpp\n"+
			"use EXACT paths from the list. one line per chunk. nothing else.\n",
		len(files), fileList.String())

	child, err := ForkWithRole(prompt, ".", "theory_planner")
	if err != nil {
		log.Printf("theory_plan: %s/%s planner fork failed: %v, using static split", owner, repo, err)
		return theory_static_split(files)
	}
	if m := getMeta(child); m != nil && m.ConvID != "" {
		RegisterConv(parent_pid, m.ConvID, owner+"/"+repo, "theory_planner", 0)
	}
	Wait(child)
	output := GetResult(child)
	log.Printf("theory_plan: %s/%s planner returned %d bytes", owner, repo, len(output))

	// parse "chunk: file1, file2, ..." lines
	fileSet := make(map[string]bool, len(files))
	for _, f := range files {
		fileSet[f] = true
	}

	var chunks [][]string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(line), "chunk:") {
			continue
		}
		rest := strings.TrimSpace(line[6:])
		if rest == "" {
			continue
		}
		var chunk []string
		for _, f := range strings.Split(rest, ",") {
			f = strings.TrimSpace(f)
			if f != "" && fileSet[f] {
				chunk = append(chunk, f)
				delete(fileSet, f)
			}
		}
		if len(chunk) > 0 {
			chunks = append(chunks, chunk)
		}
	}

	// any unclaimed files — append to last chunk or make new one
	if len(fileSet) > 0 {
		var unclaimed []string
		for f := range fileSet {
			unclaimed = append(unclaimed, f)
		}
		if len(chunks) > 0 && len(chunks[len(chunks)-1])+len(unclaimed) <= 15 {
			chunks[len(chunks)-1] = append(chunks[len(chunks)-1], unclaimed...)
		} else {
			// split unclaimed into groups of 12
			for i := 0; i < len(unclaimed); i += 12 {
				end := i + 12
				if end > len(unclaimed) {
					end = len(unclaimed)
				}
				chunks = append(chunks, unclaimed[i:end])
			}
		}
		log.Printf("theory_plan: %d unclaimed files redistributed", len(unclaimed))
	}

	if len(chunks) == 0 {
		log.Printf("theory_plan: planner output unparseable, using static split")
		return theory_static_split(files)
	}

	log.Printf("theory_plan: %s/%s %d files → %d chunks", owner, repo, len(files), len(chunks))
	for i, c := range chunks {
		log.Printf("theory_plan:   chunk %d: %d files", i+1, len(c))
	}
	return chunks
}

// theory_static_split: fallback — group by directory, split large dirs at 12.
func theory_static_split(files []string) [][]string {
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

	var chunks [][]string
	for _, d := range dirOrder {
		fs := dirFiles[d]
		for i := 0; i < len(fs); i += 12 {
			end := i + 12
			if end > len(fs) {
				end = len(fs)
			}
			chunks = append(chunks, fs[i:end])
		}
	}

	if len(chunks) == 0 {
		chunks = [][]string{files}
	}

	log.Printf("theory_static_split: %d files → %d chunks", len(files), len(chunks))
	return chunks
}

func ref_split_fork(parent_pid int64, owner, repo, sha string, files []string, task string, depth int, chunk_idx int, chunk_total int, allChunks [][]string, priorResults []string) int64 {
	dir := fmt.Sprintf("/tmp/ref_%s_%s.git", owner, repo)

	var src strings.Builder
	src.WriteString(fmt.Sprintf("[TASK] %s\n\n", task))
	src.WriteString(fmt.Sprintf("[REFERENCE] %s/%s commit %s\n", owner, repo, short_sha(sha)))
	src.WriteString(fmt.Sprintf("[YOUR ASSIGNMENT] %d files (depth %d)\n", len(files), depth))
	src.WriteString("produce LAYER 1/2 ONLY for YOUR files listed below. nothing else.\n")
	src.WriteString("other files in this repo are handled by OTHER conversations — not your concern.\n")
	src.WriteString("do NOT explore the repo to find more files to theorize. YOUR scope is FIXED.\n\n")

	// prior chunks: give child conv addresses for context, but do NOT
	// reveal how many chunks exist or what files other chunks cover.
	if len(priorResults) > 0 {
		hasPrior := false
		for j := range priorResults {
			role := fmt.Sprintf("theorize_chunk_%d", j+1)
			rec := FindConv(owner+"/"+repo, role, 0)
			if rec == nil {
				continue
			}
			if !hasPrior {
				src.WriteString("[PRIOR WORK — other conversations already produced theory for other files]\n")
				hasPrior = true
			}
			src.WriteString(fmt.Sprintf("  conv=%s (%d bytes)\n",
				rec.ConvID, len(priorResults[j])))
		}
		if hasPrior {
			src.WriteString("you may download these for context if needed. produce theory for YOUR files only.\n\n")
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
		"- ONLY output: ## MODULE headers + LAYER 1a (LaTeX) + LAYER 1b (Lean 4) + LAYER 2 (form template)\n" +
		"- write theory as TEXT in your response, or use create_file for .md files only\n" +
		"- STOP when you have covered every file in YOUR assignment. do NOT look for more files.\n\n" +
		"you may use git to read other files for CONTEXT (understanding types, interfaces, dependencies).\n" +
		"the bare repo is at " + dir + " — use `git --git-dir=" + dir + " show " + short_sha(sha) + ":<path>`.\n" +
		"but produce LAYER 1/2 ONLY for YOUR assigned files. when those are done, you are done.\n" +
		"if any single file is too large to fully analyze, " +
		"list its functions and produce both layers for the most important ones.\n\n" +
		"[SOURCE CODE — your complete assignment. write THEORY for these files, then stop]\n\n" +
		src_text

	role := fmt.Sprintf("theorize_chunk_%d", chunk_idx+1)
	pid, err := ForkWithRole(prompt, ".", role)
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

	pid, err := ForkWithRole(prompt, ".", "theorize_leaf")
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
