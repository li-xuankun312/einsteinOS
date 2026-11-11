package kernel

import (
	"fmt"
	"log"
	"os"
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
	return ref_split_with(parent_pid, owner, repo, sha, files, task, depth, nil)
}

// ref_split_with is ref_split with the chunking already decided (by the combined
// survey+plan conversation, ref_survey_plan.go). chunks == nil: ask the planner.
func ref_split_with(parent_pid int64, owner, repo, sha string, files []string, task string, depth int, planned [][]string) string {
	if len(files) <= SPLIT_CHUNK {
		return ref_split_leaf(parent_pid, owner, repo, sha, files, task, depth)
	}

	// theory_planner decides how to group files — not static round-robin.
	// planner sees file list with directories, groups related files,
	// keeps each group ≤ 12 files so theory conv can finish within round limit.
	chunks := planned
	if len(chunks) == 0 {
		chunks = theory_plan(parent_pid, owner, repo, files)
	}
	if merged := coalesceChunks(chunks); len(merged) != len(chunks) {
		log.Printf("ref_split: %s/%s the plan had %d chunks (sizes %v); chunks below %d files were merged → %d chunks",
			owner, repo, len(chunks), chunkSizes(chunks), chunkSmall, len(merged))
		chunks = merged
	}

	log.Printf("ref_split: %s/%s@%s depth=%d %d files -> %d chunks (planner)",
		owner, repo, short_sha(sha), depth, len(files), len(chunks))
	// the plan's identity names where its chunks are stored (theory_store.go)
	plan := newTheoryPlanRef(sha, chunks)
	log.Printf("ref_split: %s/%s@%s plan %s → %s", owner, repo, short_sha(sha), plan.Plan, plan.Dir())
	var prior []priorChunk // earlier chunks of THIS plan, by conversation address

	node := split_alloc(parent_pid, depth, owner, repo, sha, files)

	var child_pids []int64
	var results []string
	var chunkCovs []Coverage // final coverage per chunk (zeroed for junk)
	var junkChunks []int
	for i, chunk := range chunks {
		child_pid := ref_split_fork(parent_pid, owner, repo, sha, chunk, task, depth+1, i, len(chunks), chunks, prior)
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
			ConvDone(m.ConvID, "done") // Wait already returned above
		}

		result := GetResult(child_pid)
		rawResult := result // keep original stdout for refusal detection (before download overwrites)

		// the conversation was stopped because the server moved it to another model on its
		// first message (see ModelDowngradeMarker). The server may go on generating on that
		// other model, so downloading or polling the conversation would pick up exactly the
		// output we stopped it to avoid: skip both and go straight to a fresh conversation.
		downgraded := strings.Contains(rawResult, ModelDowngradeMarker)
		if downgraded {
			log.Printf("ref_split: chunk %d/%d conversation stopped on a model downgrade — retrying in a fresh conversation (not reading this one)", i+1, len(chunks))
		}

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
		if convID != "" && !downgraded {
			downloaded, err := DownloadConv(convID)
			if err == nil && len(downloaded) > len(result) {
				log.Printf("ref_split: chunk %d/%d GetResult=%d, downloaded=%d, using download",
					i+1, len(chunks), len(result), len(downloaded))
				result = downloaded
			} else if err != nil {
				log.Printf("ref_split: chunk %d/%d download failed (using GetResult=%d): %v",
					i+1, len(chunks), len(result), err)
			}

			// ref_store_chunk is called AFTER thin-check + polling below.
			// don't push until we have final data.
		}

		// theory completeness against the files THIS chunk was assigned (see
		// theory_coverage.go). The old check counted "## MODULE:" headers, a format the
		// pure-.lean prompt no longer produces, and mixed two metrics in one log line.
		cov := ChunkCoverage(chunk, result)
		if convID != "" && cov.CoveredCount() > 0 && len(cov.Missing) > 0 {
			result, cov = chunk_fill_missing(convID, chunk, result,
				fmt.Sprintf("chunk %d/%d", i+1, len(chunks)))
		}

		// detect bad results: too thin OR child refused (social engineering detection).
		needsRetry := false
		if chunkIsJunk(result, cov) {
			log.Printf("ref_split: chunk %d/%d result is junk (%d bytes, %d/%d assigned files covered)",
				i+1, len(chunks), len(result), cov.CoveredCount(), cov.Total)
			needsRetry = true
		}
		if strings.Contains(rawResult, "don't have any prior context") ||
			strings.Contains(rawResult, "social engineering") ||
			strings.Contains(rawResult, "no earlier tool calls") ||
			strings.Contains(rawResult, "don't have context") {
			log.Printf("ref_split: chunk %d/%d child refused (writeback confusion), retrying fresh",
				i+1, len(chunks))
			needsRetry = true
		}

		if needsRetry && convID != "" && !downgraded {
			// SSE broke but remote may still be generating. the conversation
			// on the server keeps running even after our SSE connection drops.
			// poll every 3min: download conversation, compare size to previous.
			// if size grows → remote still working, keep waiting.
			// if size stable and > 500 → remote finished, use it.
			// if size stable and <= 500 after 30min → give up, retry fresh.
			log.Printf("ref_split: chunk %d/%d too thin, polling remote conv %s (3min intervals, exit after 5 stale)",
				i+1, len(chunks), convID[:8])
			// bound the wait. A child that ran tools may legitimately take a long
			// time to persist (chunk 4's content appeared ~16 min after the break);
			// one that never ran a tool (account error at fork) has nothing to wait for.
			neverRan := false
			if col := GetCollector(child_pid); col != nil && col.ToolCalls() == 0 && len(rawResult) < 500 {
				neverRan = true
			}
			pollWait := 45 * time.Minute
			if neverRan {
				pollWait = 7 * time.Minute
			}
			pollDeadline := time.Now().Add(pollWait)
			prevSize := len(result)
			maxSeen := len(result)
			stableCount := 0
			staleCount := 0    // consecutive polls with no growth (including errors)
			continueWaste := 0 // consecutive ContinueConv with tiny pure-text response (child says "done")
			for poll := 0; staleCount < 5; poll++ {
				time.Sleep(3 * time.Minute)
				if time.Now().After(pollDeadline) {
					log.Printf("ref_split: chunk %d/%d poll %d: gave up after %s (child ran tools: %v)",
						i+1, len(chunks), poll+1, pollWait, !neverRan)
					break
				}
				redownloaded, dlErr := DownloadConv(convID)
				curSize := 0
				if dlErr == nil {
					curSize = len(redownloaded)
				}
				if dlErr != nil {
					// download failed — but remote might still be running.
					// probe before counting as stale.
					if busy, probeErr := remoteBusy(convID); probeErr == nil && busy {
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
						if busy, probeErr := remoteBusy(convID); probeErr == nil && busy {
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
					if busy, probeErr := remoteBusy(convID); probeErr == nil && busy {
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
						} else if staleCount == 0 && curSize > 500 && continueAllowed(convID) {
							// only a settled conversation may be sent a message; sending into
							// an unpersisted turn is what created chunk 1's junk branch
							log.Printf("ref_split: chunk %d/%d poll %d: conversation settled, sending ContinueConv to resume",
								i+1, len(chunks), poll+1)
							contOut, contErr := ContinueConv(convID, Bagu(fmt.Sprintf(chunkResumeWhereFmt, i+1, len(chunks)), chunkResumeWhat, fmt.Sprintf(chunkResumeContentFmt, strings.Join(chunks[i], "\n")), fmt.Sprint(len(chunks[i])), theoryTo()))
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
			retryPid := ref_split_fork(parent_pid, owner, repo, sha, chunk, task, depth+1, i, len(chunks), chunks, prior)
			if retryPid <= 0 {
				break
			}
			Wait(retryPid)
			retryResult := GetResult(retryPid)
			retryConvID := ""
			if m := getMeta(retryPid); m != nil && m.ConvID != "" {
				retryConvID = m.ConvID
				RegisterConv(parent_pid, m.ConvID, owner+"/"+repo, fmt.Sprintf("%s_r%d", role, attempt), 0)
				ConvDone(m.ConvID, "done") // Wait already returned above
			}
			// also download the retry conversation (not one that was itself stopped on a downgrade)
			if retryConvID != "" && !strings.Contains(retryResult, ModelDowngradeMarker) {
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
			if !chunkIsJunk(result, ChunkCoverage(chunk, result)) {
				needsRetry = false
			}
		}
		cov = ChunkCoverage(chunk, result)

		// completeness on the retry result too: it may also be partial.
		if lastRetryConvID != "" && cov.CoveredCount() > 0 && len(cov.Missing) > 0 {
			result, cov = chunk_fill_missing(lastRetryConvID, chunk, result,
				fmt.Sprintf("chunk %d/%d (retry conv)", i+1, len(chunks)))
		}

		// a junk result (apologies, error stubs) is NOT theory: do not merge it and
		// do not count it as coverage. 8 error stubs were merged in the Strata run.
		if chunkIsJunk(result, cov) {
			log.Printf("ref_split: chunk %d/%d discarded as junk after all retries (%d bytes)", i+1, len(chunks), len(result))
			junkChunks = append(junkChunks, i+1)
			chunkCovs = append(chunkCovs, Coverage{Total: cov.Total, Missing: append([]string{}, chunk...)})
			continue
		}
		chunkCovs = append(chunkCovs, cov)
		// the best convID: retry conv if it produced more, else original.
		storeConvID := convID
		if lastRetryConvID != "" {
			storeConvID = lastRetryConvID
		}
		// The theory text of this chunk is the files the conversation left in its
		// sandbox, in their FINAL state (chunk_files.go) — not the transcript, which
		// holds create_file bodies as first written and everything else the child
		// ran. Junk detection and coverage above still read the transcript.
		// a chunk does not end on sorry while the reference has more to show it
		// (proof_reference.go): measured on its files, continued with the sites
		if storeConvID != "" {
			chunk_fill_sorry(storeConvID, fmt.Sprintf("chunk %d/%d", i+1, len(chunks)))
		}
		theory := result
		nfiles := 0
		if files := chunkFilesFor(storeConvID); len(files) > 0 {
			theory = renderChunkFiles(files)
			nfiles = len(files)
			if ch := StatementChangesFor(storeConvID); len(ch) > 0 {
				theory += renderStatementChanges(ch)
				log.Printf("ref_split: chunk %d/%d: %d statement(s) changed after verification, recorded in its theory", i+1, len(chunks), len(ch))
			}
			log.Printf("ref_split: chunk %d/%d theory = %d file(s) from the sandbox, %d bytes (transcript was %d)",
				i+1, len(chunks), len(files), len(theory), len(result))
		}
		results = append(results, theory)
		if storeConvID != "" {
			// the kernel answers `einsteinos conv <addr> files|file` from this (spawn.go):
			// later chunks read this one by address, not from a branch
			if nfiles > 0 {
				SetConvResult(storeConvID, theory)
				prior = append(prior, priorChunk{Idx: i + 1, Conv: storeConvID, Files: nfiles})
			}
			// push final data to GitHub (after all polling, retries, ContinueConv).
			ref_store_chunk(storeConvID, i+1, owner, repo, plan)
		}

		log.Printf("ref_split: %s/%s@%s chunk %d/%d done (%d bytes)",
			owner, repo, short_sha(sha), i+1, len(chunks), len(result))
	}

	if node >= 0 {
		split_mu.Lock()
		split_table[node].Children = child_pids
		split_mu.Unlock()
	}

	if depth == 0 {
		sc := SplitCoverage{Chunks: len(chunks), Junk: junkChunks}
		for _, c := range chunkCovs {
			sc.Total += c.Total
			sc.Covered += c.CoveredCount()
			sc.Missing = append(sc.Missing, c.Missing...)
		}
		// chunks whose fork never started are absent from chunkCovs entirely
		if sc.Total < len(files) {
			sc.Total = len(files)
		}
		setSplitCoverage(owner+"/"+repo, sha, sc)
		log.Printf("ref_split: %s/%s@%s coverage %d/%d files (%d chunks, junk chunks %v)",
			owner, repo, short_sha(sha), sc.Covered, sc.Total, len(chunks), junkChunks)
	}

	merged := strings.Join(results, "\n\n")
	log.Printf("ref_split: %s/%s@%s depth=%d merged %d children -> %d bytes",
		owner, repo, short_sha(sha), depth, len(child_pids), len(merged))
	return merged
}

// ---------------------------------------------------------------------------
// ref_store_chunk: after a theory chunk completes, download everything it
// produced and push to the dev branch on GitHub. "everything" means:
//   1. the full conversation JSON (DownloadConvRaw)
//   2. every file the child created (DownloadFile for all create_file paths)
//
// stored under .einsteinOS/ on the branch so subsequent children can
// git checkout and read all prior work. no conversation_search needed,
// no kernel prompt injection of convIDs, no .md-only filter.
// ---------------------------------------------------------------------------

func ref_store_chunk(convID string, chunkIdx int, owner, repo string, plan theoryPlanRef) {
	devRepos := GetDevRepos()
	if len(devRepos) == 0 || convID == "" {
		return
	}
	parts := strings.Split(devRepos[0], "/")
	repoName := parts[len(parts)-1]
	chunkName := fmt.Sprintf("theorize_chunk_%d", chunkIdx) // for the log only
	if !plan.Valid() {
		log.Printf("ref_store_chunk: %s has no plan identity (no reference commit), not stored", chunkName)
		return
	}
	chunkRel := plan.ChunkDir(chunkIdx, convID) // .einsteinOS/theory/<ref12>/<plan>/<NN>-<conv12>

	// 1. download raw conversation JSON
	rawJSON, err := DownloadConvRaw(convID)
	if err != nil {
		log.Printf("ref_store_chunk: %s download raw failed: %v", chunkName, err)
		return
	}

	// write conv JSON to temp
	// children have repeatedly hardcoded the Axle session cookie and CSRF token
	// into verification scripts (/tmp/axle.sh, /tmp/verify.py, ...) they write via
	// bash_tool — the raw conversation transcript records those commands verbatim,
	// and this JSON is what lands in the git-tracked conv.json. GitHub's secret
	// scanning push protection correctly blocked it ("push declined due to
	// repository rule violations") — the fix is to never let the secret reach a
	// tracked file in the first place, not to react after GitHub catches it.
	rawJSON = []byte(redactSecrets(string(rawJSON)))

	convTmp := fmt.Sprintf("/tmp/chunk_%s_conv.json", convShort(convID))
	if err := os.WriteFile(convTmp, rawJSON, 0644); err != nil {
		log.Printf("ref_store_chunk: %s write conv tmp failed: %v", chunkName, err)
		return
	}

	// 2+3. the files the conversation left in its sandbox (chunk_files.go): every
	// .lean it wrote, by create_file or by bash or python, validated by downloading
	chunkDir := fmt.Sprintf("/tmp/chunk_%s_files", convShort(convID))
	os.MkdirAll(chunkDir, 0755)

	downloadedFiles := []string{} // local tmp paths
	destPaths := []string{}       // paths under .einsteinOS/
	for _, f := range chunkFilesFor(convID) {
		localTmp := fmt.Sprintf("%s/%s", chunkDir, f.Name)
		if err := os.WriteFile(localTmp, f.Content, 0644); err != nil {
			log.Printf("ref_store_chunk: %s write file %s failed: %v", chunkName, f.Name, err)
			continue
		}
		downloadedFiles = append(downloadedFiles, localTmp)
		destPaths = append(destPaths, chunkRel+"/"+f.Name)
		log.Printf("ref_store_chunk: %s downloaded %s (%d bytes)", chunkName, f.Name, len(f.Content))
	}
	forgetChunkFiles(convID)

	// 4. push to conv/<convID> branch (one branch per chunk)
	if len(downloadedFiles) == 0 {
		log.Printf("ref_store_chunk: %s 0 files downloaded, skipping push+PR", chunkName)
		return
	}
	convBranch := fmt.Sprintf("conv/%s", convID[:12])
	baseBranch := "einsteinOS/theory"
	repoDir := fmt.Sprintf("/tmp/ref_store_%s", repoName)

	// clone, checkout, copy files, commit — all via Go functions
	if err := GitCloneOrReuse(devRepos[0], repoDir); err != nil {
		log.Printf("ref_store_chunk: %s clone failed: %v", chunkName, err)
		return
	}
	if err := GitCheckoutOrCreate(repoDir, convBranch, baseBranch); err != nil {
		log.Printf("ref_store_chunk: %s checkout failed: %v", chunkName, err)
		return
	}

	// copy conv JSON + downloaded files into the work tree
	gitChunkDir := repoDir + "/" + chunkRel
	os.MkdirAll(gitChunkDir, 0755)

	convDest := fmt.Sprintf("%s/conv.json", gitChunkDir)
	copyFile(convTmp, convDest)
	// the same bytes from every chunk of the plan: git merges identical additions cleanly
	if err := os.WriteFile(repoDir+"/"+plan.PlanFile(), plan.Manifest(), 0644); err != nil {
		log.Printf("ref_store_chunk: %s write %s failed: %v", chunkName, plan.PlanFile(), err)
	}
	for i, localTmp := range downloadedFiles {
		copyFile(localTmp, fmt.Sprintf("%s/%s", repoDir, destPaths[i]))
	}

	commitMsg := fmt.Sprintf("theory: %s — conv %s, %d files → %s", chunkName, convID[:8], len(downloadedFiles), chunkRel)
	GitCommit(repoDir, commitMsg)

	// push with retry (Go-native, not bash)
	if err := GitPushRetry(repoDir, devRepos[0], convBranch, 5); err != nil {
		log.Printf("ref_store_chunk: %s push failed: %v", chunkName, err)
		return
	}
	log.Printf("ref_store_chunk: %s pushed to %s:%s — %d files, conv.json (%d bytes raw)",
		chunkName, devRepos[0], convBranch, len(downloadedFiles), len(rawJSON))

	// Merge this chunk's work into the SHARED branch — not a PR: nothing in this
	// codebase ever merges a PR for theory chunks (the only PR-merge code,
	// mergePRToMain, is for the final dev branch), so opening one here achieved
	// nothing; PriorChunksContext's "git checkout einsteinOS/theory" instruction
	// to later chunks pointed at a branch that no chunk's content had ever
	// actually reached. The kernel merges it itself, the same way it already
	// does every other git operation for children (7c5b7908: "children would
	// only push one or two files; kernel pushes all of them").
	//
	// gitUseBranchFromRemote (not a plain checkout) because baseBranch is shared
	// across every chunk of every run over time: this clone's local copy may be
	// behind what an earlier chunk (possibly from a concurrent run) already
	// merged, and basing the merge on a stale local tip would silently lose that
	// work instead of building on it.
	if err := gitUseBranchFromRemote(repoDir, baseBranch, "main"); err != nil {
		log.Printf("ref_store_chunk: %s checkout %s failed: %v — chunk work stays on %s only", chunkName, baseBranch, err, convBranch)
		return
	}
	mergeMsg := fmt.Sprintf("theory: merge %s — conv %s, %d files", chunkName, convID[:8], len(downloadedFiles))
	if err := GitMerge(repoDir, convBranch, mergeMsg); err != nil {
		// each chunk writes only under its own identity directory (theory_store.go) plus
		// its plan's plan.json, which every chunk of the plan writes with the same bytes,
		// so a conflict means something else touched those paths. The chunk's content is
		// still safe on convBranch, and later chunks of this plan read it through the
		// kernel by address, not from this branch.
		log.Printf("ref_store_chunk: %s merge into %s failed: %v — chunk work stays on %s only", chunkName, baseBranch, err, convBranch)
		return
	}
	if err := GitPushRetry(repoDir, devRepos[0], baseBranch, 20); err != nil {
		log.Printf("ref_store_chunk: %s push of merged %s failed: %v", chunkName, baseBranch, err)
		return
	}
	log.Printf("ref_store_chunk: %s merged into %s and pushed at %s", chunkName, baseBranch, chunkRel)
}

func copyFile(src, dst string) error {
	// ensure parent dir exists
	if idx := strings.LastIndex(dst, "/"); idx > 0 {
		os.MkdirAll(dst[:idx], 0755)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

// ---------------------------------------------------------------------------
// theory_plan: fork a theory_planner conv to decide file grouping.
// the planner sees the file list with directory structure and decides
// which files go together. each group ≤ 12 files so theory conv can
// finish (read source + write 3-layer theory + Axle verify) within budget.
// ---------------------------------------------------------------------------

// inserted into the shell for the chunk planner: %[1]s/%[2]s the reference
const theoryPlanWhereFmt = "参考仓库 %[1]s/%[2]s 的源文件"

// %[1]d files in %[2]d directories
const theoryPlanWhatFmt = "共 %[1]d 个源文件，分在 %[2]d 个目录"

// %[1]s the files by directory, %[2]d/%[3]d the tool calls a round and the rounds of one
// chunk's conversation; the line the kernel reads and what it does with what is not named
const theoryPlanContentFmt = "[FILES by directory]\n%[1]s" +
	"[CHUNKS] each chunk is one theory conversation: up to %[2]d tool calls a round, up to %[3]d rounds.\n" +
	"the kernel reads lines 'chunk: <exact paths from the list>', one chunk per line; files no line names are spread over the chunks, and output without such lines is cut into chunks of 12 files by the kernel.\n"

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

	to := theoryTo()
	prompt := Bagu(fmt.Sprintf(theoryPlanWhereFmt, owner, repo), fmt.Sprintf(theoryPlanWhatFmt, len(files), len(dirOrder)),
		fmt.Sprintf(theoryPlanContentFmt, fileList.String(), TurnToolBudget, RoundBudget("theorize_chunk_1"))+DoneInstruction(),
		fmt.Sprint(len(files)), to)

	child, err := ForkWithRole(prompt, ".", "theory_planner")
	if err != nil {
		log.Printf("theory_plan: %s/%s planner fork failed: %v, using static split", owner, repo, err)
		return theory_static_split(files)
	}
	var plannerConvID string
	if m := getMeta(child); m != nil && m.ConvID != "" {
		plannerConvID = m.ConvID
		RegisterConv(parent_pid, m.ConvID, owner+"/"+repo, "theory_planner", 0)
	}
	Wait(child)
	if plannerConvID != "" {
		ConvDone(plannerConvID, "done")
	}
	output := GetResult(child)
	log.Printf("theory_plan: %s/%s planner returned %d bytes", owner, repo, len(output))

	chunks, unclaimed := parsePlanLines(output, files)
	if len(unclaimed) > 0 && len(chunks) > 0 {
		chunks = redistributeUnclaimed(chunks, unclaimed)
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

// parsePlanLines reads "chunk: a, b, c" lines. Only paths in files count, each file
// once (first claim wins); a line that matches nothing is dropped. unclaimed lists
// the files nobody placed, in the order of files (not map order: the old code
// iterated a map, so which chunk an unclaimed file landed in changed run to run).
func parsePlanLines(output string, files []string) (chunks [][]string, unclaimed []string) {
	claimed := make(map[string]bool, len(files))
	known := make(map[string]bool, len(files))
	for _, f := range files {
		known[f] = true
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(line), "chunk:") {
			continue
		}
		rest := strings.TrimSpace(line[6:])
		var chunk []string
		for _, f := range strings.Split(rest, ",") {
			f = strings.TrimSpace(f)
			if f != "" && known[f] && !claimed[f] {
				claimed[f] = true
				chunk = append(chunk, f)
			}
		}
		if len(chunk) > 0 {
			chunks = append(chunks, chunk)
		}
	}
	for _, f := range files {
		if !claimed[f] {
			unclaimed = append(unclaimed, f)
		}
	}
	return chunks, unclaimed
}

// The sizes the planner is told to aim for (theory_plan's rules: ~50 tool calls per
// conversation, ~4 per file, so 8-12 files; "1-3 files wastes a conversation").
// A planner that cuts finer than that is not obeyed by the model alone, so the
// kernel enforces the same numbers: coalesceChunks.
const (
	chunkSmall   = 6  // a chunk below this is merged into its neighbour
	chunkTarget  = 12 // merging stops once a chunk would pass this
	chunkWaste   = 4  // a LAST chunk below this goes into the previous one
	chunkHardMax = 15 // and never past this
)

// coalesceChunks merges neighbouring chunks that are too small. It keeps every file
// exactly once and keeps the planner's order (it puts related files next to each
// other, so adjacent chunks are the natural ones to join). A plan whose chunks are
// already the right size is returned unchanged.
func coalesceChunks(chunks [][]string) [][]string {
	if len(chunks) < 2 {
		return chunks
	}
	var out [][]string
	var cur []string
	for _, c := range chunks {
		switch {
		case cur == nil:
			cur = append([]string(nil), c...)
		case len(cur) < chunkSmall && len(cur)+len(c) <= chunkTarget:
			cur = append(cur, c...)
		default:
			out = append(out, cur)
			cur = append([]string(nil), c...)
		}
	}
	if cur != nil {
		out = append(out, cur)
	}
	if n := len(out); n >= 2 && len(out[n-1]) < chunkWaste && len(out[n-2])+len(out[n-1]) <= chunkHardMax {
		out[n-2] = append(out[n-2], out[n-1]...)
		out = out[:n-1]
	}
	return out
}

func chunkSizes(chunks [][]string) []int {
	out := make([]int, len(chunks))
	for i, c := range chunks {
		out[i] = len(c)
	}
	return out
}

// redistributeUnclaimed puts files the planner did not place into the last chunk
// when it stays small, else into new chunks of 12.
func redistributeUnclaimed(chunks [][]string, unclaimed []string) [][]string {
	if len(chunks) > 0 && len(chunks[len(chunks)-1])+len(unclaimed) <= 15 {
		chunks[len(chunks)-1] = append(chunks[len(chunks)-1], unclaimed...)
		return chunks
	}
	for i := 0; i < len(unclaimed); i += 12 {
		end := i + 12
		if end > len(unclaimed) {
			end = len(unclaimed)
		}
		chunks = append(chunks, unclaimed[i:end])
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

// inserted into the shell when a chunk's conversation is resumed: %[1]d/%[2]d the chunk, its files
const chunkResumeWhereFmt = "第 %[1]d/%[2]d 个 chunk 的文件"
const chunkResumeWhat = "这个对话被打断了"
const chunkResumeContentFmt = "[YOUR FILES]\n%[1]s\n[OUTPUT] the kernel takes a .lean module for each of these files; a module for any other file is not part of this chunk.\n"

// inserted when a chunk ended with files no module covers: %[1]d/%[2]d covered, %[3]d missing
const chunkCoverWhere = "这个 chunk 的文件"
const chunkCoverWhatFmt = "内核测到 %[1]d/%[2]d 个文件有 module 覆盖，还缺 %[3]d 个"
const chunkCoverContentFmt = "[MISSING]\n%[1]s\n[COVERAGE] the kernel reads each module's SOURCES line: a file is covered when a module names it there.\n"

// what a chunk conversation covers: %[1]d/%[2]d the chunk, %[3]d files, %[4]d depth
const theorizeChunkWhatFmt = "这是第 %[1]d/%[2]d 个 chunk，%[3]d 个文件（depth %[4]d），其余文件由别的对话负责"

// %[1]d files, %[2]d depth
const theorizeLeafWhatFmt = "这是 %[1]d 个文件（depth %[2]d）"

func ref_split_fork(parent_pid int64, owner, repo, sha string, files []string, task string, depth int, chunk_idx int, chunk_total int, allChunks [][]string, prior []priorChunk) int64 {
	var src strings.Builder
	var tally sourceTally // what the prompt embeds, for its budget block
	// earlier chunks of THIS plan, by address: the kernel hands their files over
	src.WriteString(PriorChunksContext(prior))

	for _, f := range files {
		if isBinaryFile(f) {
			continue
		}
		content, err := RefFileAt(owner, repo, sha, f)
		if err != nil {
			continue
		}
		if len(content) > 300000 {
			content = content[:300000]
		}
		lines := strings.Count(content, "\n") + 1
		src.WriteString(fmt.Sprintf("=== %s (%d lines) ===\n", f, lines))
		src.WriteString(content)
		src.WriteString("\n\n")
		tally.Add(f, lines)
	}

	src_text := src.String()

	role := fmt.Sprintf("theorize_chunk_%d", chunk_idx+1)
	prompt := theorizePrompt(owner, repo, sha, fmt.Sprintf(theorizeChunkWhatFmt, chunk_idx+1, chunk_total, len(files), depth), task, src_text, len(files),
		BudgetBlock(tally.Theory(), RoundBudget(role)))
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

	var src strings.Builder
	var tally sourceTally // what the prompt embeds, for its budget block

	for _, f := range files {
		if isBinaryFile(f) {
			continue
		}
		content, err := RefFileAt(owner, repo, sha, f)
		if err != nil {
			continue
		}
		if len(content) > 300000 {
			content = content[:300000]
		}
		lines := strings.Count(content, "\n") + 1
		src.WriteString(fmt.Sprintf("=== %s (%d lines) ===\n", f, lines))
		src.WriteString(content)
		src.WriteString("\n\n")
		tally.Add(f, lines)
	}

	src_text := src.String()

	prompt := theorizePrompt(owner, repo, sha, fmt.Sprintf(theorizeLeafWhatFmt, len(files), depth), task, src_text, len(files),
		BudgetBlock(tally.Theory(), RoundBudget("theorize_leaf")))

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

// chunk_fill_missing asks the SAME conversation to cover the files still missing,
// at most twice, and only when the conversation has settled. Sending into a
// conversation whose last turn is not yet persisted is what produced chunk 1's
// five junk "Continue"s; if it is not settled we do not send.
func chunk_fill_missing(convID string, chunk []string, result, tag string) (string, Coverage) {
	cov := ChunkCoverage(chunk, result)
	for round := 1; round <= 2 && len(cov.Missing) > 0 && cov.CoveredCount() > 0; round++ {
		settled, tail, err := ConvSettled(convID)
		if err != nil || !settled {
			log.Printf("ref_split: %s has %d/%d files covered, %d missing — NOT sending Continue (settled=%v err=%v last=%s)",
				tag, cov.CoveredCount(), cov.Total, len(cov.Missing), settled, err, tail.LastSender)
			break
		}
		log.Printf("ref_split: %s has %d/%d files covered, %d missing — ContinueConv round %d",
			tag, cov.CoveredCount(), cov.Total, len(cov.Missing), round)
		prompt := Bagu(chunkCoverWhere, fmt.Sprintf(chunkCoverWhatFmt, cov.CoveredCount(), cov.Total, len(cov.Missing)),
			fmt.Sprintf(chunkCoverContentFmt, strings.Join(cov.Missing, "\n")), fmt.Sprint(len(cov.Missing)), theoryTo())
		contResult, _ := ContinueConv(convID, prompt)
		if full, err := DownloadConv(convID); err == nil && len(full) > len(result) {
			log.Printf("ref_split: %s ContinueConv + re-download: %d bytes (was %d)", tag, len(full), len(result))
			result = full
		} else if len(contResult) > 100 {
			result = result + "\n\n" + contResult
		}
		next := ChunkCoverage(chunk, result)
		progressed := next.CoveredCount() > cov.CoveredCount()
		cov = next
		if !progressed {
			break
		}
	}
	return result, cov
}
