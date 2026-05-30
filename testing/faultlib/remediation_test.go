package faultlib

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestRemediator(t *testing.T, serverURL string) *Remediator {
	t.Helper()
	return NewRemediator(&HarnessConfig{
		GatewayURL:    serverURL,
		GatewayAPIKey: "test-key",
		ConnStr:       "host=localhost port=5432 dbname=testdb user=postgres",
	})
}

// resolveServer returns a handler that serves the GET resolve step, mapping
// series_id → playbookID, then delegates POST calls to postHandler.
func resolveServer(t *testing.T, playbookID string, postHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"playbooks": []map[string]any{{"playbook_id": playbookID}},
			})
			return
		}
		postHandler(w, r)
	}))
}

// ── resolvePlaybookID ─────────────────────────────────────────────────────────

func TestResolvePlaybookID_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("series_id") != "pbs_db_restart_triage" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"playbooks": []map[string]any{
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"playbooks": []any{}}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if _, err := r.resolvePlaybookID(context.Background(), "pbs_missing"); err == nil {
		t.Error("expected error for empty playbooks list, got nil")
	}
}

func TestResolvePlaybookID_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if _, err := r.resolvePlaybookID(context.Background(), "pbs_test"); err == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

func TestResolvePlaybookID_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"playbooks": []map[string]any{{"playbook_id": "pb_abc"}},
		})
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	r.resolvePlaybookID(context.Background(), "pbs_test") //nolint:errcheck
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
}

// ── triggerPlaybook / RunPlaybook ─────────────────────────────────────────────

func TestTriggerPlaybook_Success(t *testing.T) {
	var gotPath, gotAuth, gotPurpose string
	srv := resolveServer(t, "pb_restart", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotPurpose = r.Header.Get("X-Purpose")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ApproveRunResponse{Status: "complete"}) //nolint:errcheck
	})
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if err := r.triggerPlaybook(context.Background(), "pbs_restart", ""); err != nil {
		t.Fatalf("triggerPlaybook: %v", err)
	}
	if gotPath != "/api/v1/fleet/playbooks/pb_restart/run" {
		t.Errorf("path = %q, want /api/v1/fleet/playbooks/pb_restart/run", gotPath)
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
		w.Write([]byte("internal error")) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if err := r.triggerPlaybook(context.Background(), "pbs_restart", ""); err == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

func TestTriggerPlaybook_NoGateway(t *testing.T) {
	r := NewRemediator(&HarnessConfig{GatewayURL: "", ConnStr: "host=localhost"})
	if err := r.triggerPlaybook(context.Background(), "pbs_restart", ""); err == nil {
		t.Error("expected error when GatewayURL is empty, got nil")
	}
}

func TestTriggerPlaybook_SendsXTraceID(t *testing.T) {
	var gotTraceID string
	srv := resolveServer(t, "pb_test", func(w http.ResponseWriter, r *http.Request) {
		gotTraceID = r.Header.Get("X-Trace-ID")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ApproveRunResponse{Status: "complete"}) //nolint:errcheck
	})
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	ctx := WithFaultTraceID(context.Background(), "trace-rem01")
	if err := r.triggerPlaybook(ctx, "pbs_test", ""); err != nil {
		t.Fatalf("triggerPlaybook: %v", err)
	}
	if gotTraceID != "trace-rem01" {
		t.Errorf("X-Trace-ID = %q, want trace-rem01", gotTraceID)
	}
}

// ── prior_run_id threading ────────────────────────────────────────────────────

func TestTriggerPlaybook_SendsPriorRunID(t *testing.T) {
	var gotBody map[string]interface{}
	srv := resolveServer(t, "pb_test", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ApproveRunResponse{Status: "complete"}) //nolint:errcheck
	})
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if err := r.triggerPlaybook(context.Background(), "pbs_test", "plr_triage01"); err != nil {
		t.Fatalf("triggerPlaybook: %v", err)
	}
	if gotBody["prior_run_id"] != "plr_triage01" {
		t.Errorf("prior_run_id = %v, want plr_triage01", gotBody["prior_run_id"])
	}
}

func TestTriggerPlaybook_OmitsPriorRunIDWhenEmpty(t *testing.T) {
	var gotBody map[string]interface{}
	srv := resolveServer(t, "pb_test", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ApproveRunResponse{Status: "complete"}) //nolint:errcheck
	})
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if err := r.triggerPlaybook(context.Background(), "pbs_test", ""); err != nil {
		t.Fatalf("triggerPlaybook: %v", err)
	}
	if _, present := gotBody["prior_run_id"]; present {
		t.Error("prior_run_id should not be present in request body when empty")
	}
}

// ── triggerAgent ──────────────────────────────────────────────────────────────

func TestTriggerAgent_Success(t *testing.T) {
	var gotPath, gotAuth, gotPurpose string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotPurpose = r.Header.Get("X-Purpose")
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
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	if gotPurpose != "remediation" {
		t.Errorf("X-Purpose = %q, want remediation", gotPurpose)
	}
}

func TestTriggerAgent_NoGateway(t *testing.T) {
	r := NewRemediator(&HarnessConfig{GatewayURL: "", ConnStr: "host=localhost"})
	if err := r.triggerAgent(context.Background(), "database", "restart"); err == nil {
		t.Error("expected error when GatewayURL is empty, got nil")
	}
}

// ── ProceedStep ───────────────────────────────────────────────────────────────

func TestProceedStep_SendsCorrectPayload(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		json.NewEncoder(w).Encode(ApproveRunResponse{Status: "complete"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	resp, err := r.ProceedStep(context.Background(), "plr_payload01", 3, "approved")
	if err != nil {
		t.Fatalf("ProceedStep: %v", err)
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

// ── runApprovalLoop (headless) ────────────────────────────────────────────────

func newApprovalResponse(runID string, stepIndex int, tool string) ApproveRunResponse {
	return ApproveRunResponse{
		RunID:      runID,
		Status:     "pending_approval",
		ApprovalID: "apr_test",
		Step: &ApproveRunStep{
			Index:  stepIndex,
			Agent:  "database",
			Tool:   tool,
			Args:   map[string]any{"pid": 1234},
			Reason: "Terminate root blocker",
		},
	}
}

func TestRunApprovalLoop_SingleStepComplete(t *testing.T) {
	proceedCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		proceedCalled = true
		json.NewEncoder(w).Encode(ApproveRunResponse{ //nolint:errcheck
			RunID: "plr_loop01", Status: "complete", Summary: "Root blocker terminated.",
		})
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	initial := newApprovalResponse("plr_loop01", 1, "terminate_connection")
	if err := r.runApprovalLoop(context.Background(), &initial); err != nil {
		t.Fatalf("runApprovalLoop: %v", err)
	}
	if !proceedCalled {
		t.Error("proceed endpoint was not called")
	}
}

func TestRunApprovalLoop_MultiStep(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		callCount++
		if callCount == 1 {
			step2 := newApprovalResponse("plr_multi01", 2, "get_blocking_queries")
			json.NewEncoder(w).Encode(step2) //nolint:errcheck
			return
		}
		json.NewEncoder(w).Encode(ApproveRunResponse{RunID: "plr_multi01", Status: "complete"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	initial := newApprovalResponse("plr_multi01", 1, "terminate_connection")
	if err := r.runApprovalLoop(context.Background(), &initial); err != nil {
		t.Fatalf("runApprovalLoop: %v", err)
	}
	if callCount != 2 {
		t.Errorf("proceed called %d times, want 2", callCount)
	}
}

func TestRunApprovalLoop_Denial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ApproveRunResponse{RunID: "plr_deny01", Status: "denied"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	initial := newApprovalResponse("plr_deny01", 1, "terminate_connection")
	if err := r.runApprovalLoop(context.Background(), &initial); err == nil {
		t.Error("expected error when step is denied, got nil")
	}
}

func TestRunApprovalLoop_PendingWithNoStep(t *testing.T) {
	r := newTestRemediator(t, "http://localhost:19999")
	initial := ApproveRunResponse{RunID: "plr_noStep", Status: "pending_approval", Step: nil}
	if err := r.runApprovalLoop(context.Background(), &initial); err == nil {
		t.Error("expected error when pending_approval response has no step")
	}
}

// ── Remediate ─────────────────────────────────────────────────────────────────

func TestRemediate_NoAction(t *testing.T) {
	r := newTestRemediator(t, "http://localhost:9999")
	result := r.Remediate(context.Background(), Failure{
		ID:          "no-action",
		Remediation: RemediationSpec{},
	}, "")
	if result.Err == nil {
		t.Error("expected error when no remediation action is configured")
	}
	if result.Method != "none" {
		t.Errorf("method = %q, want none", result.Method)
	}
}
