package agentutil

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestPlaybookDraft_Success(t *testing.T) {
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

	draft, pbID, err := RequestPlaybookDraft(context.Background(), srv.URL, "secret-key", "trace-001", "resolved", "")
	if err != nil {
		t.Fatalf("RequestPlaybookDraft: %v", err)
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

	var body map[string]string
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if body["trace_id"] != "trace-001" {
		t.Errorf("trace_id = %q, want trace-001", body["trace_id"])
	}
	if body["outcome"] != "resolved" {
		t.Errorf("outcome = %q, want resolved", body["outcome"])
	}
	if _, ok := body["series_id"]; ok {
		t.Errorf("series_id should be omitted from body when empty, got %q", body["series_id"])
	}
}

func TestRequestPlaybookDraft_IncludesSeriesIDWhenSet(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		r.Body.Read(b) //nolint:errcheck
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"playbook_id": "pb_improved"}) //nolint:errcheck
	}))
	defer srv.Close()

	_, pbID, err := RequestPlaybookDraft(context.Background(), srv.URL, "", "trace-002", "resolved", "pbs_vacuum_triage")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pbID != "pb_improved" {
		t.Errorf("playbook_id = %q, want pb_improved", pbID)
	}

	var body map[string]string
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if body["series_id"] != "pbs_vacuum_triage" {
		t.Errorf("series_id = %q, want pbs_vacuum_triage", body["series_id"])
	}
}

func TestRequestPlaybookDraft_EmptyGatewayURL(t *testing.T) {
	_, _, err := RequestPlaybookDraft(context.Background(), "", "", "trace-001", "resolved", "")
	if err == nil {
		t.Error("expected error for empty gateway URL, got nil")
	}
}

func TestRequestPlaybookDraft_NoAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"draft": "x"}) //nolint:errcheck
	}))
	defer srv.Close()

	RequestPlaybookDraft(context.Background(), srv.URL, "", "trace-001", "resolved", "") //nolint:errcheck
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty when no api key", gotAuth)
	}
}

func TestRequestPlaybookDraft_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error")) //nolint:errcheck
	}))
	defer srv.Close()

	_, _, err := RequestPlaybookDraft(context.Background(), srv.URL, "", "trace-001", "resolved", "")
	if err == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

func TestRequestPlaybookDraft_NoPlaybookID(t *testing.T) {
	// When auditd is not configured, gateway returns draft but no playbook_id.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"draft": "name: draft"}) //nolint:errcheck
	}))
	defer srv.Close()

	draft, pbID, err := RequestPlaybookDraft(context.Background(), srv.URL, "", "trace-001", "resolved", "")
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

func TestRequestPlaybookDraft_NetworkError(t *testing.T) {
	_, _, err := RequestPlaybookDraft(context.Background(), "http://127.0.0.1:19997", "", "trace-001", "resolved", "")
	if err == nil {
		t.Error("expected error for unreachable server, got nil")
	}
}
