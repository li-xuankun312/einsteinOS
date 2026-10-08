package vikingdb

import (
	"fmt"
	"strings"
	"time"
)

const (
	EntityDeveloper  = "developer"
	EntityRepo       = "repo"
	EntityFeature    = "feature"
	EntityFile       = "file"
	EntityIssue      = "issue"
	EntityPR         = "pull_request"
	EntityBranch     = "branch"

	EventCommit      = "commit"
	EventPROpen      = "pr_open"
	EventPRReview    = "pr_review"
	EventPRMerge     = "pr_merge"
	EventPRReject    = "pr_reject"
	EventIssueOpen   = "issue_open"
	EventIssueClose  = "issue_close"
	EventIssueComment = "issue_comment"
	EventCIPass      = "ci_pass"
	EventCIFail      = "ci_fail"
	EventRelease     = "release"

	CapMerge         = "merge"
	CapReview        = "review"
	CapPush          = "push"
	CapCI            = "ci"
	CapRelease       = "release"
)

type Developer struct {
	Login    string
	Email    string
	Caps     []string
	IsRoot   bool
}

type ProjectDB struct {
	engine      *Engine
	projects    *Collection
	rootUsers   map[string]bool
	developers  map[string]*Developer
}

func NewProjectDB(engine *Engine) *ProjectDB {
	return &ProjectDB{
		engine:     engine,
		projects:   engine.CreateCollection("projects"),
		rootUsers:  make(map[string]bool),
		developers: make(map[string]*Developer),
	}
}

func (pdb *ProjectDB) AddRoot(login, email string) {
	pdb.rootUsers[login] = true
	dev := &Developer{
		Login:  login,
		Email:  email,
		Caps:   []string{CapMerge, CapReview, CapPush, CapCI, CapRelease},
		IsRoot: true,
	}
	pdb.developers[login] = dev
	pdb.projects.InsertEntity(&Entity{
		ID:         "dev:" + login,
		Name:       login,
		Type:       EntityDeveloper,
		Properties: map[string]interface{}{
			"email":   email,
			"is_root": true,
			"caps":    strings.Join(dev.Caps, ","),
		},
		FirstSeen: time.Now(),
		LastSeen:  time.Now(),
	})
}

func (pdb *ProjectDB) AddDeveloper(login, email string, caps []string) {
	dev := &Developer{
		Login:  login,
		Email:  email,
		Caps:   caps,
		IsRoot: false,
	}
	pdb.developers[login] = dev
	pdb.projects.InsertEntity(&Entity{
		ID:         "dev:" + login,
		Name:       login,
		Type:       EntityDeveloper,
		Properties: map[string]interface{}{
			"email":   email,
			"is_root": false,
			"caps":    strings.Join(caps, ","),
		},
		FirstSeen: time.Now(),
		LastSeen:  time.Now(),
	})
}

func (pdb *ProjectDB) CheckCap(login, cap string) bool {
	if pdb.rootUsers[login] {
		return true
	}
	dev := pdb.developers[login]
	if dev == nil {
		return false
	}
	for _, c := range dev.Caps {
		if c == cap {
			return true
		}
	}
	return false
}

func (pdb *ProjectDB) RegisterRepo(owner, name string) {
	id := fmt.Sprintf("repo:%s/%s", owner, name)
	pdb.projects.InsertEntity(&Entity{
		ID:   id,
		Name: fmt.Sprintf("%s/%s", owner, name),
		Type: EntityRepo,
		Properties: map[string]interface{}{
			"owner": owner,
			"name":  name,
		},
		FirstSeen: time.Now(),
		LastSeen:  time.Now(),
	})
}

func (pdb *ProjectDB) RecordCommit(repo, sha, author, message string, files []string, ts time.Time) {
	eventID := fmt.Sprintf("commit:%s:%s", repo, sha[:8])
	summary := fmt.Sprintf("[%s] %s: %s", repo, author, message)

	embedding, _ := pdb.engine.Embed(summary)

	pdb.projects.InsertEvent(&Event{
		ID:        eventID,
		Timestamp: ts,
		ConvID:    repo,
		Goal:      EventCommit,
		Summary:   summary,
		Tool:      "git",
		Command:   fmt.Sprintf("git commit %s", sha[:8]),
		Files:     files,
		Tags:      []string{EventCommit, author, repo},
		Embedding: embedding,
	})

	pdb.projects.InsertEntity(&Entity{
		ID:           "dev:" + author,
		Name:         author,
		Type:         EntityDeveloper,
		EventHistory: []string{eventID},
		LastSeen:     ts,
	})

	for _, f := range files {
		fileID := fmt.Sprintf("file:%s:%s", repo, f)
		pdb.projects.InsertEntity(&Entity{
			ID:           fileID,
			Name:         f,
			Type:         EntityFile,
			Properties:   map[string]interface{}{"repo": repo},
			EventHistory: []string{eventID},
			LastSeen:     ts,
		})
	}
}

func (pdb *ProjectDB) RecordPROpen(repo string, number int, author, title, body string, ts time.Time) string {
	eventID := fmt.Sprintf("pr_open:%s:#%d", repo, number)
	summary := fmt.Sprintf("[PR #%d] %s: %s", number, author, title)
	embedding, _ := pdb.engine.Embed(summary + " " + body)

	pdb.projects.InsertEvent(&Event{
		ID:        eventID,
		Timestamp: ts,
		ConvID:    repo,
		Goal:      EventPROpen,
		Summary:   summary,
		Output:    body,
		Tags:      []string{EventPROpen, author, repo, fmt.Sprintf("pr:%d", number)},
		Embedding: embedding,
	})

	prID := fmt.Sprintf("pr:%s:#%d", repo, number)
	pdb.projects.InsertEntity(&Entity{
		ID:   prID,
		Name: fmt.Sprintf("#%d %s", number, title),
		Type: EntityPR,
		Properties: map[string]interface{}{
			"repo":   repo,
			"number": number,
			"author": author,
			"title":  title,
			"status": "open",
		},
		EventHistory: []string{eventID},
		FirstSeen:    ts,
		LastSeen:     ts,
	})

	return prID
}

func (pdb *ProjectDB) RecordPRReview(repo string, number int, reviewer, verdict, comment string, ts time.Time) error {
	if !pdb.CheckCap(reviewer, CapReview) {
		return fmt.Errorf("permission denied: %s lacks review capability", reviewer)
	}

	eventID := fmt.Sprintf("pr_review:%s:#%d:%s", repo, number, reviewer)
	summary := fmt.Sprintf("[Review] %s %s PR #%d: %s", reviewer, verdict, number, comment)
	embedding, _ := pdb.engine.Embed(summary)

	pdb.projects.InsertEvent(&Event{
		ID:        eventID,
		Timestamp: ts,
		ConvID:    repo,
		Goal:      EventPRReview,
		Summary:   summary,
		Output:    comment,
		Tags:      []string{EventPRReview, reviewer, verdict, fmt.Sprintf("pr:%d", number)},
		Embedding: embedding,
	})

	prID := fmt.Sprintf("pr:%s:#%d", repo, number)
	pdb.projects.InsertEntity(&Entity{
		ID:           prID,
		Name:         fmt.Sprintf("#%d", number),
		Type:         EntityPR,
		Properties:   map[string]interface{}{"last_review": verdict, "reviewer": reviewer},
		EventHistory: []string{eventID},
		LastSeen:     ts,
	})

	return nil
}

func (pdb *ProjectDB) RecordPRMerge(repo string, number int, merger string, ts time.Time) error {
	if !pdb.CheckCap(merger, CapMerge) {
		return fmt.Errorf("permission denied: %s lacks merge capability (need root)", merger)
	}

	eventID := fmt.Sprintf("pr_merge:%s:#%d", repo, number)
	summary := fmt.Sprintf("[Merge] %s merged PR #%d in %s", merger, number, repo)
	embedding, _ := pdb.engine.Embed(summary)

	pdb.projects.InsertEvent(&Event{
		ID:        eventID,
		Timestamp: ts,
		ConvID:    repo,
		Goal:      EventPRMerge,
		Summary:   summary,
		Tags:      []string{EventPRMerge, merger, fmt.Sprintf("pr:%d", number)},
		Embedding: embedding,
	})

	prID := fmt.Sprintf("pr:%s:#%d", repo, number)
	pdb.projects.InsertEntity(&Entity{
		ID:         prID,
		Name:       fmt.Sprintf("#%d", number),
		Type:       EntityPR,
		Properties: map[string]interface{}{"status": "merged", "merged_by": merger},
		EventHistory: []string{eventID},
		LastSeen:   ts,
	})

	return nil
}

func (pdb *ProjectDB) RecordCI(repo string, number int, passed bool, log string, ts time.Time) {
	status := EventCIPass
	if !passed {
		status = EventCIFail
	}
	eventID := fmt.Sprintf("ci:%s:#%d:%s", repo, number, ts.Format("150405"))
	summary := fmt.Sprintf("[CI %s] PR #%d in %s", status, number, repo)

	pdb.projects.InsertEvent(&Event{
		ID:        eventID,
		Timestamp: ts,
		ConvID:    repo,
		Goal:      status,
		Summary:   summary,
		Output:    log,
		ExitCode:  boolToExit(passed),
		Tags:      []string{status, fmt.Sprintf("pr:%d", number)},
	})

	prID := fmt.Sprintf("pr:%s:#%d", repo, number)
	pdb.projects.InsertEntity(&Entity{
		ID:         prID,
		Type:       EntityPR,
		Properties: map[string]interface{}{"ci": status},
		EventHistory: []string{eventID},
		LastSeen:   ts,
	})
}

func (pdb *ProjectDB) RecordIssue(repo string, number int, author, title, body string, labels []string, ts time.Time) {
	eventID := fmt.Sprintf("issue_open:%s:#%d", repo, number)
	summary := fmt.Sprintf("[Issue #%d] %s: %s", number, author, title)
	embedding, _ := pdb.engine.Embed(summary + " " + body)

	pdb.projects.InsertEvent(&Event{
		ID:        eventID,
		Timestamp: ts,
		ConvID:    repo,
		Goal:      EventIssueOpen,
		Summary:   summary,
		Output:    body,
		Tags:      append([]string{EventIssueOpen, author}, labels...),
		Embedding: embedding,
	})

	issueID := fmt.Sprintf("issue:%s:#%d", repo, number)
	pdb.projects.InsertEntity(&Entity{
		ID:   issueID,
		Name: fmt.Sprintf("#%d %s", number, title),
		Type: EntityIssue,
		Properties: map[string]interface{}{
			"repo":   repo,
			"number": number,
			"author": author,
			"title":  title,
			"status": "open",
			"labels": strings.Join(labels, ","),
		},
		EventHistory: []string{eventID},
		FirstSeen:    ts,
		LastSeen:     ts,
		Embedding:    embedding,
	})
}

func (pdb *ProjectDB) SearchRelated(query string, topK int) []SearchResult {
	embedding, err := pdb.engine.Embed(query)
	if err != nil {
		return nil
	}
	events := pdb.projects.SearchEvents(embedding, topK)
	entities := pdb.projects.SearchEntities(embedding, topK)

	merged := make([]SearchResult, 0, len(events)+len(entities))
	merged = append(merged, events...)
	merged = append(merged, entities...)
	return merged
}

func (pdb *ProjectDB) PendingPRs(repo string) []*Entity {
	prs := pdb.projects.EntitiesByType(EntityPR)
	var pending []*Entity
	for _, pr := range prs {
		r, _ := pr.Properties["repo"].(string)
		s, _ := pr.Properties["status"].(string)
		if r == repo && s == "open" {
			pending = append(pending, pr)
		}
	}
	return pending
}

func (pdb *ProjectDB) DeveloperActivity(login string) []*Event {
	devEntity := pdb.projects.GetEntity("dev:" + login)
	if devEntity == nil {
		return nil
	}
	var events []*Event
	for _, eid := range devEntity.EventHistory {
		if e := pdb.projects.GetEvent(eid); e != nil {
			events = append(events, e)
		}
	}
	return events
}

func (pdb *ProjectDB) FileHistory(repo, path string) []*Event {
	fileID := fmt.Sprintf("file:%s:%s", repo, path)
	return pdb.projects.EventsByFile(fileID)
}

func (pdb *ProjectDB) ProjectSummary(repo string) string {
	events := pdb.projects.EventsByConv(repo)
	prs := pdb.projects.EntitiesByType(EntityPR)
	issues := pdb.projects.EntitiesByType(EntityIssue)

	var openPR, mergedPR, openIssue, closedIssue int
	for _, pr := range prs {
		r, _ := pr.Properties["repo"].(string)
		if r != repo {
			continue
		}
		s, _ := pr.Properties["status"].(string)
		if s == "open" {
			openPR++
		} else {
			mergedPR++
		}
	}
	for _, issue := range issues {
		r, _ := issue.Properties["repo"].(string)
		if r != repo {
			continue
		}
		s, _ := issue.Properties["status"].(string)
		if s == "open" {
			openIssue++
		} else {
			closedIssue++
		}
	}

	return fmt.Sprintf("[%s] %d events | PR: %d open, %d merged | Issues: %d open, %d closed | Developers: %d root, %d total",
		repo, len(events), openPR, mergedPR, openIssue, closedIssue,
		len(pdb.rootUsers), len(pdb.developers))
}

func boolToExit(ok bool) int {
	if ok {
		return 0
	}
	return 1
}
