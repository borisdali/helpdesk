//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// auditdPostStatus is like auditdPost but returns the HTTP status code alongside
// the decoded body, without calling t.Fatalf on 4xx responses. This lets tests
// assert specific error status codes (409 conflict, 422 not-reversible, etc.).
func auditdPostStatus(t *testing.T, auditdURL, path string, body any) (map[string]any, int) {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, auditdURL+path, bodyReader)
	if err != nil {
		t.Fatalf("build request POST %s: %v", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var result map[string]any
	json.Unmarshal(raw, &result) //nolint:errcheck
	return result, resp.StatusCode
}

// =============================================================================
// Rollback API lifecycle (auditd-direct, no LLM required)
// =============================================================================

// syntheticScaleEvent creates a tool_execution event in auditd with a
// ScalePreState — the same shape the K8s agent writes when it scales a
// deployment. Returns the event_id.
func syntheticScaleEvent(t *testing.T, auditdURL string, previousReplicas int) string {
	t.Helper()
	eventID := fmt.Sprintf("e2e-scale-%d", time.Now().UnixNano())
	preState, _ := json.Marshal(map[string]any{
		"namespace":         "production",
		"deployment_name":   "api",
		"previous_replicas": previousReplicas,
	})
	payload := map[string]any{
		"event_id":     eventID,
		"timestamp":    time.Now().UTC().Format(time.RFC3339Nano),
		"event_type":   "tool_execution",
		"trace_id":     "e2e-rbk-" + eventID[len(eventID)-8:],
		"session":      map[string]any{"id": "e2e-rbk-session"},
		"action_class": "destructive",
		"tool": map[string]any{
			"name":      "scale_deployment",
			"pre_state": json.RawMessage(preState),
		},
		"outcome": map[string]any{"status": "success"},
	}
	created := auditdPost(t, auditdURL, "/v1/events", payload)
	if created["event_id"] == nil {
		t.Fatalf("syntheticScaleEvent: event_id missing from response: %v", created)
	}
	return eventID
}

// TestRollback_PreState_SurvivesStoreRoundTrip verifies that a tool_execution
// event written with pre_state survives the auditd store round-trip and can
// be retrieved with the pre_state field intact — a prerequisite for rollback
// plan derivation.
func TestRollback_PreState_SurvivesStoreRoundTrip(t *testing.T) {
	c := LoadConfig()
	if !isAuditdReachable(c.AuditdURL) {
		t.Skipf("auditd not reachable at %s", c.AuditdURL)
	}

	eventID := syntheticScaleEvent(t, c.AuditdURL, 3)
	t.Logf("created event: %s", eventID)

	result := auditdGet(t, c.AuditdURL, "/v1/events/"+eventID)
	if result["event_id"] != eventID {
		t.Errorf("event_id = %v, want %s", result["event_id"], eventID)
	}

	tool, _ := result["tool"].(map[string]any)
	if tool == nil {
		t.Fatal("tool field missing from retrieved event")
	}
	if tool["name"] != "scale_deployment" {
		t.Errorf("tool.name = %q, want scale_deployment", tool["name"])
	}
	if tool["pre_state"] == nil {
		t.Error("tool.pre_state missing after store round-trip")
	}
	preState, _ := tool["pre_state"].(map[string]any)
	if preState != nil {
		if v, _ := preState["previous_replicas"].(float64); v != 3 {
			t.Errorf("pre_state.previous_replicas = %v, want 3", preState["previous_replicas"])
		}
	}
	t.Logf("pre_state round-trip OK: %v", tool["pre_state"])
}

// TestRollback_DerivePlan_OK verifies POST /v1/events/{id}/rollback-plan
// returns a valid plan with the correct inverse operation for a scale event.
func TestRollback_DerivePlan_OK(t *testing.T) {
	c := LoadConfig()
	if !isAuditdReachable(c.AuditdURL) {
		t.Skipf("auditd not reachable at %s", c.AuditdURL)
	}

	eventID := syntheticScaleEvent(t, c.AuditdURL, 5)
	t.Logf("event: %s", eventID)

	plan, status := auditdPostStatus(t, c.AuditdURL, "/v1/events/"+eventID+"/rollback-plan", nil)
	if status != http.StatusOK {
		t.Fatalf("derive plan: status = %d, want 200; body: %v", status, plan)
	}
	t.Logf("plan: %v", plan)

	if plan["reversibility"] != "yes" {
		t.Errorf("reversibility = %q, want yes", plan["reversibility"])
	}
	inverseOp, _ := plan["inverse_op"].(map[string]any)
	if inverseOp == nil {
		t.Fatal("inverse_op missing from plan")
	}
	if inverseOp["tool"] != "scale_deployment" {
		t.Errorf("inverse_op.tool = %q, want scale_deployment", inverseOp["tool"])
	}
	args, _ := inverseOp["args"].(map[string]any)
	if args == nil {
		t.Fatal("inverse_op.args missing")
	}
	if replicas, _ := args["replicas"].(float64); replicas != 5 {
		t.Errorf("inverse_op.args.replicas = %v, want 5", args["replicas"])
	}
}

// TestRollback_DerivePlan_NotFound returns 404 for an unknown event ID.
func TestRollback_DerivePlan_NotFound(t *testing.T) {
	c := LoadConfig()
	if !isAuditdReachable(c.AuditdURL) {
		t.Skipf("auditd not reachable at %s", c.AuditdURL)
	}

	_, status := auditdPostStatus(t, c.AuditdURL, "/v1/events/e2e-ghost-99999/rollback-plan", nil)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for non-existent event", status)
	}
}

// TestRollback_InitiateLifecycle exercises the complete rollback API:
//
//	initiate (201) → list → get → cancel (200) → verify persisted.
func TestRollback_InitiateLifecycle(t *testing.T) {
	c := LoadConfig()
	if !isAuditdReachable(c.AuditdURL) {
		t.Skipf("auditd not reachable at %s", c.AuditdURL)
	}

	eventID := syntheticScaleEvent(t, c.AuditdURL, 3)
	t.Logf("original event: %s", eventID)

	// --- Initiate ---
	created, initStatus := auditdPostStatus(t, c.AuditdURL, "/v1/rollbacks", map[string]any{
		"original_event_id": eventID,
		"justification":     "e2e test rollback",
	})
	if initStatus != http.StatusCreated {
		t.Fatalf("initiate: status = %d, want 201; body: %v", initStatus, created)
	}
	rollbackObj, _ := created["rollback"].(map[string]any)
	if rollbackObj == nil {
		t.Fatalf("rollback field missing from initiate response: %v", created)
	}
	rollbackID, _ := rollbackObj["rollback_id"].(string)
	if rollbackID == "" {
		t.Fatal("rollback_id missing from rollback object")
	}
	if rollbackObj["status"] != "pending_approval" {
		t.Errorf("status = %q, want pending_approval", rollbackObj["status"])
	}
	t.Logf("rollback_id: %s", rollbackID)

	// --- List ---
	records := auditdGetList(t, c.AuditdURL, "/v1/rollbacks")
	found := false
	for _, r := range records {
		if r["rollback_id"] == rollbackID {
			found = true
		}
	}
	if !found {
		t.Errorf("rollback %s not found in GET /v1/rollbacks", rollbackID)
	}

	// --- Get ---
	detail := auditdGet(t, c.AuditdURL, "/v1/rollbacks/"+rollbackID)
	if detail["rollback"] == nil {
		t.Error("rollback field missing from GET /v1/rollbacks/{id} response")
	}

	// --- Cancel ---
	cancelled, cancelStatus := auditdPostStatus(t, c.AuditdURL, "/v1/rollbacks/"+rollbackID+"/cancel", nil)
	if cancelStatus != http.StatusOK {
		t.Fatalf("cancel: status = %d, want 200; body: %v", cancelStatus, cancelled)
	}
	cancelledRbk, _ := cancelled["rollback"].(map[string]any)
	if cancelledRbk == nil {
		t.Fatal("rollback field missing from cancel response")
	}
	if cancelledRbk["status"] != "cancelled" {
		t.Errorf("status after cancel = %q, want cancelled", cancelledRbk["status"])
	}

	// Verify the cancelled status was persisted.
	after := auditdGet(t, c.AuditdURL, "/v1/rollbacks/"+rollbackID)
	if afterRbk, _ := after["rollback"].(map[string]any); afterRbk != nil {
		if afterRbk["status"] != "cancelled" {
			t.Errorf("persisted status = %q, want cancelled", afterRbk["status"])
		}
	}
	t.Logf("rollback lifecycle OK: %s → pending_approval → cancelled", rollbackID)
}

// TestRollback_DryRun verifies dry-run returns a plan without creating a record.
func TestRollback_DryRun(t *testing.T) {
	c := LoadConfig()
	if !isAuditdReachable(c.AuditdURL) {
		t.Skipf("auditd not reachable at %s", c.AuditdURL)
	}

	eventID := syntheticScaleEvent(t, c.AuditdURL, 2)

	resp, status := auditdPostStatus(t, c.AuditdURL, "/v1/rollbacks", map[string]any{
		"original_event_id": eventID,
		"dry_run":           true,
	})
	if status != http.StatusOK {
		t.Fatalf("dry-run: status = %d, want 200; body: %v", status, resp)
	}
	if resp["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", resp["dry_run"])
	}
	if resp["plan"] == nil {
		t.Error("plan missing from dry-run response")
	}

	// No record should be created for this event.
	records := auditdGetList(t, c.AuditdURL, "/v1/rollbacks")
	for _, r := range records {
		if r["original_event_id"] == eventID {
			t.Errorf("dry-run unexpectedly created a persistent rollback record for %s", eventID)
		}
	}
}

// TestRollback_Duplicate_Returns409 verifies that a second initiation for the
// same event_id returns HTTP 409 Conflict.
func TestRollback_Duplicate_Returns409(t *testing.T) {
	c := LoadConfig()
	if !isAuditdReachable(c.AuditdURL) {
		t.Skipf("auditd not reachable at %s", c.AuditdURL)
	}

	eventID := syntheticScaleEvent(t, c.AuditdURL, 3)

	first, firstStatus := auditdPostStatus(t, c.AuditdURL, "/v1/rollbacks", map[string]any{
		"original_event_id": eventID,
	})
	if firstStatus != http.StatusCreated {
		t.Fatalf("first initiation: status = %d, want 201; body: %v", firstStatus, first)
	}

	_, secondStatus := auditdPostStatus(t, c.AuditdURL, "/v1/rollbacks", map[string]any{
		"original_event_id": eventID,
	})
	if secondStatus != http.StatusConflict {
		t.Errorf("duplicate initiation: status = %d, want 409", secondStatus)
	}
}
