package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"helpdesk/testing/faultlib"
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

// resolveHandler returns an http.HandlerFunc that handles the GET resolve step
// (returning series_id as the playbook_id) then delegates POST calls to post.
func resolveHandler(post http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			seriesID := r.URL.Query().Get("series_id")
			json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
				"playbooks": []map[string]interface{}{{"playbook_id": seriesID}},
			})
			return
		}
		post(w, r)
	}
}

func TestTriggerPlaybook_Success(t *testing.T) {
	var gotPath, gotAuth, gotPurpose string
	srv := httptest.NewServer(resolveHandler(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotPurpose = r.Header.Get("X-Purpose")
		json.NewEncoder(w).Encode(faultlib.ApproveRunResponse{Status: "complete"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if _, err := r.triggerPlaybook(context.Background(), "pbs_restart", ""); err != nil {
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
		w.Write([]byte("internal error")) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if _, err := r.triggerPlaybook(context.Background(), "pbs_restart", ""); err == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

func TestTriggerPlaybook_NoGateway(t *testing.T) {
	r := NewRemediator(&HarnessConfig{GatewayURL: "", ConnStr: "host=localhost"})
	if _, err := r.triggerPlaybook(context.Background(), "pbs_restart", ""); err == nil {
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
	r := newTestRemediator(t, "http://localhost:9999")
	result := r.Remediate(context.Background(), Failure{
		ID:          "no-action",
		Remediation: RemediationSpec{},
	}, "")
	if result.Err == nil {
		t.Error("expected error when no remediation action is configured")
	}
}

// ── runApprovalLoop / ProceedStep ─────────────────────────────────────────────

func newApprovalResponse(runID string, stepIndex int, tool string) faultlib.ApproveRunResponse {
	return faultlib.ApproveRunResponse{
		RunID:      runID,
		Status:     "pending_approval",
		ApprovalID: "apr_test",
		Step: &faultlib.ApproveRunStep{
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
		json.NewEncoder(w).Encode(faultlib.ApproveRunResponse{ //nolint:errcheck
			RunID: "plr_loop01", Status: "complete", Summary: "Root blocker terminated; locks cleared.",
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
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		callCount++
		if callCount == 1 {
			json.NewEncoder(w).Encode(newApprovalResponse("plr_multi01", 2, "get_blocking_queries")) //nolint:errcheck
			return
		}
		json.NewEncoder(w).Encode(faultlib.ApproveRunResponse{RunID: "plr_multi01", Status: "complete", Summary: "done"}) //nolint:errcheck
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
		json.NewEncoder(w).Encode(faultlib.ApproveRunResponse{RunID: "plr_deny01", Status: "denied"}) //nolint:errcheck
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
	initial := faultlib.ApproveRunResponse{RunID: "plr_noStep", Status: "pending_approval", Step: nil}
	if err := r.runApprovalLoop(context.Background(), initial); err == nil {
		t.Error("expected error when pending_approval response has no step")
	}
}

func TestRunApprovalLoop_EffectiveApprovalModeOverride(t *testing.T) {
	// cfg.ApprovalMode is "manual" (would normally prompt via TTY), but the
	// gateway returns effective_approval_mode="force" due to approval_override_roles
	// clamping. The loop must use the effective mode and auto-approve without
	// calling promptStepApproval. If the override is ignored, promptStepApproval
	// opens /dev/tty, falls back to os.Stdin, reads EOF, and returns an error —
	// so a passing test proves the override is honoured.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(faultlib.ApproveRunResponse{ //nolint:errcheck
			RunID: "plr_eff01", Status: "complete", Summary: "done",
		})
	}))
	defer srv.Close()

	r := NewRemediator(&HarnessConfig{
		GatewayURL:    srv.URL,
		GatewayAPIKey: "test-key",
		ConnStr:       "host=localhost",
		ApprovalMode:  "manual", // would prompt if EffectiveApprovalMode not honoured
	})
	initial := faultlib.ApproveRunResponse{
		RunID:                 "plr_eff01",
		Status:                "pending_approval",
		ApprovalID:            "apr_eff",
		EffectiveApprovalMode: "force", // gateway clamped "manual" → "force"
		Step: &faultlib.ApproveRunStep{
			Index: 1, Agent: "database", Tool: "terminate_connection",
			Args: map[string]any{"pid": 1234}, Reason: "Terminate root blocker",
		},
	}
	if err := r.runApprovalLoop(context.Background(), initial); err != nil {
		t.Fatalf("runApprovalLoop: %v (EffectiveApprovalMode override not honoured?)", err)
	}
}

func TestTriggerPlaybook_AgentApprove_FullLoop(t *testing.T) {
	proceedCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
				"playbooks": []map[string]interface{}{{"playbook_id": "pb_approve_test"}},
			})
		case r.URL.Path == "/api/v1/fleet/playbooks/pb_approve_test/run":
			json.NewEncoder(w).Encode(faultlib.ApproveRunResponse{ //nolint:errcheck
				RunID:      "plr_approve01",
				Status:     "pending_approval",
				ApprovalID: "apr_001",
				Step: &faultlib.ApproveRunStep{
					Index:  1,
					Agent:  "database",
					Tool:   "terminate_connection",
					Args:   map[string]any{"pid": 5555},
					Reason: "Terminate root blocker",
				},
			})
		case r.URL.Path == "/api/v1/fleet/playbook-runs/plr_approve01/proceed":
			proceedCount++
			json.NewEncoder(w).Encode(faultlib.ApproveRunResponse{ //nolint:errcheck
				RunID: "plr_approve01", Status: "complete", Summary: "Blocker terminated.",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if _, err := r.triggerPlaybook(context.Background(), "pbs_lock_chain_remediate", ""); err != nil {
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
		json.NewDecoder(r.Body).Decode(&gotBody)                                  //nolint:errcheck
		json.NewEncoder(w).Encode(faultlib.ApproveRunResponse{Status: "complete"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	resp, err := r.inner.ProceedStep(context.Background(), "plr_payload01", 3, "approved")
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

// ── X-Trace-ID bridge ─────────────────────────────────────────────────────────

func TestTriggerPlaybook_BridgesTraceID(t *testing.T) {
	// Verifies that a trace ID stored under the local ctxKeyFaultTraceID{} key
	// is bridged into faultlib's context slot so RunPlaybook sets X-Trace-ID.
	var gotTraceID string
	srv := httptest.NewServer(resolveHandler(func(w http.ResponseWriter, r *http.Request) {
		gotTraceID = r.Header.Get("X-Trace-ID")
		json.NewEncoder(w).Encode(faultlib.ApproveRunResponse{Status: "complete"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	ctx := context.WithValue(context.Background(), ctxKeyFaultTraceID{}, "trace-rem-bridge")
	if _, err := r.triggerPlaybook(ctx, "pbs_test", ""); err != nil {
		t.Fatalf("triggerPlaybook: %v", err)
	}
	if gotTraceID != "trace-rem-bridge" {
		t.Errorf("X-Trace-ID = %q, want trace-rem-bridge", gotTraceID)
	}
}

// ── prior_run_id threading ────────────────────────────────────────────────────

func TestTriggerPlaybook_SendsPriorRunID(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(resolveHandler(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)                                  //nolint:errcheck
		json.NewEncoder(w).Encode(faultlib.ApproveRunResponse{Status: "complete"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if _, err := r.triggerPlaybook(context.Background(), "pbs_test", "plr_triage01"); err != nil {
		t.Fatalf("triggerPlaybook: %v", err)
	}
	if gotBody["prior_run_id"] != "plr_triage01" {
		t.Errorf("prior_run_id = %v, want plr_triage01", gotBody["prior_run_id"])
	}
}

func TestTriggerPlaybook_OmitsPriorRunIDWhenEmpty(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(resolveHandler(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)                                  //nolint:errcheck
		json.NewEncoder(w).Encode(faultlib.ApproveRunResponse{Status: "complete"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if _, err := r.triggerPlaybook(context.Background(), "pbs_test", ""); err != nil {
		t.Fatalf("triggerPlaybook: %v", err)
	}
	if _, present := gotBody["prior_run_id"]; present {
		t.Error("prior_run_id should not be present in request body when empty")
	}
}

func TestRemediate_PlaybookThenRecovery(t *testing.T) {
	playbookCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("series_id") == "pbs_test":
			json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
				"playbooks": []map[string]interface{}{{"playbook_id": "pb_resolved_test"}},
			})
		case r.URL.Path == "/api/v1/fleet/playbooks/pb_resolved_test/run":
			playbookCalled = true
			json.NewEncoder(w).Encode(faultlib.ApproveRunResponse{Status: "complete"}) //nolint:errcheck
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := r.Remediate(ctx, Failure{
		ID: "test-fault",
		Remediation: RemediationSpec{
			PlaybookID:    "pbs_test",
			VerifySQL:     "SELECT 1",
			VerifyTimeout: "1s",
		},
	}, "")

	if !playbookCalled {
		t.Error("playbook endpoint was not called")
	}
	_ = result // pollRecovery will fail without a real DB — that's expected
}

// ── postFeedback remediation/at_gate tests ────────────────────────────────

func TestPostFeedback_RemediationAtGate_Approved(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	v := true
	r.postFeedback(context.Background(), "plr_abc123", "remediation", "at_gate", &v, "", "")

	wantPath := "/api/v1/fleet/playbook-runs/plr_abc123/feedback"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if gotBody["feedback_type"] != "remediation" {
		t.Errorf("feedback_type = %v, want remediation", gotBody["feedback_type"])
	}
	if gotBody["feedback_time"] != "at_gate" {
		t.Errorf("feedback_time = %v, want at_gate", gotBody["feedback_time"])
	}
	if gotBody["verdict_correct"] != true {
		t.Errorf("verdict_correct = %v, want true", gotBody["verdict_correct"])
	}
}

func TestPostFeedback_RemediationAtGate_Denied_WithNotes(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	v := false
	r.postFeedback(context.Background(), "plr_abc123", "remediation", "at_gate", &v, "plan would terminate active sessions", "")

	if gotBody["verdict_correct"] != false {
		t.Errorf("verdict_correct = %v, want false", gotBody["verdict_correct"])
	}
	if gotBody["verdict_notes"] != "plan would terminate active sessions" {
		t.Errorf("verdict_notes = %v, want notes string", gotBody["verdict_notes"])
	}
	if gotBody["feedback_time"] != "at_gate" {
		t.Errorf("feedback_time = %v, want at_gate", gotBody["feedback_time"])
	}
}

func TestPostFeedback_RemediationAtGate_NoGateway(t *testing.T) {
	// With no gateway configured, postFeedback must be a silent no-op (no panic).
	r := NewRemediator(&HarnessConfig{GatewayURL: "", ConnStr: "host=localhost"})
	v := true
	r.postFeedback(context.Background(), "plr_abc123", "remediation", "at_gate", &v, "", "")
}
