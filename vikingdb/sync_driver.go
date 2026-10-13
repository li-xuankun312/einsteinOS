package vikingdb

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

type SyncDriver struct {
	Engine    *Engine
	ProjectDB **ProjectDB
	Token     string
	Roots     map[string]string
}

type sync_input struct {
	Repo  string `json:"repo"`
	Token string `json:"token"`
}

func (d *SyncDriver) Match(name string) bool {
	return strings.ToLower(name) == "sync_repo"
}

func (d *SyncDriver) Execute(name string, inputJSON string, workDir string) string {
	var input sync_input
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return fmt.Sprintf("parse error: %v", err)
	}

	parts := strings.SplitN(input.Repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "usage: sync_repo {repo: 'owner/name'}"
	}

	token := input.Token
	if token == "" {
		token = d.Token
	}

	if *d.ProjectDB == nil {
		*d.ProjectDB = NewProjectDB(d.Engine)
		for user, email := range d.Roots {
			(*d.ProjectDB).AddRoot(user, email)
		}
	}

	syncer := NewGitHubSync(token, *d.ProjectDB)
	err := syncer.SyncRepo(parts[0], parts[1])
	if err != nil {
		return fmt.Sprintf("sync %s failed: %v", input.Repo, err)
	}

	summary := (*d.ProjectDB).ProjectSummary(input.Repo)
	log.Printf("[vikingdb] sync_repo tool: %s done", input.Repo)
	return summary
}
