package vikingdb

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type GitHubSync struct {
	token      string
	baseURL    string
	pdb        *ProjectDB
	httpClient *http.Client
	lastSync   map[string]time.Time
}

func NewGitHubSync(token string, pdb *ProjectDB) *GitHubSync {
	client := buildHTTPClient()
	return &GitHubSync{
		token:      token,
		baseURL:    "https://api.github.com",
		pdb:        pdb,
		httpClient: client,
		lastSync:   make(map[string]time.Time),
	}
}

func buildHTTPClient() *http.Client {
	proxyEnv := os.Getenv("ALL_PROXY")
	if proxyEnv == "" {
		proxyEnv = os.Getenv("https_proxy")
	}
	if proxyEnv == "" {
		proxyEnv = os.Getenv("HTTPS_PROXY")
	}

	transport := &http.Transport{
		TLSHandshakeTimeout:   60 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		DisableKeepAlives:     false,
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       120 * time.Second,
		ForceAttemptHTTP2:     false,
	}

	if proxyEnv != "" {
		parsed, err := url.Parse(proxyEnv)
		if err == nil {
			transport.Proxy = http.ProxyURL(parsed)
			log.Printf("[vikingdb] using proxy: %s", parsed.String())
		}
	}

	return &http.Client{
		Timeout:   120 * time.Second,
		Transport: transport,
	}
}

func (gs *GitHubSync) SyncRepo(owner, repo string) error {
	fullName := owner + "/" + repo
	log.Printf("[vikingdb] syncing %s", fullName)

	gs.pdb.RegisterRepo(owner, repo)

	commitCount, err := gs.syncCommits(owner, repo, 100)
	if err != nil {
		log.Printf("[vikingdb] commits sync error: %v", err)
	}

	prCount, err := gs.syncPRs(owner, repo, 50)
	if err != nil {
		log.Printf("[vikingdb] PRs sync error: %v", err)
	}

	issueCount, err := gs.syncIssues(owner, repo, 50)
	if err != nil {
		log.Printf("[vikingdb] issues sync error: %v", err)
	}

	gs.lastSync[fullName] = time.Now()
	log.Printf("[vikingdb] synced %s: %d commits, %d PRs, %d issues",
		fullName, commitCount, prCount, issueCount)
	return nil
}

func (gs *GitHubSync) syncCommits(owner, repo string, maxPages int) (int, error) {
	fullName := owner + "/" + repo
	count := 0
	since := ""
	if t, ok := gs.lastSync[fullName]; ok {
		since = "&since=" + t.Format(time.RFC3339)
	}

	for page := 1; page <= maxPages; page++ {
		url := fmt.Sprintf("%s/repos/%s/%s/commits?per_page=100&page=%d%s",
			gs.baseURL, owner, repo, page, since)

		var commits []ghCommit
		if err := gs.get(url, &commits); err != nil {
			return count, err
		}
		if len(commits) == 0 {
			break
		}

		for _, c := range commits {
			ts := c.Commit.Author.Date
			if ts.IsZero() {
				ts = time.Now()
			}

			var files []string
			for _, f := range c.Files {
				files = append(files, f.Filename)
			}

			author := c.Commit.Author.Name
			if c.Author.Login != "" {
				author = c.Author.Login
			}

			gs.pdb.RecordCommit(fullName, c.SHA, author, c.Commit.Message, files, ts)
			count++
		}

		if len(commits) < 100 {
			break
		}
	}
	return count, nil
}

func (gs *GitHubSync) syncPRs(owner, repo string, maxPages int) (int, error) {
	fullName := owner + "/" + repo
	count := 0

	for page := 1; page <= maxPages; page++ {
		url := fmt.Sprintf("%s/repos/%s/%s/pulls?state=all&per_page=100&page=%d&sort=updated&direction=desc",
			gs.baseURL, owner, repo, page)

		var prs []ghPullRequest
		if err := gs.get(url, &prs); err != nil {
			return count, err
		}
		if len(prs) == 0 {
			break
		}

		for _, pr := range prs {
			author := pr.User.Login
			ts := pr.CreatedAt
			if ts.IsZero() {
				ts = time.Now()
			}

			prID := gs.pdb.RecordPROpen(fullName, pr.Number, author, pr.Title, pr.Body, ts)

			if pr.MergedAt != nil && !pr.MergedAt.IsZero() {
				merger := author
				if pr.MergedBy.Login != "" {
					merger = pr.MergedBy.Login
				}
				gs.pdb.RecordPRMerge(fullName, pr.Number, merger, *pr.MergedAt)
			}

			_ = prID
			count++
		}

		if len(prs) < 100 {
			break
		}
	}
	return count, nil
}

func (gs *GitHubSync) syncIssues(owner, repo string, maxPages int) (int, error) {
	fullName := owner + "/" + repo
	count := 0

	for page := 1; page <= maxPages; page++ {
		url := fmt.Sprintf("%s/repos/%s/%s/issues?state=all&per_page=100&page=%d&sort=updated&direction=desc",
			gs.baseURL, owner, repo, page)

		var issues []ghIssue
		if err := gs.get(url, &issues); err != nil {
			return count, err
		}
		if len(issues) == 0 {
			break
		}

		for _, issue := range issues {
			if issue.PullRequest != nil {
				continue
			}
			author := issue.User.Login
			ts := issue.CreatedAt
			if ts.IsZero() {
				ts = time.Now()
			}

			var labels []string
			for _, l := range issue.Labels {
				labels = append(labels, l.Name)
			}

			gs.pdb.RecordIssue(fullName, issue.Number, author, issue.Title, issue.Body, labels, ts)
			count++
		}

		if len(issues) < 100 {
			break
		}
	}
	return count, nil
}

func (gs *GitHubSync) SyncPRReviews(owner, repo string, prNumber int) error {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews?per_page=100",
		gs.baseURL, owner, repo, prNumber)

	var reviews []ghReview
	if err := gs.get(url, &reviews); err != nil {
		return err
	}

	fullName := owner + "/" + repo
	for _, r := range reviews {
		verdict := strings.ToLower(r.State)
		ts := r.SubmittedAt
		if ts.IsZero() {
			ts = time.Now()
		}
		gs.pdb.RecordPRReview(fullName, prNumber, r.User.Login, verdict, r.Body, ts)
	}
	return nil
}

func (gs *GitHubSync) get(url string, result interface{}) error {
	maxRetries := 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt*attempt) * 2 * time.Second
			log.Printf("[vikingdb] retry %d/%d after %v: %s", attempt+1, maxRetries, backoff, truncURL(url))
			time.Sleep(backoff)
		}

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/vnd.github.v3+json")
		req.Header.Set("Connection", "keep-alive")
		if gs.token != "" {
			req.Header.Set("Authorization", "token "+gs.token)
		}

		resp, err := gs.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("github api: %w", err)
			continue
		}

		if resp.StatusCode == 403 {
			resp.Body.Close()
			return fmt.Errorf("github api rate limited (403)")
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("github api %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("github api %d: %s", resp.StatusCode, truncBody(body))
		}

		err = json.NewDecoder(resp.Body).Decode(result)
		resp.Body.Close()
		return err
	}

	return lastErr
}

func truncURL(u string) string {
	if len(u) > 80 {
		return u[:80] + "..."
	}
	return u
}

func truncBody(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "..."
	}
	return string(b)
}

type ghCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name  string    `json:"name"`
			Email string    `json:"email"`
			Date  time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Files []struct {
		Filename string `json:"filename"`
	} `json:"files"`
}

type ghPullRequest struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	MergedAt  *time.Time `json:"merged_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	MergedBy struct {
		Login string `json:"login"`
	} `json:"merged_by"`
}

type ghIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"created_at"`
	User        struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest *struct{} `json:"pull_request"`
}

type ghReview struct {
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	State       string    `json:"state"`
	Body        string    `json:"body"`
	SubmittedAt time.Time `json:"submitted_at"`
}
