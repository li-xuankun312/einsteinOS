package vikingdb

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type MemoryEntry struct {
	ID        string
	Content   string
	Author    string
	Timestamp time.Time
	Metadata  map[string]interface{}
	Score     float32
}

type MemorySearchRequest struct {
	Query   string
	TopK    int
	UserID  string
	AppName string
}

type MemorySearchResponse struct {
	Entries []MemoryEntry
	Query   string
	Elapsed time.Duration
}

type MemoryService struct {
	engine *Engine
	pdb    *ProjectDB
}

func NewMemoryService(engine *Engine, pdb *ProjectDB) *MemoryService {
	return &MemoryService{engine: engine, pdb: pdb}
}

func (ms *MemoryService) SearchMemory(ctx context.Context, req *MemorySearchRequest) (*MemorySearchResponse, error) {
	start := time.Now()
	topK := req.TopK
	if topK <= 0 {
		topK = 10
	}

	results := ms.pdb.SearchRelated(req.Query, topK)

	var entries []MemoryEntry
	for _, r := range results {
		entry := MemoryEntry{
			ID:       r.ID,
			Score:    r.Score,
			Metadata: r.Data,
		}

		if e := ms.findEvent(r.ID); e != nil {
			entry.Content = e.Summary
			entry.Timestamp = e.Timestamp
			entry.Author = extractAuthor(e.Summary)
		} else if ent := ms.findEntity(r.ID); ent != nil {
			entry.Content = fmt.Sprintf("[%s] %s", ent.Type, ent.Name)
			entry.Timestamp = ent.LastSeen
		} else {
			entry.Content = r.ID
		}

		entries = append(entries, entry)
	}

	return &MemorySearchResponse{
		Entries: entries,
		Query:   req.Query,
		Elapsed: time.Since(start),
	}, nil
}

func (ms *MemoryService) AddEvent(ctx context.Context, event *Event) error {
	if ms.pdb == nil || ms.pdb.projects == nil {
		return fmt.Errorf("project db not initialized")
	}

	if len(event.Embedding) == 0 && event.Summary != "" {
		embedding, _ := ms.engine.Embed(event.Summary)
		event.Embedding = embedding
	}

	ms.pdb.projects.InsertEvent(event)
	return nil
}

func (ms *MemoryService) findEvent(id string) *Event {
	if ms.pdb == nil || ms.pdb.projects == nil {
		return nil
	}
	return ms.pdb.projects.GetEvent(id)
}

func (ms *MemoryService) findEntity(id string) *Entity {
	if ms.pdb == nil || ms.pdb.projects == nil {
		return nil
	}
	return ms.pdb.projects.GetEntity(id)
}

func (ms *MemoryService) ProjectSummary(repo string) string {
	if ms.pdb == nil {
		return ""
	}
	return ms.pdb.ProjectSummary(repo)
}

func (ms *MemoryService) FormatForLLM(resp *MemorySearchResponse) string {
	if resp == nil || len(resp.Entries) == 0 {
		return "No relevant memories found."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d relevant memories for \"%s\":\n\n", len(resp.Entries), resp.Query))

	for i, entry := range resp.Entries {
		sb.WriteString(fmt.Sprintf("%d. [score=%.3f] %s\n", i+1, entry.Score, entry.Content))
		if !entry.Timestamp.IsZero() {
			sb.WriteString(fmt.Sprintf("   time: %s\n", entry.Timestamp.Format("2006-01-02 15:04")))
		}
		if entry.Metadata != nil {
			if tool, ok := entry.Metadata["tool"]; ok {
				sb.WriteString(fmt.Sprintf("   tool: %v\n", tool))
			}
			if goal, ok := entry.Metadata["goal"]; ok {
				sb.WriteString(fmt.Sprintf("   goal: %v\n", goal))
			}
		}
	}

	return sb.String()
}

func extractAuthor(summary string) string {
	if idx := strings.Index(summary, "] "); idx >= 0 {
		rest := summary[idx+2:]
		if colon := strings.Index(rest, ":"); colon >= 0 {
			return strings.TrimSpace(rest[:colon])
		}
	}
	return ""
}

type ToolDeclaration struct {
	Name        string
	Description string
	Parameters  map[string]interface{}
}

func VikingDBToolDeclaration() ToolDeclaration {
	return ToolDeclaration{
		Name:        "search_project_memory",
		Description: "Search project memory (commits, PRs, issues, files) for relevant context. Use this when you need to understand project history, find related code changes, or look up what was done before.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Search query describing what you're looking for in project history",
				},
			},
			"required": []string{"query"},
		},
	}
}

func VikingDBProjectToolDeclaration() ToolDeclaration {
	return ToolDeclaration{
		Name:        "project_status",
		Description: "Get current project status: open PRs, recent activity, developer stats. Use this to understand current project state.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"repo": map[string]interface{}{
					"type":        "string",
					"description": "Repository in owner/name format",
				},
			},
			"required": []string{"repo"},
		},
	}
}

func (ms *MemoryService) HandleToolCall(toolName string, args map[string]interface{}) (string, error) {
	switch toolName {
	case "search_project_memory":
		query, _ := args["query"].(string)
		if query == "" {
			return "", fmt.Errorf("missing query parameter")
		}
		resp, err := ms.SearchMemory(context.Background(), &MemorySearchRequest{
			Query: query,
			TopK:  10,
		})
		if err != nil {
			return "", err
		}
		return ms.FormatForLLM(resp), nil

	case "project_status":
		repo, _ := args["repo"].(string)
		if repo == "" {
			return "", fmt.Errorf("missing repo parameter")
		}
		return ms.ProjectSummary(repo), nil

	default:
		return "", fmt.Errorf("unknown tool: %s", toolName)
	}
}
