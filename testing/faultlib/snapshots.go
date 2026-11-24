package faultlib

// Snapshots a fault's remediation reads back. A remediation that restores a value
// the fault is about to break (db-backup-archiving-broken restores archive_command
// from get_saved_snapshots) can only do so if a reading of the healthy value was
// recorded before the injection; a stack that is new or whose audit volume was
// wiped has none, and the remediation correctly refuses to guess ("no_action").
// The harness records the reading itself, through the gateway's direct tool route,
// which stores the result the same way a scheduled baseline job does.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CaptureSnapshots calls each of the fault's pre_snapshots tools once, against the
// fault's agent connection, before the fault is injected. It does nothing when the
// fault lists none, and it needs the gateway: a direct run has no audit trail to
// record into, so the remediation could not read it back either.
func CaptureSnapshots(ctx context.Context, cfg *HarnessConfig, f Failure) error {
	if len(f.PreSnapshots) == 0 || !cfg.ViaGateway || cfg.GatewayURL == "" {
		return nil
	}
	conn := ResolvedAgentConnStr(cfg, f)
	for _, tool := range f.PreSnapshots {
		if err := captureSnapshot(ctx, cfg, tool, conn); err != nil {
			return fmt.Errorf("recording a %s snapshot before injecting %s: %w", tool, f.ID, err)
		}
	}
	return nil
}

func captureSnapshot(ctx context.Context, cfg *HarnessConfig, tool, conn string) error {
	body, err := json.Marshal(map[string]any{"connection_string": conn})
	if err != nil {
		return err
	}
	url := strings.TrimSuffix(cfg.GatewayURL, "/") + "/api/v1/db/" + tool
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.GatewayAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.GatewayAPIKey)
	}
	if cfg.OperatorID != "" {
		req.Header.Set("X-User", cfg.OperatorID)
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("gateway returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}
