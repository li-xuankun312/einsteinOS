package claudeweb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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
		hc = &http.Client{}
	}
	return &Client{
		baseURL:    cfg.BaseURL,
		orgID:      cfg.OrgID,
		cookie:     cfg.Cookie,
		httpClient: hc,
	}
}

// Completion sends a completion request and returns the raw SSE response body.
// The caller is responsible for closing the body.
func (c *Client) Completion(convID string, req *CompletionRequest) (io.ReadCloser, error) {
	url := fmt.Sprintf("%s/api/organizations/%s/chat_conversations/%s/completion",
		c.baseURL, c.orgID, convID)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-client-platform", "web_claude_ai")
	httpReq.Header.Set("anthropic-client-version", "1.0.0")
	httpReq.Header.Set("Cookie", c.cookie)

	maxRetries := 3
	backoffs := []time.Duration{3 * time.Second, 8 * time.Second, 15 * time.Second}
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			wait := backoffs[attempt-1]
			log.Printf("claudeweb: retry %d/%d after %v", attempt, maxRetries, wait)
			time.Sleep(wait)
			httpReq, _ = http.NewRequest("POST", url, bytes.NewReader(body))
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

		if resp.StatusCode >= 500 {
			errBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("API %d: %s", resp.StatusCode, truncBody(errBody))
			log.Printf("claudeweb: attempt %d status %d: %s", attempt, resp.StatusCode, truncBody(errBody))
			continue
		}

		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			log.Printf("claudeweb: attempt %d status %d: %s", attempt, resp.StatusCode, truncBody(errBody))
			return nil, fmt.Errorf("API %d: %s", resp.StatusCode, truncBody(errBody))
		}

		return resp.Body, nil
	}

	return nil, lastErr
}

func truncBody(b []byte) string {
	s := string(b)
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
