package claudeweb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(errBody))
	}

	return resp.Body, nil
}
