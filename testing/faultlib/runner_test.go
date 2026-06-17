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

// newTestRunner returns a Runner configured with the given gateway URL.
func newTestRunner(t *testing.T, gatewayURL string, viaGateway bool) *Runner {
	t.Helper()
	return NewRunner(&HarnessConfig{
		GatewayURL:     gatewayURL,
		GatewayAPIKey:  "test-key",
		GatewayPurpose: "diagnostic",
		ConnStr:        "test-db",
		ViaGateway:     viaGateway,
	})
}

// playbookServer returns an httptest.Server handling the resolve (GET) and
// run (POST) endpoints. runResp is the JSON body returned by the run endpoint.
func playbookServer(t *testing.T, seriesID, playbookID string, runResp map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			if r.URL.Query().Get("series_id") != seriesID {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"playbooks": []map[string]any{{"playbook_id": playbookID}},
			})
			return
		}
		if r.URL.Path != "/api/v1/fleet/playbooks/"+playbookID+"/run" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(runResp) //nolint:errcheck
	}))
}

// ── runViaPlaybook ────────────────────────────────────────────────────────────

func TestRunViaPlaybook_Success(t *testing.T) {
	srv := playbookServer(t, "pbs_lock_chain_triage", "pb_abc123",
		map[string]any{"text": "root blocker is PID 42", "crystal_ball": false})
	defer srv.Close()

	r := newTestRunner(t, srv.URL, true)
	f := Failure{
		ID:                        "db-tx-lock-chain-blocker",
		Category:                  "database",
		Prompt:                    "Investigate the lock chain.",
		Timeout:                   "30s",
		DiagnosisPlaybookSeriesID: "pbs_lock_chain_triage",
	}

	resp := r.runViaPlaybook(context.Background(), f)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if resp.Text != "root blocker is PID 42" {
		t.Errorf("Text = %q, want %q", resp.Text, "root blocker is PID 42")
	}
	if resp.CrystalBall {
		t.Error("CrystalBall should be false for a normal run")
	}
}

func TestRunViaPlaybook_CrystalBallDetected(t *testing.T) {
	srv := playbookServer(t, "pbs_lock_chain_triage", "pb_abc123",
		map[string]any{
			"text":                 "The connection is blocked.",
			"crystal_ball":         true,
			"crystal_ball_warning": "playbook scaffolding was bypassed",
		})
	defer srv.Close()

	r := newTestRunner(t, srv.URL, true)
	f := Failure{
		ID:                        "db-tx-lock-chain-blocker",
		Category:                  "database",
		Prompt:                    "Investigate.",
		Timeout:                   "30s",
		DiagnosisPlaybookSeriesID: "pbs_lock_chain_triage",
	}

	resp := r.runViaPlaybook(context.Background(), f)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if !resp.CrystalBall {
		t.Error("CrystalBall should be true when gateway signals crystal-ball mode")
	}
}

func TestRunViaPlaybook_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error")) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRunner(t, srv.URL, true)
	f := Failure{
		ID: "db-lock", Prompt: "Investigate.", Timeout: "30s",
		DiagnosisPlaybookSeriesID: "pbs_lock_chain_triage",
	}

	resp := r.runViaPlaybook(context.Background(), f)
	if resp.Error == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

func TestRunViaPlaybook_PlaybookNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"playbooks": []any{}}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRunner(t, srv.URL, true)
	f := Failure{
		ID: "db-lock", Prompt: "Investigate.", Timeout: "30s",
		DiagnosisPlaybookSeriesID: "pbs_missing_series",
	}

	resp := r.runViaPlaybook(context.Background(), f)
	if resp.Error == nil {
		t.Error("expected error when playbook series not found, got nil")
	}
}

func TestRunViaPlaybook_SendsAuthAndPurpose(t *testing.T) {
	var gotAuth, gotPurpose string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"playbooks": []map[string]any{{"playbook_id": "pb_x"}},
			})
			return
		}
		gotAuth = r.Header.Get("Authorization")
		gotPurpose = r.Header.Get("X-Purpose")
		json.NewEncoder(w).Encode(map[string]any{"text": "ok"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRunner(t, srv.URL, true)
	f := Failure{ID: "test", Prompt: "p", Timeout: "30s", DiagnosisPlaybookSeriesID: "pbs_x"}
	r.runViaPlaybook(context.Background(), f)

	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer test-key")
	}
	if gotPurpose != "diagnostic" {
		t.Errorf("X-Purpose = %q, want %q", gotPurpose, "diagnostic")
	}
}

func TestRunViaPlaybook_RunIDPopulated(t *testing.T) {
	srv := playbookServer(t, "pbs_lock_chain_triage", "pb_abc",
		map[string]any{"text": "result", "run_id": "run_xyz789"})
	defer srv.Close()

	r := newTestRunner(t, srv.URL, true)
	f := Failure{
		ID: "db-lock", Prompt: "investigate", Timeout: "30s",
		DiagnosisPlaybookSeriesID: "pbs_lock_chain_triage",
	}

	resp := r.runViaPlaybook(context.Background(), f)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if resp.RunID != "run_xyz789" {
		t.Errorf("RunID = %q, want %q", resp.RunID, "run_xyz789")
	}
}

// ── X-User propagation ────────────────────────────────────────────────────────

func TestRunViaPlaybook_SendsXUser(t *testing.T) {
	var gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"playbooks": []map[string]any{{"playbook_id": "pb_x"}},
			})
			return
		}
		gotUser = r.Header.Get("X-User")
		json.NewEncoder(w).Encode(map[string]any{"text": "ok"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := NewRunner(&HarnessConfig{
		GatewayURL: srv.URL, GatewayAPIKey: "key",
		ViaGateway: true, OperatorID: "alice",
	})
	f := Failure{ID: "test", Prompt: "p", Timeout: "30s", DiagnosisPlaybookSeriesID: "pbs_x"}
	r.runViaPlaybook(context.Background(), f)

	if gotUser != "alice" {
		t.Errorf("X-User = %q, want %q", gotUser, "alice")
	}
}

func TestResolvePlaybookID_SendsXUser(t *testing.T) {
	var gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get("X-User")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"playbooks": []map[string]any{{"playbook_id": "pb_x"}},
		})
	}))
	defer srv.Close()

	r := NewRunner(&HarnessConfig{
		GatewayURL: srv.URL, GatewayAPIKey: "key", OperatorID: "alice",
	})
	client := &http.Client{Timeout: 5 * time.Second}
	if _, err := r.resolvePlaybookID(context.Background(), client, "pbs_x"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotUser != "alice" {
		t.Errorf("X-User = %q, want %q", gotUser, "alice")
	}
}

// ── Run dispatch ──────────────────────────────────────────────────────────────

func TestRun_ViaGatewayWithSeriesID_UsesPlaybook(t *testing.T) {
	playbookCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"playbooks": []map[string]any{{"playbook_id": "pb_abc"}},
			})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/run") {
			playbookCalled = true
		}
		json.NewEncoder(w).Encode(map[string]any{"text": "diagnosis via playbook"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := NewRunner(&HarnessConfig{
		GatewayURL: srv.URL, GatewayAPIKey: "key",
		GatewayPurpose: "diagnostic", ConnStr: "test-db", ViaGateway: true,
	})
	f := Failure{
		ID: "db-lock", Category: "database",
		Prompt: "investigate", Timeout: "30s",
		DiagnosisPlaybookSeriesID: "pbs_lock_chain_triage",
	}

	resp := r.Run(context.Background(), f)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if !playbookCalled {
		t.Error("expected playbook /run endpoint to be called with ViaGateway=true and DiagnosisPlaybookSeriesID set")
	}
}

func TestRun_ViaGatewayNoSeriesID_UsesGatewayQuery(t *testing.T) {
	queryCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/query" {
			queryCalled = true
			json.NewEncoder(w).Encode(map[string]any{"text": "query result"}) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	r := newTestRunner(t, srv.URL, true)
	f := Failure{
		ID: "db-slow", Category: "database",
		Prompt: "investigate", Timeout: "30s",
		// DiagnosisPlaybookSeriesID intentionally empty
	}

	resp := r.Run(context.Background(), f)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if !queryCalled {
		t.Error("expected gateway /api/v1/query to be called when ViaGateway=true and no DiagnosisPlaybookSeriesID")
	}
}

// ── isGateway auto-detection ─────────────────────────────────────────────────

func TestRun_IsGateway_RoutesViaGatewayAPI(t *testing.T) {
	var queryCalled bool
	var gotAgentName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/agents":
			// Signals to IsGatewayURL that this is a helpdesk gateway.
			w.WriteHeader(http.StatusOK)
		case "/api/v1/query":
			queryCalled = true
			var req map[string]string
			json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
			gotAgentName = req["agent"]
			json.NewEncoder(w).Encode(map[string]any{"text": "gateway diagnosed it"}) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// ViaGateway is NOT set — routing must be driven by isGateway auto-detection.
	r := NewRunner(&HarnessConfig{
		DBAgentURL:     srv.URL,
		GatewayAPIKey:  "test-key",
		GatewayPurpose: "diagnostic",
	})
	f := Failure{ID: "db-test", Category: "database", Prompt: "investigate", Timeout: "30s"}

	resp := r.Run(context.Background(), f)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if !queryCalled {
		t.Error("expected /api/v1/query to be called when agent URL is a gateway")
	}
	if gotAgentName != "database" {
		t.Errorf("agent name = %q, want %q", gotAgentName, "database")
	}
}

func TestRun_IsGateway_CachesResult(t *testing.T) {
	probeCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/agents" {
			probeCount++
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"text": "ok"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := NewRunner(&HarnessConfig{DBAgentURL: srv.URL, GatewayAPIKey: "key"})
	f := Failure{ID: "db", Category: "database", Prompt: "p", Timeout: "30s"}

	r.Run(context.Background(), f)
	r.Run(context.Background(), f)
	r.Run(context.Background(), f)

	if probeCount != 1 {
		t.Errorf("isGateway probed /api/v1/agents %d times across 3 Run calls, want 1 (cached)", probeCount)
	}
}

// ── categoryToGatewayAgent ────────────────────────────────────────────────────

func TestCategoryToGatewayAgent(t *testing.T) {
	cases := []struct {
		category string
		want     string
	}{
		{"database", "database"},
		{"kubernetes", "k8s"},   // gateway alias is "k8s", not "kubernetes"
		{"host", "sysadmin"},    // gateway alias is "sysadmin"
		{"compound", "database"},
		{"unknown", "database"}, // default falls back to database
	}
	for _, tc := range cases {
		got := categoryToGatewayAgent(tc.category)
		if got != tc.want {
			t.Errorf("categoryToGatewayAgent(%q) = %q, want %q", tc.category, got, tc.want)
		}
	}
}

// ── X-Trace-ID propagation ────────────────────────────────────────────────────

func TestRunViaPlaybook_SendsXTraceID(t *testing.T) {
	var gotTraceID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"playbooks": []map[string]any{{"playbook_id": "pb_x"}},
			})
			return
		}
		gotTraceID = r.Header.Get("X-Trace-ID")
		json.NewEncoder(w).Encode(map[string]any{"text": "ok"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRunner(t, srv.URL, true)
	f := Failure{ID: "test", Prompt: "p", Timeout: "30s", DiagnosisPlaybookSeriesID: "pbs_x"}
	ctx := WithFaultTraceID(context.Background(), "trace-abc123")
	r.runViaPlaybook(ctx, f)

	if gotTraceID != "trace-abc123" {
		t.Errorf("X-Trace-ID = %q, want %q", gotTraceID, "trace-abc123")
	}
}

// TestRunViaPlaybook_GateEscalation_SendsRemediationSeriesID verifies that when
// GateEscalation is true and Remediation.PlaybookID is set, the runner includes
// remediation_series_id in the request body. This field powers the server-side
// fallback gate: without it, an LLM that omits TRANSITION_TO silently bypasses
// operator review.
func TestRunViaPlaybook_GateEscalation_SendsRemediationSeriesID(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"playbooks": []map[string]any{{"playbook_id": "pb_triage01"}},
			})
			return
		}
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		json.NewEncoder(w).Encode(map[string]any{"text": "ok", "run_id": "plr_x"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := NewRunner(&HarnessConfig{
		GatewayURL:    srv.URL,
		GatewayAPIKey: "key",
		ViaGateway:    true,
		GateEscalation: true,
	})
	f := Failure{
		ID:                        "db-tx-lock-chain-blocker",
		Category:                  "database",
		Prompt:                    "Investigate the lock chain.",
		Timeout:                   "30s",
		DiagnosisPlaybookSeriesID: "pbs_lock_chain_triage",
		Remediation: RemediationSpec{
			PlaybookID: "pbs_lock_chain_remediate",
		},
	}

	resp := r.runViaPlaybook(context.Background(), f)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	if gotBody["gate_escalation"] != true {
		t.Errorf("gate_escalation = %v, want true", gotBody["gate_escalation"])
	}
	if gotBody["remediation_series_id"] != "pbs_lock_chain_remediate" {
		t.Errorf("remediation_series_id = %q, want %q", gotBody["remediation_series_id"], "pbs_lock_chain_remediate")
	}
}

// TestRunViaPlaybook_GateEscalation_NoRemediationSeriesID_WhenPlaybookIDEmpty
// verifies that remediation_series_id is NOT sent when Remediation.PlaybookID
// is empty, so faults without a remediation playbook aren't affected.
func TestRunViaPlaybook_GateEscalation_NoRemediationSeriesID_WhenPlaybookIDEmpty(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"playbooks": []map[string]any{{"playbook_id": "pb_diag01"}},
			})
			return
		}
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		json.NewEncoder(w).Encode(map[string]any{"text": "ok"}) //nolint:errcheck
	}))
	defer srv.Close()

	r := NewRunner(&HarnessConfig{
		GatewayURL:     srv.URL,
		GatewayAPIKey:  "key",
		ViaGateway:     true,
		GateEscalation: true,
	})
	f := Failure{
		ID: "db-some-fault", Category: "database",
		Prompt: "investigate", Timeout: "30s",
		DiagnosisPlaybookSeriesID: "pbs_some_triage",
		// Remediation.PlaybookID intentionally empty
	}

	resp := r.runViaPlaybook(context.Background(), f)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if _, ok := gotBody["remediation_series_id"]; ok {
		t.Errorf("remediation_series_id should not be sent when Remediation.PlaybookID is empty, got %q", gotBody["remediation_series_id"])
	}
	if gotBody["gate_escalation"] != true {
		t.Errorf("gate_escalation = %v, want true", gotBody["gate_escalation"])
	}
}
