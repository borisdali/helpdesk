package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// preflightConfig holds the connection info needed for preflight checks.
type preflightConfig struct {
	gatewayURL string
	apiKey     string
	jobID      string // used in X-Purpose-Note (may be empty before job creation)
}

// preflightServer verifies that a single database server is reachable via the
// gateway before any stage execution begins. It calls POST /api/v1/db/check_connection.
func preflightServer(ctx context.Context, cfg preflightConfig, serverName string) error {
	args := map[string]any{
		"db_server": serverName,
	}
	body, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("marshal preflight args: %w", err)
	}

	url := cfg.gatewayURL + "/api/v1/db/check_connection"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create preflight request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Purpose", "fleet_rollout")
	if cfg.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.apiKey)
	}
	note := "preflight"
	if cfg.jobID != "" {
		note = "preflight job_id=" + cfg.jobID
	}
	note += " server=" + serverName
	req.Header.Set("X-Purpose-Note", note)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("gateway unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("check_connection returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// runPreflight runs preflightServer for all servers sequentially.
// Returns a map of serverName → error for any failures.
func runPreflight(ctx context.Context, cfg preflightConfig, servers []string) map[string]error {
	failures := make(map[string]error)
	for _, server := range servers {
		slog.Info("preflight check", "server", server)
		if err := preflightServer(ctx, cfg, server); err != nil {
			slog.Warn("preflight failed", "server", server, "err", err)
			failures[server] = err
		} else {
			slog.Info("preflight ok", "server", server)
		}
	}
	return failures
}
