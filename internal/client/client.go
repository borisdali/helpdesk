// Package client provides an authenticated HTTP client for the aiHelpDesk gateway.
// All four client-layer binaries (helpdesk-client, fleet-runner, prevention-monitor,
// webui) embed this package so credential handling and header injection live in one place.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// ConfirmedTool is a single tool execution confirmed in the audit trail.
type ConfirmedTool struct {
	Name        string // tool name, e.g. "cancel_query"
	ActionClass string // "read", "write", or "destructive"
}

// TraceVerification is the audit-trail evidence for a single gateway round-trip.
// It records every tool_execution event found for a given trace ID.
type TraceVerification struct {
	TraceID              string
	ToolsConfirmed       []ConfirmedTool
	WriteConfirmed       []string // tool names whose ActionClass is "write"
	DestructiveConfirmed []string // tool names whose ActionClass is "destructive"
}

// HasMutations returns true when any write or destructive tool was confirmed.
func (v *TraceVerification) HasMutations() bool {
	return len(v.WriteConfirmed) > 0 || len(v.DestructiveConfirmed) > 0
}

// Config holds the configuration for the gateway client.
// Flags take precedence over environment variables; callers merge them before
// calling New().
type Config struct {
	GatewayURL  string
	AuditURL    string        // base URL for auditd; "" disables VerifyTrace
	UserID      string        // X-User header (human users, static provider)
	APIKey      string        // Authorization: Bearer <key> (service accounts or api_key users)
	Purpose     string        // X-Purpose header (diagnostic, remediation, compliance, emergency)
	PurposeNote string        // X-Purpose-Note header (free-text, e.g. incident number)
	Timeout     time.Duration // per-request timeout; 0 → 5 minutes
}

// NewConfigFromEnv returns a Config populated from well-known environment variables.
// Callers may override individual fields from flags before calling New().
func NewConfigFromEnv() Config {
	return Config{
		GatewayURL:  envOrDefault("HELPDESK_GATEWAY_URL", "http://localhost:8080"),
		AuditURL:    os.Getenv("HELPDESK_AUDIT_URL"),
		UserID:      os.Getenv("HELPDESK_CLIENT_USER"),
		APIKey:      os.Getenv("HELPDESK_CLIENT_API_KEY"),
		Purpose:     os.Getenv("HELPDESK_SESSION_PURPOSE"),
		PurposeNote: os.Getenv("HELPDESK_SESSION_PURPOSE_NOTE"),
		Timeout:     5 * time.Minute,
	}
}

// Client is an authenticated HTTP client for the aiHelpDesk gateway.
// It attaches identity and purpose headers to every request.
type Client struct {
	cfg        Config
	httpClient *http.Client
}

// New creates a new Client with the given configuration.
func New(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: timeout},
	}
}

// GatewayURL returns the configured gateway base URL.
func (c *Client) GatewayURL() string { return c.cfg.GatewayURL }

// QueryRequest holds the parameters for a single query.
type QueryRequest struct {
	// Agent is the target agent alias: database, db, k8s, incident, research.
	// Required — the gateway returns a 400 if empty or unknown.
	Agent string
	// Message is the natural language query sent to the agent.
	Message string
	// PurposeNote overrides the config-level purpose note for this request only
	// (e.g. to attach a per-query incident ticket number).
	PurposeNote string
	// ContextID resumes an existing agent session from a prior query.
	// Leave empty on the first turn; pass the value returned in QueryResponse.ContextID
	// on all subsequent turns to maintain conversation history.
	ContextID string
}

// QueryResponse holds the parsed response from a query.
type QueryResponse struct {
	Text      string // Response text from the agent.
	TraceID   string // X-Trace-ID response header value.
	Agent     string // Resolved agent name as reported by the gateway.
	ContextID string // Agent session context ID — pass back on the next turn to continue the conversation.
}

// Query sends a natural language query to the gateway and returns the response.
// The gateway performs identity validation, policy enforcement, and routes the
// request to the appropriate agent.
func (c *Client) Query(ctx context.Context, req QueryRequest) (*QueryResponse, error) {
	body := map[string]any{
		"agent":   req.Agent,
		"message": req.Message,
	}
	note := req.PurposeNote
	if note == "" {
		note = c.cfg.PurposeNote
	}
	if note != "" {
		body["purpose_note"] = note
	}
	if req.ContextID != "" {
		body["context_id"] = req.ContextID
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.GatewayURL+"/api/v1/query", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.addHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gateway unreachable (%s): %w", c.cfg.GatewayURL, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// handled below
	case http.StatusUnauthorized:
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e) //nolint:errcheck
		if e.Error != "" {
			return nil, fmt.Errorf("%s — set --user / --api-key or check your .env credentials", e.Error)
		}
		return nil, fmt.Errorf("authentication required — set --user / --api-key or check your .env credentials")
	default:
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e) //nolint:errcheck
		if e.Error != "" {
			return nil, fmt.Errorf("gateway error (%d): %s", resp.StatusCode, e.Error)
		}
		return nil, fmt.Errorf("gateway returned status %d", resp.StatusCode)
	}

	var result struct {
		Agent     string `json:"agent"`
		Text      string `json:"text"`
		ContextID string `json:"context_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &QueryResponse{
		Text:      result.Text,
		TraceID:   resp.Header.Get("X-Trace-ID"),
		Agent:     result.Agent,
		ContextID: result.ContextID,
	}, nil
}

// Ping verifies that the gateway is reachable and that credentials are accepted.
// It calls GET /api/v1/agents which requires no agent-specific permissions.
func (c *Client) Ping(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.cfg.GatewayURL+"/api/v1/agents", nil)
	if err != nil {
		return err
	}
	c.addHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("gateway unreachable at %s: %w", c.cfg.GatewayURL, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e) //nolint:errcheck
		if e.Error != "" {
			return fmt.Errorf("%s — set --user / --api-key or check your .env credentials", e.Error)
		}
		return fmt.Errorf("authentication required — set --user / --api-key or check your .env credentials")
	default:
		return fmt.Errorf("gateway health check returned status %d", resp.StatusCode)
	}
}

// Do sends a raw HTTP request to the gateway with all standard auth headers attached.
// extraHeaders contains optional additional headers (e.g. X-Purpose-Note for per-call context).
// The caller is responsible for closing the returned response body.
func (c *Client) Do(ctx context.Context, method, path string, body []byte, extraHeaders ...map[string]string) (*http.Response, error) {
	var bodyReader *bytes.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	} else {
		bodyReader = bytes.NewReader(nil)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, c.cfg.GatewayURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.addHeaders(httpReq)
	for _, hdrs := range extraHeaders {
		for k, v := range hdrs {
			httpReq.Header.Set(k, v)
		}
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gateway unreachable (%s): %w", c.cfg.GatewayURL, err)
	}
	return resp, nil
}

// addHeaders attaches authentication and session headers to every outgoing request.
// Header precedence mirrors the gateway's identity resolution order:
// Authorization: Bearer (api_key / service account) takes precedence over X-User.
func (c *Client) addHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	if c.cfg.UserID != "" {
		req.Header.Set("X-User", c.cfg.UserID)
	}
	if c.cfg.Purpose != "" {
		req.Header.Set("X-Purpose", c.cfg.Purpose)
	}
}

// VerifyTrace queries auditd for tool_execution events belonging to traceID
// that occurred at or after since. It retries once after 200 ms to absorb
// async write propagation (same strategy as buildDelegationVerification in
// internal/audit/delegate_tool.go).
//
// Returns nil, nil when AuditURL is not configured — verification is optional
// and callers should treat a nil result as "not available".
func (c *Client) VerifyTrace(ctx context.Context, traceID string, since time.Time) (*TraceVerification, error) {
	if c.cfg.AuditURL == "" {
		return nil, nil
	}

	reqURL := strings.TrimRight(c.cfg.AuditURL, "/") +
		"/v1/events?event_type=tool_execution&trace_id=" + traceID +
		"&since=" + since.UTC().Format(time.RFC3339)

	type rawEvent struct {
		ActionClass string `json:"action_class"`
		Tool        *struct {
			Name string `json:"name"`
		} `json:"tool"`
	}

	var events []rawEvent
	var lastErr error

	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(200 * time.Millisecond)
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("verify trace: build request: %w", err)
		}
		c.addHeaders(httpReq)

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("verify trace: %w", err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("verify trace: auditd returned status %d", resp.StatusCode)
			continue
		}
		decErr := json.NewDecoder(resp.Body).Decode(&events)
		resp.Body.Close()
		if decErr != nil {
			lastErr = fmt.Errorf("verify trace: decode: %w", decErr)
			continue
		}
		if len(events) == 0 && attempt == 0 {
			// Empty on first attempt — retry once for async propagation.
			continue
		}
		lastErr = nil
		break
	}

	if lastErr != nil {
		return nil, lastErr
	}

	v := &TraceVerification{TraceID: traceID}
	for _, ev := range events {
		if ev.Tool == nil || ev.Tool.Name == "" {
			continue
		}
		name := ev.Tool.Name
		v.ToolsConfirmed = append(v.ToolsConfirmed, ConfirmedTool{Name: name, ActionClass: ev.ActionClass})
		switch ev.ActionClass {
		case "write":
			v.WriteConfirmed = append(v.WriteConfirmed, name)
		case "destructive":
			v.DestructiveConfirmed = append(v.DestructiveConfirmed, name)
		}
	}
	return v, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
