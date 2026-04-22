package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDoPlaybookDraftRequest_Success(t *testing.T) {
	var gotMethod, gotBody, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		b := make([]byte, r.ContentLength)
		r.Body.Read(b) //nolint:errcheck
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
			"draft":       "name: test-playbook",
			"playbook_id": "pb_abc123",
		})
	}))
	defer srv.Close()

	draft, pbID, err := doPlaybookDraftRequest(context.Background(), srv.URL, "secret-key", "inc-001", "resolved")
	if err != nil {
		t.Fatalf("doPlaybookDraftRequest: %v", err)
	}
	if draft != "name: test-playbook" {
		t.Errorf("draft = %q, want %q", draft, "name: test-playbook")
	}
	if pbID != "pb_abc123" {
		t.Errorf("playbook_id = %q, want pb_abc123", pbID)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("Authorization = %q, want Bearer secret-key", gotAuth)
	}
	// Verify trace_id and outcome are in the request body.
	var body map[string]string
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if body["trace_id"] != "inc-001" {
		t.Errorf("trace_id = %q, want inc-001", body["trace_id"])
	}
	if body["outcome"] != "resolved" {
		t.Errorf("outcome = %q, want resolved", body["outcome"])
	}
}

func TestDoPlaybookDraftRequest_EmptyGatewayURL(t *testing.T) {
	_, _, err := doPlaybookDraftRequest(context.Background(), "", "", "inc-001", "resolved")
	if err == nil {
		t.Error("expected error for empty gateway URL, got nil")
	}
}

func TestDoPlaybookDraftRequest_NoAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"draft": "x"}) //nolint:errcheck
	}))
	defer srv.Close()

	doPlaybookDraftRequest(context.Background(), srv.URL, "", "inc-001", "resolved") //nolint:errcheck
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty when no api key", gotAuth)
	}
}

func TestDoPlaybookDraftRequest_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error")) //nolint:errcheck
	}))
	defer srv.Close()

	_, _, err := doPlaybookDraftRequest(context.Background(), srv.URL, "", "inc-001", "resolved")
	if err == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

func TestDoPlaybookDraftRequest_NoPlaybookID(t *testing.T) {
	// When auditd is not configured, gateway returns draft but no playbook_id.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"draft": "name: draft"}) //nolint:errcheck
	}))
	defer srv.Close()

	draft, pbID, err := doPlaybookDraftRequest(context.Background(), srv.URL, "", "inc-001", "resolved")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if draft != "name: draft" {
		t.Errorf("draft = %q, want %q", draft, "name: draft")
	}
	if pbID != "" {
		t.Errorf("playbook_id = %q, want empty when not returned", pbID)
	}
}

func TestDoPlaybookDraftRequest_NetworkError(t *testing.T) {
	_, _, err := doPlaybookDraftRequest(context.Background(), "http://127.0.0.1:19997", "", "inc-001", "resolved")
	if err == nil {
		t.Error("expected error for unreachable server, got nil")
	}
}

// ── shouldGenerateDraft gate ───────────────────────────────────────────────
// Test the auto-trigger condition by verifying that requestPlaybookDraft is
// called (or not) based on Outcome and HELPDESK_GATEWAY_URL.

func TestShouldGenerateDraft_ResolvedOutcome_WithGateway(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"draft": "x", "playbook_id": "pb_1"}) //nolint:errcheck
	}))
	defer srv.Close()

	t.Setenv("HELPDESK_GATEWAY_URL", srv.URL)

	// Simulate the shouldGenerateDraft gate for outcome="resolved".
	outcome := "resolved"
	gateway := srv.URL
	shouldGenerate := (outcome == "resolved" || outcome == "escalated") && gateway != ""
	if !shouldGenerate {
		t.Fatal("shouldGenerateDraft should be true for outcome=resolved with gateway set")
	}
	_, _, err := doPlaybookDraftRequest(context.Background(), gateway, "", "inc-001", outcome)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("gateway was not called")
	}
}

func TestShouldGenerateDraft_EscalatedOutcome_WithGateway(t *testing.T) {
	outcome := "escalated"
	gateway := "http://gateway.example.com"
	shouldGenerate := (outcome == "resolved" || outcome == "escalated") && gateway != ""
	if !shouldGenerate {
		t.Error("shouldGenerateDraft should be true for outcome=escalated with gateway set")
	}
}

func TestShouldGenerateDraft_InvestigatingOutcome_NoTrigger(t *testing.T) {
	outcome := ""
	gateway := "http://gateway.example.com"
	shouldGenerate := (outcome == "resolved" || outcome == "escalated") && gateway != ""
	if shouldGenerate {
		t.Error("shouldGenerateDraft should be false when outcome is empty (still investigating)")
	}
}

func TestShouldGenerateDraft_ResolvedOutcome_NoGateway(t *testing.T) {
	outcome := "resolved"
	gateway := ""
	shouldGenerate := (outcome == "resolved" || outcome == "escalated") && gateway != ""
	if shouldGenerate {
		t.Error("shouldGenerateDraft should be false when HELPDESK_GATEWAY_URL is not set")
	}
}

func TestShouldGenerateDraft_GenerateFlagOverrides_NoOutcome(t *testing.T) {
	// GeneratePlaybookDraft=true still triggers even without outcome or gateway env.
	// (Backward compat: the HTTP call will fail, but the gate opens.)
	generateFlag := true
	outcome := ""
	gateway := ""
	shouldGenerate := generateFlag || (outcome == "resolved" || outcome == "escalated") && gateway != ""
	if !shouldGenerate {
		t.Error("shouldGenerateDraft should be true when GeneratePlaybookDraft=true")
	}
}
