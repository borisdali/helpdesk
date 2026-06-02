package faultlib

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestRunPlaybook_UsesAgentConnStr(t *testing.T) {
	var gotBody map[string]interface{}
	srv := resolveServer(t, "pb_test", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ApproveRunResponse{Status: "complete"}) //nolint:errcheck
	})
	defer srv.Close()

	r := NewRemediator(&HarnessConfig{
		GatewayURL:   srv.URL,
		GatewayAPIKey: "test-key",
		ConnStr:      "host=primary",
		AgentConnStr: "host=replica",
	})
	if _, err := r.RunPlaybook(context.Background(), "pbs_test", ""); err != nil {
		t.Fatalf("RunPlaybook: %v", err)
	}
	if gotBody["connection_string"] != "host=replica" {
		t.Errorf("connection_string = %v, want host=replica (AgentConnStr takes precedence)", gotBody["connection_string"])
	}
}

func TestRunPlaybook_FallsBackToConnStr(t *testing.T) {
	var gotBody map[string]interface{}
	srv := resolveServer(t, "pb_test", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ApproveRunResponse{Status: "complete"}) //nolint:errcheck
	})
	defer srv.Close()

	r := NewRemediator(&HarnessConfig{
		GatewayURL:   srv.URL,
		GatewayAPIKey: "test-key",
		ConnStr:      "host=primary",
		// AgentConnStr intentionally empty
	})
	if _, err := r.RunPlaybook(context.Background(), "pbs_test", ""); err != nil {
		t.Fatalf("RunPlaybook: %v", err)
	}
	if gotBody["connection_string"] != "host=primary" {
		t.Errorf("connection_string = %v, want host=primary (ConnStr fallback)", gotBody["connection_string"])
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

// ── ProceedEscalation ─────────────────────────────────────────────────────────

func TestProceedEscalation_SendsCorrectPayload(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := json.Marshal(map[string]any{})
		_ = body
		raw := make([]byte, r.ContentLength)
		r.Body.Read(raw) //nolint:errcheck
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ApproveRunResponse{Status: "complete"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	req := ProceedEscalationRequest{
		Resolution:       "approved",
		ResolvedBy:       "ops-alice",
		ApprovalMode:     "review",
		ConnectionString: "host=localhost port=5432 dbname=testdb user=postgres",
	}
	resp, err := r.ProceedEscalation(context.Background(), "plr_gate01", req)
	if err != nil {
		t.Fatalf("ProceedEscalation: %v", err)
	}
	if resp.Status != "complete" {
		t.Errorf("status = %q, want complete", resp.Status)
	}
	if gotPath != "/api/v1/fleet/playbook-runs/plr_gate01/proceed-escalation" {
		t.Errorf("path = %q, want proceed-escalation path", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(gotBody), &decoded); err != nil {
		t.Fatalf("decoding request body: %v", err)
	}
	if decoded["resolution"] != "approved" {
		t.Errorf("resolution = %v, want approved", decoded["resolution"])
	}
	if decoded["approval_mode"] != "review" {
		t.Errorf("approval_mode = %v, want review", decoded["approval_mode"])
	}
}

func TestProceedEscalation_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"run not in gate_pending state"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if _, err := r.ProceedEscalation(context.Background(), "plr_bad", ProceedEscalationRequest{Resolution: "approved"}); err == nil {
		t.Error("expected error for non-2xx response, got nil")
	}
}

// ── RunGateLoop ───────────────────────────────────────────────────────────────

func TestRunGateLoop_AutoApprovesAndComplete(t *testing.T) {
	var gotBody map[string]any
	proceedCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proceedCalled = true
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ApproveRunResponse{Status: "complete", Summary: "Root blocker terminated."}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	gate := &ApproveRunResponse{
		RunID:            "plr_gate01",
		Status:           "pending_gate",
		EscalationTarget: "pbs_lock_chain_remediate",
	}
	if err := r.RunGateLoop(context.Background(), gate); err != nil {
		t.Fatalf("RunGateLoop: %v", err)
	}
	if !proceedCalled {
		t.Error("proceed-escalation endpoint was not called")
	}
	if gotBody["resolution"] != "approved" {
		t.Errorf("resolution = %v, want approved", gotBody["resolution"])
	}
	if gotBody["approval_mode"] != "auto" {
		t.Errorf("approval_mode = %v, want auto (default when HarnessConfig.ApprovalMode is empty)", gotBody["approval_mode"])
	}
}

func TestRunGateLoop_DrivesApprovalLoopOnPendingApproval(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		callCount++
		if callCount == 1 {
			// First call: proceed-escalation returns pending_approval
			json.NewEncoder(w).Encode(ApproveRunResponse{ //nolint:errcheck
				RunID:      "plr_gate01",
				Status:     "pending_approval",
				ApprovalID: "apr_test",
				Step: &ApproveRunStep{
					Index: 1, Agent: "database", Tool: "terminate_connection",
					Args: map[string]any{"pid": 1234}, Reason: "Terminate root blocker",
				},
			})
			return
		}
		// Second call: proceed-step returns complete
		json.NewEncoder(w).Encode(ApproveRunResponse{RunID: "plr_gate01", Status: "complete"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	gate := &ApproveRunResponse{
		RunID:            "plr_gate01",
		Status:           "pending_gate",
		EscalationTarget: "pbs_lock_chain_remediate",
	}
	if err := r.RunGateLoop(context.Background(), gate); err != nil {
		t.Fatalf("RunGateLoop: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 server calls (proceed-escalation + proceed-step), got %d", callCount)
	}
}

// TestTriggerPlaybook_PendingGateDispatchesToRunGateLoop verifies that when the
// playbook run endpoint returns pending_gate, triggerPlaybook calls RunGateLoop
// which auto-approves by calling proceed-escalation.
func TestTriggerPlaybook_PendingGateDispatchesToRunGateLoop(t *testing.T) {
	proceedCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			// resolvePlaybookID: GET /api/v1/fleet/playbooks?series_id=...
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"playbooks": []map[string]any{{"playbook_id": "pb_triage01"}},
			})
		case r.URL.Path == "/api/v1/fleet/playbooks/pb_triage01/run":
			// RunPlaybook: returns pending_gate
			json.NewEncoder(w).Encode(ApproveRunResponse{ //nolint:errcheck
				RunID:            "plr_gate01",
				Status:           "pending_gate",
				EscalationTarget: "pbs_lock_chain_remediate",
				EscalationFindings: "Lock chain detected.",
			})
		case strings.Contains(r.URL.Path, "/proceed-escalation"):
			// RunGateLoop: proceed-escalation returns complete
			proceedCalled = true
			json.NewEncoder(w).Encode(ApproveRunResponse{Status: "complete"}) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if err := r.triggerPlaybook(context.Background(), "pbs_lock_chain_triage", ""); err != nil {
		t.Fatalf("triggerPlaybook: %v", err)
	}
	if !proceedCalled {
		t.Error("proceed-escalation was not called for pending_gate response")
	}
}

// newFastRemediator creates a Remediator with a short poll interval for tests.
func newFastRemediator(serverURL string, extra ...func(*HarnessConfig)) *Remediator {
	cfg := &HarnessConfig{
		GatewayURL:          serverURL,
		GatewayAPIKey:       "test-key",
		EmitAndWait:         true,
		GatewayPollInterval: 10 * time.Millisecond,
	}
	for _, fn := range extra {
		fn(cfg)
	}
	return NewRemediator(cfg)
}

// ---- WaitForGateResolution tests ----

func TestWaitForGateResolution_PollsUntilResolved(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		callCount++
		outcome := "gate_pending"
		if callCount >= 3 {
			outcome = "escalated"
		}
		json.NewEncoder(w).Encode(map[string]string{"outcome": outcome}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newFastRemediator(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := r.WaitForGateResolution(ctx, "plr_poll01")
	if err != nil {
		t.Fatalf("WaitForGateResolution: %v", err)
	}
	if resp.Status != "escalated" {
		t.Errorf("status = %q, want escalated", resp.Status)
	}
	if resp.RunID != "plr_poll01" {
		t.Errorf("run_id = %q, want plr_poll01", resp.RunID)
	}
	if callCount < 3 {
		t.Errorf("expected at least 3 poll calls (2 gate_pending + 1 escalated), got %d", callCount)
	}
}

func TestWaitForGateResolution_AbandonedReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"outcome": "abandoned"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newFastRemediator(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := r.WaitForGateResolution(ctx, "plr_abandoned")
	if err == nil {
		t.Fatal("expected error for abandoned gate, got nil")
	}
	if !strings.Contains(err.Error(), "abandoned") {
		t.Errorf("error = %q, want it to mention 'abandoned'", err.Error())
	}
}

func TestWaitForGateResolution_ContextCancelled(t *testing.T) {
	// ctx already cancelled — should return immediately without any HTTP calls.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("HTTP request was made after context cancellation")
	}))
	defer srv.Close()

	r := newFastRemediator(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the first poll tick

	_, err := r.WaitForGateResolution(ctx, "plr_cancel")
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
}

// ---- RunGateLoop emit-and-wait tests ----

func TestRunGateLoop_EmitAndWait_PollsInsteadOfAutoApprove(t *testing.T) {
	proceedCalled := false
	var lastPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		lastPath = r.URL.Path
		if strings.Contains(r.URL.Path, "proceed-escalation") {
			proceedCalled = true
		}
		json.NewEncoder(w).Encode(map[string]string{"outcome": "escalated"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newFastRemediator(srv.URL)
	gate := &ApproveRunResponse{
		RunID:            "plr_ew01",
		Status:           "pending_gate",
		EscalationTarget: "pbs_vacuum_remediate",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := r.RunGateLoop(ctx, gate); err != nil {
		t.Fatalf("RunGateLoop: %v", err)
	}
	// Emit-and-wait must not call proceed-escalation directly.
	if proceedCalled {
		t.Error("proceed-escalation was called — emit-and-wait should poll, not auto-approve")
	}
	// Must have called the poll endpoint for the run ID.
	if !strings.Contains(lastPath, "plr_ew01") {
		t.Errorf("poll path %q does not contain run ID plr_ew01", lastPath)
	}
}

func TestRunGateLoop_HeadlessAutoApprove_WhenEmitAndWaitFalse(t *testing.T) {
	proceedCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proceedCalled = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ApproveRunResponse{Status: "complete"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := NewRemediator(&HarnessConfig{
		GatewayURL:  srv.URL,
		EmitAndWait: false, // headless: auto-approve immediately
	})
	gate := &ApproveRunResponse{RunID: "plr_headless", Status: "pending_gate"}

	if err := r.RunGateLoop(context.Background(), gate); err != nil {
		t.Fatalf("RunGateLoop: %v", err)
	}
	if !proceedCalled {
		t.Error("expected proceed-escalation to be called in headless auto-approve mode")
	}
}
