package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── postNotify ────────────────────────────────────────────────────────────

func TestPostNotify_Success(t *testing.T) {
	var gotMethod, gotContentType string
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	report := BuildReport("run-notify", []EvalResult{
		{FailureID: "f1", FailureName: "Max connections", Category: "database", Score: 1.0, Passed: true},
	})
	postNotify(srv.URL, report)

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	// Body should be valid JSON containing the report.
	var decoded Report
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("request body was not valid JSON: %v", err)
	}
	if decoded.ID != "run-notify" {
		t.Errorf("report.id = %q, want run-notify", decoded.ID)
	}
	if len(decoded.Results) != 1 {
		t.Errorf("report.results length = %d, want 1", len(decoded.Results))
	}
}

func TestPostNotify_NonOKStatus(t *testing.T) {
	// A non-2xx status should be logged but must not panic or return an error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	// postNotify should return silently (no panic, no fatal).
	postNotify(srv.URL, BuildReport("run-fail", nil))
}

func TestPostNotify_NetworkError(t *testing.T) {
	// Unreachable server — should log and return silently.
	postNotify("http://127.0.0.1:19998", BuildReport("run-net", nil))
}

func TestPostNotify_ReportFieldsPresent(t *testing.T) {
	// Verify that summary fields survive the JSON round-trip.
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	report := BuildReport("run-fields", []EvalResult{
		{FailureID: "f1", Category: "database", Passed: true, Score: 0.9},
		{FailureID: "f2", Category: "database", Passed: false, Score: 0.4},
	})
	postNotify(srv.URL, report)

	var decoded Report
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Summary.Total != 2 {
		t.Errorf("summary.total = %d, want 2", decoded.Summary.Total)
	}
	if decoded.Summary.Passed != 1 {
		t.Errorf("summary.passed = %d, want 1", decoded.Summary.Passed)
	}
	if decoded.Summary.PassRate != 0.5 {
		t.Errorf("summary.pass_rate = %.2f, want 0.50", decoded.Summary.PassRate)
	}
}

// ── requestVaultDraft ─────────────────────────────────────────────────────

func TestRequestVaultDraft_Success(t *testing.T) {
	var gotMethod, gotAuth string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"draft":"name: test","playbook_id":"pb_vault_001"}`))
	}))
	defer srv.Close()

	cfg := &HarnessConfig{GatewayURL: srv.URL, GatewayAPIKey: "vault-key"}
	pbID, err := requestVaultDraft(context.Background(), cfg, "faulttest-abc-db-max-connections", "resolved")
	if err != nil {
		t.Fatalf("requestVaultDraft: %v", err)
	}
	if pbID != "pb_vault_001" {
		t.Errorf("playbook_id = %q, want pb_vault_001", pbID)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotAuth != "Bearer vault-key" {
		t.Errorf("Authorization = %q, want Bearer vault-key", gotAuth)
	}
	// Verify trace_id and outcome are in the body.
	var body map[string]string
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body["trace_id"] != "faulttest-abc-db-max-connections" {
		t.Errorf("trace_id = %q", body["trace_id"])
	}
	if body["outcome"] != "resolved" {
		t.Errorf("outcome = %q, want resolved", body["outcome"])
	}
}

func TestRequestVaultDraft_NoPlaybookID(t *testing.T) {
	// Gateway returns draft but no playbook_id (auditd not configured).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"draft":"name: test"}`))
	}))
	defer srv.Close()

	cfg := &HarnessConfig{GatewayURL: srv.URL}
	pbID, err := requestVaultDraft(context.Background(), cfg, "trace-1", "resolved")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pbID != "" {
		t.Errorf("playbook_id = %q, want empty when not returned", pbID)
	}
}

func TestRequestVaultDraft_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("error"))
	}))
	defer srv.Close()

	cfg := &HarnessConfig{GatewayURL: srv.URL}
	_, err := requestVaultDraft(context.Background(), cfg, "trace-1", "resolved")
	if err == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

func TestRequestVaultDraft_NetworkError(t *testing.T) {
	cfg := &HarnessConfig{GatewayURL: "http://127.0.0.1:19996"}
	_, err := requestVaultDraft(context.Background(), cfg, "trace-1", "resolved")
	if err == nil {
		t.Error("expected error for unreachable server, got nil")
	}
}

func TestRequestVaultDraft_URLPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	cfg := &HarnessConfig{GatewayURL: srv.URL}
	requestVaultDraft(context.Background(), cfg, "t", "resolved") //nolint:errcheck
	if gotPath != "/api/v1/fleet/playbooks/from-trace" {
		t.Errorf("path = %q, want /api/v1/fleet/playbooks/from-trace", gotPath)
	}
}

// ── registerAutoDBWithGateway ─────────────────────────────────────────────

func TestRegisterAutoDBWithGateway_PostsCorrectPayload(t *testing.T) {
	var gotPath, gotMethod, gotAuth string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	connStr := "host=127.0.0.1 port=61195 dbname=faulttest user=postgres password=faulttest sslmode=disable"
	if err := registerAutoDBWithGateway(srv.URL, "test-key", connStr); err != nil {
		t.Fatalf("registerAutoDBWithGateway: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/admin/infra/register-db" {
		t.Errorf("path = %q, want /api/v1/admin/infra/register-db", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	var payload map[string]any
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if payload["server_id"] != "faulttest-auto-61195" {
		t.Errorf("server_id = %v, want faulttest-auto-61195", payload["server_id"])
	}
	if payload["connection_string"] != connStr {
		t.Errorf("connection_string = %v", payload["connection_string"])
	}
	tags, _ := payload["tags"].([]any)
	if len(tags) != 2 {
		t.Errorf("tags = %v, want [chaos test]", tags)
	}
}

func TestRegisterAutoDBWithGateway_ServerIDFromPort(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Port extracted regardless of field order.
	registerAutoDBWithGateway(srv.URL, "", "dbname=faulttest port=55000 host=127.0.0.1 user=postgres") //nolint:errcheck
	var payload map[string]any
	json.Unmarshal(gotBody, &payload) //nolint:errcheck
	if payload["server_id"] != "faulttest-auto-55000" {
		t.Errorf("server_id = %v, want faulttest-auto-55000", payload["server_id"])
	}
}

func TestRegisterAutoDBWithGateway_NoPort_FallbackID(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	registerAutoDBWithGateway(srv.URL, "", "host=localhost dbname=faulttest user=postgres") //nolint:errcheck
	var payload map[string]any
	json.Unmarshal(gotBody, &payload) //nolint:errcheck
	if payload["server_id"] != "faulttest-auto" {
		t.Errorf("server_id = %v, want faulttest-auto (no port in conn string)", payload["server_id"])
	}
}

func TestRegisterAutoDBWithGateway_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := registerAutoDBWithGateway(srv.URL, "", "host=127.0.0.1 port=61195 dbname=faulttest user=postgres")
	if err == nil {
		t.Error("want error for 500 response")
	}
}

func TestRegisterAutoDBWithGateway_NetworkError(t *testing.T) {
	err := registerAutoDBWithGateway("http://127.0.0.1:19994", "", "host=127.0.0.1 port=61195 dbname=faulttest user=postgres")
	if err == nil {
		t.Error("want error for unreachable server")
	}
}
