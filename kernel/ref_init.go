package kernel

import (
	"fmt"
	"log"
	"strings"
	"time"
)

type RefRepoConfig struct {
	Owner string
	Name  string
}

var refRepoConfigs []RefRepoConfig

func RefRegister(owner, name string) {
	refRepoConfigs = append(refRepoConfigs, RefRepoConfig{Owner: owner, Name: name})
}

type RefRecorder interface {
	RegisterRepo(owner, name string)
	RecordCommit(repo, sha, author, message string, files []string, ts time.Time)
}

func RefInit(recorder RefRecorder) {
	if len(refRepoConfigs) == 0 {
		log.Println("ref_init: no reference repos configured")
		return
	}

	for _, cfg := range refRepoConfigs {
		key := cfg.Owner + "/" + cfg.Name
		dir := fmt.Sprintf("/tmp/ref_%s_%s.git", cfg.Owner, cfg.Name)

		log.Printf("ref_init: cloning %s", key)
		repo, err := RefClone(cfg.Owner, cfg.Name, dir)
		if err != nil {
			log.Printf("ref_init: clone %s failed: %v", key, err)
			continue
		}

		log.Printf("ref_init: listing commits for %s", key)
		commits, err := RefListCommits(cfg.Owner, cfg.Name)
		if err != nil {
			log.Printf("ref_init: list %s failed: %v", key, err)
			continue
		}

		recorder.RegisterRepo(cfg.Owner, cfg.Name)

		for _, c := range commits {
			_, files, _ := RefReadCommit(cfg.Owner, cfg.Name, c.SHA)
			if files == nil {
				files = []string{}
			}

			msg := c.Message
			if len(msg) > 200 {
				msg = msg[:200]
			}

			recorder.RecordCommit(key, c.SHA, c.Author, msg, files, c.Date)
		}

		_ = repo

		log.Printf("ref_init: %s done — %d commits indexed", key, len(commits))
	}

	log.Printf("ref_init: %d reference repos initialized", len(refRepoConfigs))
}

func RefInitSummary() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("reference repos: %d configured\n", len(refRepoConfigs)))
	for _, cfg := range refRepoConfigs {
		key := cfg.Owner + "/" + cfg.Name
		refMu.Lock()
		r := refRepos[key]
		refMu.Unlock()
		if r == nil {
			sb.WriteString(fmt.Sprintf("  %s: not loaded\n", key))
		} else {
			sb.WriteString(fmt.Sprintf("  %s: %d commits, cloned %s\n",
				key, len(r.Commits), r.ClonedAt.Format("15:04:05")))
		}
	}
	return sb.String()
}
