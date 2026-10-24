package claudeweb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ClientConfig configures the claude web API client.
type ClientConfig struct {
	// BaseURL is the base URL of the web API, e.g. "https://chk.aimonkey.plus"
	BaseURL string
	// OrgID is the organization UUID.
	OrgID string
	// Cookie is the full cookie string for authentication.
	Cookie string
	// HTTPClient is optional; defaults to http.DefaultClient.
	HTTPClient *http.Client
}

// Client communicates with the claude.ai web API (or compatible proxy).
type Client struct {
	baseURL    string
	orgID      string
	cookie     string
	httpClient *http.Client
}

// NewClient creates a new web API client.
func NewClient(cfg ClientConfig) *Client {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{
			Transport: &http.Transport{
				TLSHandshakeTimeout:   8 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
				Proxy:                 http.ProxyFromEnvironment,
			},
		}
	}
	return &Client{
		baseURL:    cfg.BaseURL,
		orgID:      cfg.OrgID,
		cookie:     cfg.Cookie,
		httpClient: hc,
	}
}

const (
	apiWindowSize = 9
	apiWindowDur  = 10 * time.Minute
)

var (
	apiCallTimes [apiWindowSize]time.Time
	apiCallIdx   int
	apiCallMu    sync.Mutex
)

func apiThrottle() {
	apiCallMu.Lock()
	defer apiCallMu.Unlock()

	oldest := apiCallTimes[apiCallIdx%apiWindowSize]
	if !oldest.IsZero() {
		elapsed := time.Since(oldest)
		if elapsed < apiWindowDur {
			wait := apiWindowDur - elapsed
			log.Printf("api_throttle: %d calls in %v, sleeping %v",
				apiWindowSize, elapsed.Truncate(time.Second), wait.Truncate(time.Second))
			apiCallMu.Unlock()
			time.Sleep(wait)
			apiCallMu.Lock()
		}
	}
	apiCallTimes[apiCallIdx%apiWindowSize] = time.Now()
	apiCallIdx++
}

// Completion sends a completion request and returns the raw SSE response body.
// The caller is responsible for closing the body.
func (c *Client) Completion(convID string, req *CompletionRequest) (io.ReadCloser, error) {
	apiThrottle()
	url := fmt.Sprintf("%s/api/organizations/%s/chat_conversations/%s/completion",
		c.baseURL, c.orgID, convID)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create request: %w", err)
	}
	// cancel is NOT deferred here — caller reads resp.Body which needs the context alive.
	// context auto-cancels after 10min, killing a stuck SSE stream.
	_ = cancel

	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-client-platform", "web_claude_ai")
	httpReq.Header.Set("anthropic-client-version", "1.0.0")
	httpReq.Header.Set("Cookie", c.cookie)

	maxRetries := 6
	backoffs := []time.Duration{1 * time.Second, 1 * time.Second, 2 * time.Second, 2 * time.Second, 3 * time.Second, 3 * time.Second}
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			wait := backoffs[attempt-1]
			log.Printf("claudeweb: retry %d/%d after %v", attempt, maxRetries, wait)
			time.Sleep(wait)
			ctx, cancel = context.WithTimeout(context.Background(), 10*time.Minute)
			_ = cancel
			httpReq, _ = http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
			httpReq.Header.Set("Accept", "text/event-stream")
			httpReq.Header.Set("Content-Type", "application/json")
			httpReq.Header.Set("anthropic-client-platform", "web_claude_ai")
			httpReq.Header.Set("anthropic-client-version", "1.0.0")
			httpReq.Header.Set("Cookie", c.cookie)
		}

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("http request: %w", err)
			log.Printf("claudeweb: attempt %d http error: %v", attempt, err)
			continue
		}

		if resp.StatusCode >= 400 && resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			errStr := string(errBody)
			log.Printf("claudeweb: attempt %d status %d: %s", attempt, resp.StatusCode, truncBody(errBody))

			if isRateLimit(errStr) {
				if isProxyThrottle(errStr) {
					if waitDur := parseRetryAfter(errStr); waitDur > 0 {
						log.Printf("claudeweb: proxy throttle, sleeping %v until quota resets", waitDur)
						sleepWithHeartbeat(waitDur)
					} else {
						log.Printf("claudeweb: proxy throttle, waiting 2 minutes before retry")
						sleepWithHeartbeat(2 * time.Minute)
					}
					lastErr = &RateLimitError{Body: errStr}
					continue
				}
				lastErr = &RateLimitError{Body: errStr}
				return nil, lastErr
			}

			if resp.StatusCode == 403 && contains(errStr, "Just a moment") {
				// Cloudflare challenge — IP flagged, wait and retry
				log.Printf("claudeweb: Cloudflare challenge, waiting 3 minutes before retry")
				sleepWithHeartbeat(3 * time.Minute)
				lastErr = fmt.Errorf("cloudflare 403: challenge page")
				continue
			}

			if resp.StatusCode >= 500 {
				lastErr = fmt.Errorf("API %d: %s", resp.StatusCode, truncBody(errBody))
				continue
			}
			return nil, fmt.Errorf("API %d: %s", resp.StatusCode, truncBody(errBody))
		}

		return resp.Body, nil
	}

	// slow retry phase: network may recover in a few minutes.
	// especially important for Continue (writeback) where the child's work is done.
	log.Printf("claudeweb: fast retries exhausted, entering slow retry (3 attempts, 3min intervals)")
	for slowAttempt := 0; slowAttempt < 3; slowAttempt++ {
		sleepWithHeartbeat(3 * time.Minute)
		log.Printf("claudeweb: slow retry %d/3", slowAttempt+1)
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Minute)
		_ = cancel
		httpReq, _ = http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("anthropic-client-platform", "web_claude_ai")
		httpReq.Header.Set("anthropic-client-version", "1.0.0")
		httpReq.Header.Set("Cookie", c.cookie)
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("http request: %w", err)
			log.Printf("claudeweb: slow retry %d/3 http error: %v", slowAttempt+1, err)
			continue
		}
		if resp.StatusCode >= 400 {
			errBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("API %d: %s", resp.StatusCode, truncBody(errBody))
			log.Printf("claudeweb: slow retry %d/3 status %d", slowAttempt+1, resp.StatusCode)
			continue
		}
		return resp.Body, nil
	}

	return nil, lastErr
}

func (c *Client) CreateConversation(name, modelName string) (string, error) {
	url := fmt.Sprintf("%s/api/organizations/%s/chat_conversations",
		c.baseURL, c.orgID)

	body, err := json.Marshal(map[string]interface{}{
		"name":  name,
		"model": modelName,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", c.cookie)
	req.Header.Set("anthropic-client-platform", "web_claude_ai")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create conversation: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("create conversation %d: %s", resp.StatusCode, truncBody(respBody))
	}

	var result struct {
		UUID string `json:"uuid"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse create response: %w (body: %s)", err, truncBody(respBody))
	}
	convID := result.UUID
	if convID == "" {
		convID = result.ID
	}
	if convID == "" {
		return "", fmt.Errorf("no conversation id in response: %s", truncBody(respBody))
	}
	return convID, nil
}

func (c *Client) DownloadConversation(convID string) (string, error) {
	// tree=True: return the full message tree, not just the last leaf.
	// rendering_mode=messages: raw message format with tool_use/tool_result.
	// render_all_tools=true: include tool_use input (create_file file_text).
	// consistency=strong: don't return stale cache if remote just wrote.
	url := fmt.Sprintf("%s/api/organizations/%s/chat_conversations/%s?tree=True&rendering_mode=messages&render_all_tools=true&consistency=strong",
		c.baseURL, c.orgID, convID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Cookie", c.cookie)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download %s: status %d", convID[:8], resp.StatusCode)
	}

	var conv struct {
		ChatMessages []struct {
			Sender  string            `json:"sender"`
			Content []json.RawMessage `json:"content"`
		} `json:"chat_messages"`
	}
	if err := json.Unmarshal(body, &conv); err != nil {
		return string(body), nil
	}

	var md strings.Builder
	var createdFiles int
	for _, msg := range conv.ChatMessages {
		for _, raw := range msg.Content {
			var block struct {
				Type    string          `json:"type"`
				Text    string          `json:"text"`
				Name    string          `json:"name"`
				Input   json.RawMessage `json:"input"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(raw, &block) != nil {
				continue
			}

			switch block.Type {
			case "text":
				if msg.Sender == "assistant" && len(block.Text) > 0 {
					md.WriteString(block.Text)
					md.WriteString("\n\n")
				}
			case "tool_use":
				if block.Name == "create_file" || block.Name == "str_replace" {
					// create_file: input has {path, file_text}
					// this is the primary output channel for theory/code.
					// even if SSE broke mid-stream, the remote conversation
					// has the full file_text in its message history.
					var inp struct {
						Path     string `json:"path"`
						FileText string `json:"file_text"`
					}
					if json.Unmarshal(block.Input, &inp) == nil && inp.FileText != "" {
						md.WriteString(fmt.Sprintf("\n## FILE: %s\n", inp.Path))
						md.WriteString(inp.FileText)
						md.WriteString("\n\n")
						createdFiles++
					}
				} else if block.Name == "bash_tool" || block.Name == "bash" {
					var inp struct {
						Command string `json:"command"`
					}
					if json.Unmarshal(block.Input, &inp) == nil && inp.Command != "" {
						md.WriteString("$ " + inp.Command + "\n")
					}
				}
			case "tool_result":
				// shell execution output — compile results, test output
				var content string
				if json.Unmarshal(block.Content, &content) == nil {
					if len(content) > 0 {
						md.WriteString(content)
						md.WriteString("\n\n")
					}
				} else {
					var parts []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					}
					if json.Unmarshal(block.Content, &parts) == nil {
						for _, p := range parts {
							if p.Type == "text" && len(p.Text) > 0 {
								md.WriteString(p.Text)
								md.WriteString("\n\n")
							}
						}
					}
				}
			}
		}
	}
	log.Printf("claudeweb: downloaded %s: %d chars from %d messages (%d created files)",
		convID[:8], md.Len(), len(conv.ChatMessages), createdFiles)
	return md.String(), nil
}

// DownloadConversationRaw returns the raw JSON body of a conversation.
// same request as DownloadConversation but no parsing — caller handles it.
func (c *Client) DownloadConversationRaw(convID string) ([]byte, error) {
	url := fmt.Sprintf("%s/api/organizations/%s/chat_conversations/%s?tree=True&rendering_mode=messages&render_all_tools=true&consistency=strong",
		c.baseURL, c.orgID, convID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", c.cookie)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download-raw %s: status %d", convID[:8], resp.StatusCode)
	}
	return body, nil
}

// DownloadFile downloads a file from the remote sandbox (create_file outputs).
// uses the wiggle/download-file endpoint. parameters only, no text content.
func (c *Client) DownloadFile(convID, filePath string) ([]byte, error) {
	url := fmt.Sprintf("%s/api/organizations/%s/conversations/%s/wiggle/download-file?path=%s",
		c.baseURL, c.orgID, convID, filePath)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", c.cookie)
	req.Header.Set("Accept", "*/*")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download-file %s %s: status %d", convID[:8], filePath, resp.StatusCode)
	}
	log.Printf("claudeweb: download-file %s %s: %d bytes", convID[:8], filePath, len(body))
	return body, nil
}

func (c *Client) StopResponse(convID string) error {
	url := fmt.Sprintf("%s/api/organizations/%s/chat_conversations/%s/stop_response",
		c.baseURL, c.orgID, convID)
	body, _ := json.Marshal(map[string]string{})
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", c.cookie)
	req.Header.Set("anthropic-client-platform", "web_claude_ai")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	log.Printf("claudeweb: stop_response %s -> %d", convID[:8], resp.StatusCode)
	return nil
}

// ProbeConvBusy checks if a remote conversation's code execution environment is busy.
// Sends a minimal completion request. If the server returns wiggle_container_in_use,
// the remote is still running (SSE dropped but execution continues).
// Returns:
//   - true, nil:  remote is busy
//   - false, nil: remote is idle (accepted the probe or returned non-busy error)
//   - false, err: network error, inconclusive
func (c *Client) ProbeConvBusy(convID string) (bool, error) {
	url := fmt.Sprintf("%s/api/organizations/%s/chat_conversations/%s/completion",
		c.baseURL, c.orgID, convID)

	probeReq := map[string]interface{}{
		"prompt":         "continue",
		"model":          "claude-sonnet-4-6",
		"timezone":       "Asia/Shanghai",
		"locale":         "en-US",
		"effort":         "low",
		"thinking_mode":  "off",
		"rendering_mode": "messages",
		"attachments":    []interface{}{},
		"files":          []interface{}{},
		"sync_sources":   []interface{}{},
		"tools":          []interface{}{},
	}

	body, err := json.Marshal(probeReq)
	if err != nil {
		return false, fmt.Errorf("marshal probe: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("create probe request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-client-platform", "web_claude_ai")
	req.Header.Set("Cookie", c.cookie)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("probe http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		// remote accepted the probe — it was idle. BUT the probe message ("continue")
		// may go to a stale/reset context and generate garbage (e.g. metamaterials).
		// immediately stop the response so it doesn't contaminate the conversation.
		resp.Body.Close()
		if stopErr := c.StopResponse(convID); stopErr != nil {
			log.Printf("claudeweb: probe %s → 200 (idle), stop failed: %v", convID[:8], stopErr)
		} else {
			log.Printf("claudeweb: probe %s → 200 (idle, stopped)", convID[:8])
		}
		return false, nil
	}

	errBody, _ := io.ReadAll(resp.Body)
	errStr := string(errBody)

	if strings.Contains(errStr, "wiggle_container_in_use") || strings.Contains(errStr, "Another response is already running") {
		log.Printf("claudeweb: probe %s → busy (container_in_use)", convID[:8])
		return true, nil
	}

	// rate limit or other error — inconclusive about busy-ness
	log.Printf("claudeweb: probe %s → %d: %s", convID[:8], resp.StatusCode, truncBody(errBody))
	return false, nil
}

func truncBody(b []byte) string {
	s := string(b)
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

type RateLimitError struct {
	Body       string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("rate limit: retry after %v", e.RetryAfter)
	}
	return "rate limit: " + truncBody([]byte(e.Body))
}

// parseRetryAfter: extract "请 2026-10-18 13:40:00 后重试" or "try again after 2026-10-18 13:40:00"
// returns duration until that time, or 0 if not parseable.
func parseRetryAfter(body string) time.Duration {
	// look for YYYY-MM-DD HH:MM:SS pattern
	for i := 0; i+18 < len(body); i++ {
		if body[i] >= '2' && body[i] <= '2' && body[i+4] == '-' && body[i+7] == '-' && body[i+10] == ' ' && body[i+13] == ':' && body[i+16] == ':' {
			ts := body[i : i+19]
			// try parsing with Shanghai timezone (proxy is Chinese)
			loc, _ := time.LoadLocation("Asia/Shanghai")
			if loc == nil {
				loc = time.FixedZone("CST", 8*3600)
			}
			t, err := time.ParseInLocation("2006-01-02 15:04:05", ts, loc)
			if err == nil {
				dur := time.Until(t)
				if dur > 0 {
					return dur
				}
			}
		}
	}
	return 0
}

func isRateLimit(body string) bool {
	return len(body) > 0 &&
		(contains(body, "使用次数已达到限制") ||
			contains(body, "rate limit") ||
			contains(body, "usage limit") ||
			contains(body, "为防止claude封号") ||
			contains(body, "提问次数已达上限") ||
			contains(body, "频率过快") ||
			contains(body, "count has reached the limit"))
}

func isProxyThrottle(body string) bool {
	return contains(body, "为防止claude封号") ||
		contains(body, "提问次数已达上限") ||
		contains(body, "频率过快") ||
		contains(body, "count has reached the limit")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchStr(s, sub)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
