package agentutil

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

// playbookDraftTimeout is generous because draft synthesis is an LLM call on
// the gateway side, not a simple lookup — matches the longer of the two
// pre-unification caller timeouts (faulttest's 90s beat the incident agent's
// 60s) rather than risking a caller-specific regression by picking the
// shorter one.
const playbookDraftTimeout = 90 * time.Second

// RequestPlaybookDraft POSTs to the gateway's /api/v1/fleet/playbooks/from-trace
// endpoint to synthesize a Playbook draft from an audit trace, returning the
// draft YAML and the persisted playbook_id (empty when auditd is not
// configured on the gateway). seriesID, when non-empty, pins the draft to an
// existing series so the handler improves it instead of cold-synthesizing —
// "improvement mode".
//
// Shared between testing/cmd/faulttest (its own direct post-remediation draft
// request) and agents/incident (create_incident_bundle's outcome-triggered
// draft request) — two separate main packages that previously duplicated
// this exact request-building/response-parsing logic, with the incident
// agent's copy lacking series_id support.
func RequestPlaybookDraft(ctx context.Context, gatewayURL, apiKey, traceID, outcome, seriesID string) (draft, playbookID string, err error) {
	if gatewayURL == "" {
		return "", "", fmt.Errorf("gateway URL is required")
	}

	body := map[string]string{
		"trace_id": traceID,
		"outcome":  outcome,
	}
	if seriesID != "" {
		body["series_id"] = seriesID
	}
	reqBody, err := json.Marshal(body)
	if err != nil {
		return "", "", fmt.Errorf("marshal request: %w", err)
	}

	reqURL := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbooks/from-trace"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: playbookDraftTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("POST from-trace: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("gateway returned %d: %s", resp.StatusCode, respBody)
	}

	var result struct {
		Draft      string `json:"draft"`
		PlaybookID string `json:"playbook_id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", "", fmt.Errorf("decode response: %w", err)
	}
	return result.Draft, result.PlaybookID, nil
}
