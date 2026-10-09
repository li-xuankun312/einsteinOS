package vikingdb

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
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
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = 15 * time.Second

	proxyURL := os.Getenv("ALL_PROXY")
	if proxyURL == "" {
		proxyURL = os.Getenv("https_proxy")
	}
	if proxyURL == "" {
		proxyURL = os.Getenv("HTTPS_PROXY")
	}

	if proxyURL != "" && strings.HasPrefix(proxyURL, "socks5") {
		parsed, err := url.Parse(proxyURL)
		if err == nil {
			host := parsed.Host
			transport.DialContext = nil
			transport.Dial = func(network, addr string) (net.Conn, error) {
				return socks5Dial(host, addr)
			}
			log.Printf("[vikingdb] using SOCKS5 proxy: %s", host)
		}
	} else if proxyURL != "" {
		parsed, _ := url.Parse(proxyURL)
		if parsed != nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}

	return &http.Client{
		Timeout:   60 * time.Second,
		Transport: transport,
	}
}

func socks5Dial(proxyAddr, targetAddr string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", proxyAddr, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("socks5 connect to proxy: %w", err)
	}

	conn.Write([]byte{0x05, 0x01, 0x00})
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks5 handshake: %w", err)
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		conn.Close()
		return nil, errors.New("socks5 auth failed")
	}

	host, portStr, err := net.SplitHostPort(targetAddr)
	if err != nil {
		conn.Close()
		return nil, err
	}
	port, _ := net.LookupPort("tcp", portStr)

	req := []byte{0x05, 0x01, 0x00, 0x03}
	req = append(req, byte(len(host)))
	req = append(req, []byte(host)...)
	portBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(portBuf, uint16(port))
	req = append(req, portBuf...)

	conn.Write(req)

	reply := make([]byte, 4)
	if _, err := io.ReadFull(conn, reply); err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks5 reply: %w", err)
	}
	if reply[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks5 error: code %d", reply[1])
	}

	switch reply[3] {
	case 0x01:
		io.ReadFull(conn, make([]byte, 4+2))
	case 0x03:
		lenBuf := make([]byte, 1)
		io.ReadFull(conn, lenBuf)
		io.ReadFull(conn, make([]byte, int(lenBuf[0])+2))
	case 0x04:
		io.ReadFull(conn, make([]byte, 16+2))
	}

	return conn, nil
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
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if gs.token != "" {
		req.Header.Set("Authorization", "token "+gs.token)
	}

	resp, err := gs.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("github api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 403 {
		return fmt.Errorf("github api rate limited (403)")
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github api %d: %s", resp.StatusCode, truncBody(body))
	}

	return json.NewDecoder(resp.Body).Decode(result)
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
