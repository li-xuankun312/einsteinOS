package kernel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type AxleDriver struct {
	Cookie string // AXLE_COOKIE env or refreshed by user
	CSRF   string // AXLE_CSRF env
}

type axle_input struct {
	FormalStatement string `json:"formal_statement"`
	Content         string `json:"content"`
}

func (d *AxleDriver) Match(name string) bool {
	n := strings.ToLower(name)
	return n == "axle_verify" || n == "verify_proof" || n == "lean_verify"
}

func (d *AxleDriver) Execute(name string, inputJSON string, workDir string) string {
	var input axle_input
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return fmt.Sprintf("parse error: %v", err)
	}

	if input.FormalStatement == "" || input.Content == "" {
		return `usage: axle_verify {"formal_statement": "theorem ... := by sorry", "content": "theorem ... := by omega"}`
	}

	payload := map[string]interface{}{
		"formal_statement":      input.FormalStatement,
		"content":               input.Content,
		"permitted_sorries":     []string{},
		"ignore_imports":        true,
		"allow_statement_axioms": true,
		"allow_bv_decide":       true,
		"environment":           "lean-4.35.0",
		"timeout_seconds":       120,
	}

	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", "https://axle.axiommath.ai/api/v1/verify_proof", bytes.NewReader(body))
	if err != nil {
		return fmt.Sprintf("request build error: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-request-source", "web")
	req.Header.Set("Origin", "https://axle.axiommath.ai")
	req.Header.Set("Referer", "https://axle.axiommath.ai/verify_proof")
	if d.Cookie != "" {
		req.Header.Set("Cookie", d.Cookie)
	}
	if d.CSRF != "" {
		req.Header.Set("x-browser-api-csrf", d.CSRF)
	}

	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Sprintf("axle request failed: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Sprintf("axle HTTP %d: %s", resp.StatusCode, truncAxle(string(respBody), 500))
	}

	var result struct {
		Okay               bool `json:"okay"`
		FailedDeclarations []string `json:"failed_declarations"`
		LeanMessages       struct {
			Errors []string `json:"errors"`
		} `json:"lean_messages"`
		ToolMessages struct {
			Errors []string `json:"errors"`
		} `json:"tool_messages"`
		Timings struct {
			TotalMs int `json:"total_ms"`
		} `json:"timings"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Sprintf("axle parse error: %v\nraw: %s", err, truncAxle(string(respBody), 500))
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("okay=%v time=%dms\n", result.Okay, result.Timings.TotalMs))

	for _, e := range result.ToolMessages.Errors {
		sb.WriteString(fmt.Sprintf("  tool_err: %s\n", e))
	}
	for _, e := range result.LeanMessages.Errors {
		sb.WriteString(fmt.Sprintf("  lean_err: %s\n", e))
	}
	if len(result.FailedDeclarations) > 0 {
		sb.WriteString(fmt.Sprintf("  failed: %v\n", result.FailedDeclarations))
	}

	log.Printf("[axle] verify: okay=%v time=%dms", result.Okay, result.Timings.TotalMs)
	return sb.String()
}

func truncAxle(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
