package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// auditEvent is a minimal representation of an audit event for tool evidence.
type auditEvent struct {
	EventType string `json:"event_type"`
	Tool      *struct {
		Name string `json:"name"`
	} `json:"tool,omitempty"`
}

// auditQueryTools fetches tool execution names from the audit service for the
// given time window. Returns nil when AuditURL is empty or the query fails.
//
// It calls GET {auditURL}/v1/events?since=RFC3339&event_type=tool_execution
// and extracts the tool name from each matching event.
func auditQueryTools(ctx context.Context, auditURL string, since time.Time) []string {
	if auditURL == "" {
		return nil
	}

	reqURL := fmt.Sprintf("%s/v1/events?since=%s&event_type=tool_execution",
		auditURL, since.UTC().Format(time.RFC3339))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		slog.Warn("audit query: failed to build request", "err", err)
		return nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("audit query: HTTP request failed", "url", reqURL, "err", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		slog.Warn("audit query: unexpected status", "status", resp.StatusCode, "body", string(body))
		return nil
	}

	var events []auditEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		slog.Warn("audit query: failed to decode response", "err", err)
		return nil
	}

	var tools []string
	seen := make(map[string]bool)
	for _, e := range events {
		if e.Tool == nil || e.Tool.Name == "" {
			continue
		}
		if !seen[e.Tool.Name] {
			tools = append(tools, e.Tool.Name)
			seen[e.Tool.Name] = true
		}
	}

	slog.Debug("audit query: found tool executions", "count", len(tools), "tools", tools)
	return tools
}
