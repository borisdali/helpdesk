package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestRemediator returns a Remediator pointed at the given server URL.
func newTestRemediator(t *testing.T, serverURL string) *Remediator {
	t.Helper()
	return NewRemediator(&HarnessConfig{
		GatewayURL:    serverURL,
		GatewayAPIKey: "test-key",
		ConnStr:       "host=localhost port=5432 dbname=testdb user=postgres",
	})
}

func TestTriggerPlaybook_Success(t *testing.T) {
	var gotPath, gotAuth, gotPurpose string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			// resolvePlaybookID: return the series_id as the playbook_id (simplified).
			seriesID := r.URL.Query().Get("series_id")
			json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
				"playbooks": []map[string]interface{}{{"playbook_id": seriesID}},
			})
			return
		}
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotPurpose = r.Header.Get("X-Purpose")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if err := r.triggerPlaybook(context.Background(), "pbs_restart"); err != nil {
		t.Fatalf("triggerPlaybook: %v", err)
	}

	if gotPath != "/api/v1/fleet/playbooks/pbs_restart/run" {
		t.Errorf("path = %q, want /api/v1/fleet/playbooks/pbs_restart/run", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	if gotPurpose != "remediation" {
		t.Errorf("X-Purpose = %q, want remediation", gotPurpose)
	}
}

func TestTriggerPlaybook_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if err := r.triggerPlaybook(context.Background(), "pbs_restart"); err == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

// ── resolvePlaybookID ─────────────────────────────────────────────────────

func TestResolvePlaybookID_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("series_id") != "pbs_db_restart_triage" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
			"playbooks": []map[string]interface{}{
				{"playbook_id": "pb_f49b5eac", "series_id": "pbs_db_restart_triage"},
			},
		})
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	id, err := r.resolvePlaybookID(context.Background(), "pbs_db_restart_triage")
	if err != nil {
		t.Fatalf("resolvePlaybookID: %v", err)
	}
	if id != "pb_f49b5eac" {
		t.Errorf("playbook_id = %q, want pb_f49b5eac", id)
	}
}

func TestResolvePlaybookID_Empty(t *testing.T) {
	// Gateway returns empty list → no active playbook for this series.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"playbooks": []interface{}{}}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	_, err := r.resolvePlaybookID(context.Background(), "pbs_missing")
	if err == nil {
		t.Error("expected error for empty playbooks list, got nil")
	}
}

func TestResolvePlaybookID_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	_, err := r.resolvePlaybookID(context.Background(), "pbs_test")
	if err == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

func TestResolvePlaybookID_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
			"playbooks": []map[string]interface{}{{"playbook_id": "pb_abc"}},
		})
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	r.resolvePlaybookID(context.Background(), "pbs_test") //nolint:errcheck
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
}

func TestTriggerPlaybook_NoGateway(t *testing.T) {
	r := NewRemediator(&HarnessConfig{GatewayURL: "", ConnStr: "host=localhost"})
	if err := r.triggerPlaybook(context.Background(), "pbs_restart"); err == nil {
		t.Error("expected error when GatewayURL is empty, got nil")
	}
}

func TestTriggerAgent_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if err := r.triggerAgent(context.Background(), "database", "restart the database"); err != nil {
		t.Fatalf("triggerAgent: %v", err)
	}

	if gotPath != "/api/v1/query" {
		t.Errorf("path = %q, want /api/v1/query", gotPath)
	}
}

func TestTriggerAgent_NoGateway(t *testing.T) {
	r := NewRemediator(&HarnessConfig{GatewayURL: "", ConnStr: "host=localhost"})
	if err := r.triggerAgent(context.Background(), "database", "restart"); err == nil {
		t.Error("expected error when GatewayURL is empty, got nil")
	}
}

func TestRemediate_NoAction(t *testing.T) {
	r := newTestRemediator(t, "http://localhost:9999") // won't be called
	result := r.Remediate(context.Background(), Failure{
		ID:          "no-action",
		Remediation: RemediationSpec{}, // no playbook, no agent
	})
	if result.Err == nil {
		t.Error("expected error when no remediation action is configured")
	}
}

// ── runApprovalLoop / proceedStep ────────────────────────────────────────────

// newApproveRunResponse builds a minimal pending_approval response.
func newApprovalResponse(runID string, stepIndex int, tool string) approveRunResponse {
	return approveRunResponse{
		RunID:      runID,
		Status:     "pending_approval",
		ApprovalID: "apr_test",
		Step: &approveRunStep{
			Index:  stepIndex,
			Agent:  "database",
			Tool:   tool,
			Args:   map[string]any{"pid": 1234},
			Reason: "Terminate root blocker",
		},
	}
}

func TestRunApprovalLoop_SingleStepComplete(t *testing.T) {
	// Server: first proceed call returns complete.
	proceedCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		proceedCalled = true
		json.NewEncoder(w).Encode(approveRunResponse{ //nolint:errcheck
			RunID:   "plr_loop01",
			Status:  "complete",
			Summary: "Root blocker terminated; locks cleared.",
		})
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	initial := newApprovalResponse("plr_loop01", 1, "terminate_connection")

	if err := r.runApprovalLoop(context.Background(), initial); err != nil {
		t.Fatalf("runApprovalLoop: %v", err)
	}
	if !proceedCalled {
		t.Error("proceed endpoint was not called")
	}
}

func TestRunApprovalLoop_MultiStep(t *testing.T) {
	// Server: first proceed returns another pending_approval, second returns complete.
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		callCount++
		if callCount == 1 {
			json.NewEncoder(w).Encode(newApprovalResponse("plr_multi01", 2, "get_blocking_queries")) //nolint:errcheck
			return
		}
		json.NewEncoder(w).Encode(approveRunResponse{RunID: "plr_multi01", Status: "complete", Summary: "done"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	initial := newApprovalResponse("plr_multi01", 1, "terminate_connection")

	if err := r.runApprovalLoop(context.Background(), initial); err != nil {
		t.Fatalf("runApprovalLoop: %v", err)
	}
	if callCount != 2 {
		t.Errorf("proceed called %d times, want 2", callCount)
	}
}

func TestRunApprovalLoop_Denial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(approveRunResponse{RunID: "plr_deny01", Status: "denied"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	initial := newApprovalResponse("plr_deny01", 1, "terminate_connection")

	if err := r.runApprovalLoop(context.Background(), initial); err == nil {
		t.Error("expected error when step is denied, got nil")
	}
}

func TestRunApprovalLoop_PendingWithNoStep(t *testing.T) {
	r := newTestRemediator(t, "http://localhost:19999")
	initial := approveRunResponse{
		RunID:   "plr_noStep",
		Status:  "pending_approval",
		Step:    nil, // missing step — should error immediately
	}

	if err := r.runApprovalLoop(context.Background(), initial); err == nil {
		t.Error("expected error when pending_approval response has no step")
	}
}

func TestTriggerPlaybook_AgentApprove_FullLoop(t *testing.T) {
	// Simulates: resolve → run (pending_approval) → proceed (complete).
	proceedCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
				"playbooks": []map[string]interface{}{{"playbook_id": "pb_approve_test"}},
			})
		case r.URL.Path == "/api/v1/fleet/playbooks/pb_approve_test/run":
			// First call: return pending_approval.
			json.NewEncoder(w).Encode(approveRunResponse{ //nolint:errcheck
				RunID:      "plr_approve01",
				Status:     "pending_approval",
				ApprovalID: "apr_001",
				Step: &approveRunStep{
					Index:  1,
					Agent:  "database",
					Tool:   "terminate_connection",
					Args:   map[string]any{"pid": 5555},
					Reason: "Terminate root blocker",
				},
			})
		case r.URL.Path == "/api/v1/fleet/playbook-runs/plr_approve01/proceed":
			proceedCount++
			json.NewEncoder(w).Encode(approveRunResponse{ //nolint:errcheck
				RunID:   "plr_approve01",
				Status:  "complete",
				Summary: "Blocker terminated.",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if err := r.triggerPlaybook(context.Background(), "pbs_idle_blocker_remediate"); err != nil {
		t.Fatalf("triggerPlaybook: %v", err)
	}
	if proceedCount != 1 {
		t.Errorf("proceed called %d times, want 1", proceedCount)
	}
}

func TestProceedStep_SendsCorrectPayload(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		json.NewEncoder(w).Encode(approveRunResponse{Status: "complete"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	resp, err := r.proceedStep(context.Background(), "plr_payload01", 3)
	if err != nil {
		t.Fatalf("proceedStep: %v", err)
	}
	if resp.Status != "complete" {
		t.Errorf("status = %q, want complete", resp.Status)
	}
	if gotBody["resolution"] != "approved" {
		t.Errorf("resolution = %v, want approved", gotBody["resolution"])
	}
	if gotBody["resolved_by"] != "faulttest" {
		t.Errorf("resolved_by = %v, want faulttest", gotBody["resolved_by"])
	}
	if step, _ := gotBody["step_index"].(float64); int(step) != 3 {
		t.Errorf("step_index = %v, want 3", gotBody["step_index"])
	}
}

func TestRemediate_PlaybookThenRecovery(t *testing.T) {
	// Server handles: resolve (GET list) and run (POST run).
	playbookCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("series_id") == "pbs_test":
			// resolvePlaybookID: return a versioned playbook_id.
			json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
				"playbooks": []map[string]interface{}{{"playbook_id": "pb_resolved_test"}},
			})
		case r.URL.Path == "/api/v1/fleet/playbooks/pb_resolved_test/run":
			playbookCalled = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	r := NewRemediator(&HarnessConfig{
		GatewayURL:    srv.URL,
		GatewayAPIKey: "key",
		ConnStr:       "host=localhost port=5432 dbname=testdb user=postgres password=testpass",
	})

	// pollRecovery will fail (no real DB), so we just check that the playbook
	// was triggered and the error is from the recovery poll, not the trigger.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := r.Remediate(ctx, Failure{
		ID: "test-fault",
		Remediation: RemediationSpec{
			PlaybookID:    "pbs_test",
			VerifySQL:     "SELECT 1",
			VerifyTimeout: "1s",
		},
	})

	if !playbookCalled {
		t.Error("playbook endpoint was not called")
	}
	// Recovery fails (no real DB) — that's expected; just verify the trigger succeeded.
	if result.Err == nil {
		// Only fail if somehow recovery passed without a real DB.
		// In unit test context the poll will always time out.
	}
}
