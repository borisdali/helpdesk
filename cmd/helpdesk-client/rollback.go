package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"helpdesk/internal/client"
)

// runRollbackEvent initiates or dry-runs a rollback for a tool_execution event.
// auditURL is the auditd base URL; eventID is the tool_execution event to reverse.
func runRollbackEvent(ctx context.Context, cfg client.Config, eventID, justification string, dryRun bool) error {
	auditURL := strings.TrimRight(cfg.AuditURL, "/")
	if auditURL == "" {
		return fmt.Errorf("--audit-url (or HELPDESK_AUDIT_URL) is required for rollback operations")
	}

	reqBody := map[string]any{
		"original_event_id": eventID,
		"dry_run":           dryRun,
	}
	if justification != "" {
		reqBody["justification"] = justification
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	resp, err := doAuditRequest(ctx, cfg, "POST", auditURL+"/v1/rollbacks", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case 201, 200:
		if dryRun {
			fmt.Println("=== Rollback Plan (dry-run — nothing was persisted) ===")
		} else {
			fmt.Println("=== Rollback Initiated ===")
		}
		prettyPrint(respBody)
	case 409:
		fmt.Println("=== Conflict: Active Rollback Exists ===")
		prettyPrint(respBody)
		return fmt.Errorf("active rollback already exists for event %s", eventID)
	case 422:
		fmt.Println("=== Not Reversible ===")
		prettyPrint(respBody)
		return fmt.Errorf("event %s is not reversible (HTTP 422)", eventID)
	case 404:
		return fmt.Errorf("event %s not found or not a tool_execution event", eventID)
	default:
		return fmt.Errorf("auditd returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// runRollbackPlan derives and prints a rollback plan for an event without persisting anything.
func runRollbackPlan(ctx context.Context, cfg client.Config, eventID string) error {
	auditURL := strings.TrimRight(cfg.AuditURL, "/")
	if auditURL == "" {
		return fmt.Errorf("--audit-url (or HELPDESK_AUDIT_URL) is required for rollback operations")
	}

	url := fmt.Sprintf("%s/v1/events/%s/rollback-plan", auditURL, eventID)
	resp, err := doAuditRequest(ctx, cfg, "POST", url, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("auditd returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	fmt.Println("=== Rollback Plan ===")
	prettyPrint(respBody)
	return nil
}

// runListRollbacks lists rollback records from the auditd service.
func runListRollbacks(ctx context.Context, cfg client.Config) error {
	auditURL := strings.TrimRight(cfg.AuditURL, "/")
	if auditURL == "" {
		return fmt.Errorf("--audit-url (or HELPDESK_AUDIT_URL) is required for rollback operations")
	}

	resp, err := doAuditRequest(ctx, cfg, "GET", auditURL+"/v1/rollbacks", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("auditd returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	fmt.Println("=== Rollbacks ===")
	prettyPrint(respBody)
	return nil
}

// doAuditRequest sends an HTTP request to the auditd service with auth headers.
func doAuditRequest(ctx context.Context, cfg client.Config, method, url string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	if cfg.UserID != "" {
		req.Header.Set("X-User", cfg.UserID)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to %s failed: %w", url, err)
	}
	return resp, nil
}

// prettyPrint JSON-decodes and re-encodes with indentation for human display.
// Falls back to raw output if decoding fails.
func prettyPrint(data []byte) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		fmt.Println(string(data))
		return
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Println(string(data))
		return
	}
	fmt.Println(string(out))
}
