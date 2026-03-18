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
	"time"
)

// Config holds the configuration for the gateway client.
// Flags take precedence over environment variables; callers merge them before
// calling New().
type Config struct {
	GatewayURL  string
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
}

// QueryResponse holds the parsed response from a query.
type QueryResponse struct {
	Text    string // Response text from the agent.
	TraceID string // X-Trace-ID response header value.
	Agent   string // Resolved agent name as reported by the gateway.
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
		return nil, fmt.Errorf("authentication failed: check --user / --api-key credentials")
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
		Agent string `json:"agent"`
		Text  string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &QueryResponse{
		Text:    result.Text,
		TraceID: resp.Header.Get("X-Trace-ID"),
		Agent:   result.Agent,
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
		return fmt.Errorf("authentication failed: check --user / --api-key credentials")
	default:
		return fmt.Errorf("gateway health check returned status %d", resp.StatusCode)
	}
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

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
