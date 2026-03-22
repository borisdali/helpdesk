package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/client"
)

// gatewayStub is a minimal httptest server that records incoming requests and
// returns configurable responses — used across all sub-tests.
type gatewayStub struct {
	*httptest.Server
	lastHeaders http.Header
	lastBody    map[string]any

	// configurable response
	statusCode int
	respBody   any
	traceID    string
}

func newGatewayStub(t *testing.T) *gatewayStub {
	t.Helper()
	s := &gatewayStub{
		statusCode: http.StatusOK,
		respBody:   map[string]any{"agent": "postgres_database_agent", "text": "all good"},
		traceID:    "tr_abc12345",
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.lastHeaders = r.Header.Clone()
		if r.Body != nil {
			json.NewDecoder(r.Body).Decode(&s.lastBody) //nolint:errcheck
		}
		if s.traceID != "" {
			w.Header().Set("X-Trace-ID", s.traceID)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.statusCode)
		json.NewEncoder(w).Encode(s.respBody) //nolint:errcheck
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *gatewayStub) newClient(cfg client.Config) *client.Client {
	cfg.GatewayURL = s.URL
	return client.New(cfg)
}

// ── NewConfigFromEnv ────────────────────────────────────────────────────────

func TestNewConfigFromEnv(t *testing.T) {
	t.Setenv("HELPDESK_GATEWAY_URL", "http://gw.internal:8080")
	t.Setenv("HELPDESK_CLIENT_USER", "alice@example.com")
	t.Setenv("HELPDESK_CLIENT_API_KEY", "sk-test")
	t.Setenv("HELPDESK_SESSION_PURPOSE", "diagnostic")
	t.Setenv("HELPDESK_SESSION_PURPOSE_NOTE", "INC-42")

	cfg := client.NewConfigFromEnv()

	if cfg.GatewayURL != "http://gw.internal:8080" {
		t.Errorf("GatewayURL: got %q", cfg.GatewayURL)
	}
	if cfg.UserID != "alice@example.com" {
		t.Errorf("UserID: got %q", cfg.UserID)
	}
	if cfg.APIKey != "sk-test" {
		t.Errorf("APIKey: got %q", cfg.APIKey)
	}
	if cfg.Purpose != "diagnostic" {
		t.Errorf("Purpose: got %q", cfg.Purpose)
	}
	if cfg.PurposeNote != "INC-42" {
		t.Errorf("PurposeNote: got %q", cfg.PurposeNote)
	}
}

func TestNewConfigFromEnv_Defaults(t *testing.T) {
	// Ensure no stray env vars from the test environment interfere.
	os.Unsetenv("HELPDESK_GATEWAY_URL")

	cfg := client.NewConfigFromEnv()
	if cfg.GatewayURL != "http://localhost:8080" {
		t.Errorf("default GatewayURL: got %q", cfg.GatewayURL)
	}
}

// ── Headers ────────────────────────────────────────────────────────────────

func TestQuery_APIKeyHeader(t *testing.T) {
	stub := newGatewayStub(t)
	c := stub.newClient(client.Config{APIKey: "my-secret-key"})

	_, err := c.Query(context.Background(), client.QueryRequest{Agent: "database", Message: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if got := stub.lastHeaders.Get("Authorization"); got != "Bearer my-secret-key" {
		t.Errorf("Authorization header: got %q, want %q", got, "Bearer my-secret-key")
	}
}

func TestQuery_UserHeader(t *testing.T) {
	stub := newGatewayStub(t)
	c := stub.newClient(client.Config{UserID: "bob@example.com"})

	_, err := c.Query(context.Background(), client.QueryRequest{Agent: "database", Message: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if got := stub.lastHeaders.Get("X-User"); got != "bob@example.com" {
		t.Errorf("X-User header: got %q, want %q", got, "bob@example.com")
	}
}

func TestQuery_PurposeHeader(t *testing.T) {
	stub := newGatewayStub(t)
	c := stub.newClient(client.Config{Purpose: "remediation"})

	_, err := c.Query(context.Background(), client.QueryRequest{Agent: "database", Message: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if got := stub.lastHeaders.Get("X-Purpose"); got != "remediation" {
		t.Errorf("X-Purpose header: got %q, want %q", got, "remediation")
	}
}

func TestQuery_NoCredentials(t *testing.T) {
	// With no credentials configured, no auth headers should be sent.
	stub := newGatewayStub(t)
	c := stub.newClient(client.Config{})

	_, err := c.Query(context.Background(), client.QueryRequest{Agent: "database", Message: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if got := stub.lastHeaders.Get("Authorization"); got != "" {
		t.Errorf("unexpected Authorization header: %q", got)
	}
	if got := stub.lastHeaders.Get("X-User"); got != "" {
		t.Errorf("unexpected X-User header: %q", got)
	}
}

// ── Request body ───────────────────────────────────────────────────────────

func TestQuery_RequestBody(t *testing.T) {
	stub := newGatewayStub(t)
	c := stub.newClient(client.Config{PurposeNote: "config-note"})

	_, err := c.Query(context.Background(), client.QueryRequest{
		Agent:   "k8s",
		Message: "check pods",
	})
	if err != nil {
		t.Fatal(err)
	}

	if stub.lastBody["agent"] != "k8s" {
		t.Errorf("body agent: got %v", stub.lastBody["agent"])
	}
	if stub.lastBody["message"] != "check pods" {
		t.Errorf("body message: got %v", stub.lastBody["message"])
	}
	// Config-level purpose note should be sent when no per-request override.
	if stub.lastBody["purpose_note"] != "config-note" {
		t.Errorf("body purpose_note: got %v", stub.lastBody["purpose_note"])
	}
}

func TestQuery_PerRequestPurposeNoteOverridesConfig(t *testing.T) {
	stub := newGatewayStub(t)
	c := stub.newClient(client.Config{PurposeNote: "config-note"})

	_, err := c.Query(context.Background(), client.QueryRequest{
		Agent:       "database",
		Message:     "ping",
		PurposeNote: "per-request-note",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stub.lastBody["purpose_note"] != "per-request-note" {
		t.Errorf("purpose_note: got %v, want per-request-note", stub.lastBody["purpose_note"])
	}
}

// ── Response parsing ───────────────────────────────────────────────────────

func TestQuery_ResponseParsed(t *testing.T) {
	stub := newGatewayStub(t)
	stub.respBody = map[string]any{"agent": "postgres_database_agent", "text": "DB is healthy"}
	stub.traceID = "tr_xyz99"

	c := stub.newClient(client.Config{})
	resp, err := c.Query(context.Background(), client.QueryRequest{Agent: "database", Message: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "DB is healthy" {
		t.Errorf("Text: got %q", resp.Text)
	}
	if resp.TraceID != "tr_xyz99" {
		t.Errorf("TraceID: got %q", resp.TraceID)
	}
	if resp.Agent != "postgres_database_agent" {
		t.Errorf("Agent: got %q", resp.Agent)
	}
}

// ── Error handling ─────────────────────────────────────────────────────────

func TestQuery_401_ReturnsAuthError(t *testing.T) {
	stub := newGatewayStub(t)
	stub.statusCode = http.StatusUnauthorized
	stub.respBody = map[string]any{"error": "authentication failed: identity: invalid API key"}

	c := stub.newClient(client.Config{APIKey: "wrong-key"})
	_, err := c.Query(context.Background(), client.QueryRequest{Agent: "database", Message: "ping"})
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !contains(err.Error(), "authentication failed") {
		t.Errorf("error should mention authentication: %v", err)
	}
}

func TestQuery_400_IncludesGatewayMessage(t *testing.T) {
	stub := newGatewayStub(t)
	stub.statusCode = http.StatusBadRequest
	stub.respBody = map[string]any{"error": `unknown agent "bogus" (valid: database, db, k8s, incident, research)`}

	c := stub.newClient(client.Config{})
	_, err := c.Query(context.Background(), client.QueryRequest{Agent: "bogus", Message: "ping"})
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if !contains(err.Error(), "bogus") {
		t.Errorf("error should include agent name: %v", err)
	}
}

func TestQuery_502_ReturnsGatewayError(t *testing.T) {
	stub := newGatewayStub(t)
	stub.statusCode = http.StatusBadGateway
	stub.respBody = map[string]any{"error": "agent unavailable"}

	c := stub.newClient(client.Config{})
	_, err := c.Query(context.Background(), client.QueryRequest{Agent: "database", Message: "ping"})
	if err == nil {
		t.Fatal("expected error for 502")
	}
	if !contains(err.Error(), "agent unavailable") {
		t.Errorf("error should include gateway message: %v", err)
	}
}

func TestQuery_ContextCancelled(t *testing.T) {
	stub := newGatewayStub(t)
	c := stub.newClient(client.Config{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := c.Query(ctx, client.QueryRequest{Agent: "database", Message: "ping"})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

// ── Ping ───────────────────────────────────────────────────────────────────

func TestPing_OK(t *testing.T) {
	stub := newGatewayStub(t)
	stub.respBody = []any{} // /api/v1/agents returns an array
	c := stub.newClient(client.Config{})

	if err := c.Ping(context.Background()); err != nil {
		t.Errorf("Ping should succeed: %v", err)
	}
}

func TestPing_Unreachable(t *testing.T) {
	c := client.New(client.Config{
		GatewayURL: "http://127.0.0.1:19999", // nothing listening
		Timeout:    200 * time.Millisecond,
	})
	if err := c.Ping(context.Background()); err == nil {
		t.Error("Ping should fail when gateway is unreachable")
	}
}

func TestPing_AuthFailure(t *testing.T) {
	stub := newGatewayStub(t)
	stub.statusCode = http.StatusUnauthorized

	c := stub.newClient(client.Config{APIKey: "bad-key"})
	err := c.Ping(context.Background())
	if err == nil {
		t.Fatal("expected auth error")
	}
	if !contains(err.Error(), "authentication") {
		t.Errorf("error should mention authentication: %v", err)
	}
}

func TestPing_AuthFailure_IncludesGatewayReason(t *testing.T) {
	stub := newGatewayStub(t)
	stub.statusCode = http.StatusUnauthorized
	stub.respBody = map[string]any{"error": "authentication failed: identity: no credentials provided"}

	c := stub.newClient(client.Config{})
	err := c.Ping(context.Background())
	if err == nil {
		t.Fatal("expected auth error")
	}
	if !contains(err.Error(), "no credentials provided") {
		t.Errorf("error should include gateway reason: %v", err)
	}
}

// ── GatewayURL ─────────────────────────────────────────────────────────────

func TestGatewayURL(t *testing.T) {
	c := client.New(client.Config{GatewayURL: "http://gw.internal:8080"})
	if c.GatewayURL() != "http://gw.internal:8080" {
		t.Errorf("GatewayURL: got %q", c.GatewayURL())
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func contains(s, substr string) bool { return strings.Contains(s, substr) }
