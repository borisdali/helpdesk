package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestRunner returns a Runner configured to use the given gateway URL.
func newTestRunner(t *testing.T, gatewayURL string, viaGateway bool) *Runner {
	t.Helper()
	return NewRunner(&HarnessConfig{
		GatewayURL:    gatewayURL,
		GatewayAPIKey: "test-key",
		GatewayPurpose: "diagnostic",
		ConnStr:       "test-db",
		ViaGateway:    viaGateway,
	})
}

// playbookServer returns an httptest.Server that handles the resolve (GET) and
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

// ── runViaPlaybook ────────────────────────────────────────────────────────

func TestRunViaPlaybook_Success(t *testing.T) {
	srv := playbookServer(t, "pbs_k8s_pod_crash_triage", "pb_abc123",
		map[string]any{"text": "WAL disk full detected", "crystal_ball": false})
	defer srv.Close()

	r := newTestRunner(t, srv.URL, true)
	f := Failure{
		ID:                        "db-wal-disk-full-k8s",
		Category:                  "kubernetes",
		Prompt:                    "Investigate the pod crash.",
		Timeout:                   "30s",
		DiagnosisPlaybookSeriesID: "pbs_k8s_pod_crash_triage",
	}

	resp := r.runViaPlaybook(context.Background(), f)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if resp.Text != "WAL disk full detected" {
		t.Errorf("Text = %q, want %q", resp.Text, "WAL disk full detected")
	}
	if resp.CrystalBall {
		t.Error("CrystalBall should be false for normal run")
	}
}

func TestRunViaPlaybook_CrystalBallDetected(t *testing.T) {
	srv := playbookServer(t, "pbs_k8s_pod_crash_triage", "pb_abc123",
		map[string]any{
			"text":                 "The pod crashed.",
			"crystal_ball":         true,
			"crystal_ball_warning": "scaffolding bypassed",
		})
	defer srv.Close()

	r := newTestRunner(t, srv.URL, true)
	f := Failure{
		ID:                        "db-wal-disk-full-k8s",
		Category:                  "kubernetes",
		Prompt:                    "Investigate.",
		Timeout:                   "30s",
		DiagnosisPlaybookSeriesID: "pbs_k8s_pod_crash_triage",
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
		ID:                        "db-wal-disk-full-k8s",
		Prompt:                    "Investigate.",
		Timeout:                   "30s",
		DiagnosisPlaybookSeriesID: "pbs_k8s_pod_crash_triage",
	}

	resp := r.runViaPlaybook(context.Background(), f)
	if resp.Error == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

func TestRunViaPlaybook_PlaybookNotFound(t *testing.T) {
	// Gateway returns empty playbooks list — series_id not registered.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"playbooks": []any{}}) //nolint:errcheck
	}))
	defer srv.Close()

	r := newTestRunner(t, srv.URL, true)
	f := Failure{
		ID:                        "db-wal-disk-full-k8s",
		Prompt:                    "Investigate.",
		Timeout:                   "30s",
		DiagnosisPlaybookSeriesID: "pbs_missing",
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
	f := Failure{
		ID: "test", Prompt: "p", Timeout: "30s",
		DiagnosisPlaybookSeriesID: "pbs_x",
	}
	r.runViaPlaybook(context.Background(), f)

	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	if gotPurpose != "diagnostic" {
		t.Errorf("X-Purpose = %q, want diagnostic", gotPurpose)
	}
}

// ── Runner.Run dispatch ───────────────────────────────────────────────────

func TestRun_ViaGatewayWithSeriesID_UsesPlaybook(t *testing.T) {
	playbookCalled := false
	srv := playbookServer(t, "pbs_k8s_pod_crash_triage", "pb_abc",
		map[string]any{"text": "diagnosis via playbook"})
	defer srv.Close()
	// Wrap to detect which path was taken.
	origSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/fleet/playbooks/pb_abc/run" {
			playbookCalled = true
		}
		// Forward to the real handler.
		srv.Config.Handler.ServeHTTP(w, r)
	}))
	defer origSrv.Close()

	r := NewRunner(&HarnessConfig{
		GatewayURL:     origSrv.URL,
		GatewayAPIKey:  "key",
		GatewayPurpose: "diagnostic",
		ConnStr:        "test-db",
		ViaGateway:     true,
	})
	f := Failure{
		ID: "db-wal-disk-full-k8s", Category: "kubernetes",
		Prompt: "investigate", Timeout: "30s",
		DiagnosisPlaybookSeriesID: "pbs_k8s_pod_crash_triage",
	}

	resp := r.Run(context.Background(), f)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if !playbookCalled {
		t.Error("expected playbook endpoint to be called with --via-gateway")
	}
}

func TestRun_ViaGatewayNoSeriesID_FallsBack(t *testing.T) {
	// Fault has no DiagnosisPlaybookSeriesID → should NOT call the playbook endpoint.
	// agentURL is also empty → Run returns an error about no agent configured.
	r := NewRunner(&HarnessConfig{
		GatewayURL: "http://gateway:8080",
		ViaGateway: true,
		// No K8sAgentURL configured.
	})
	f := Failure{
		ID: "k8s-crashloop", Category: "kubernetes",
		Prompt: "investigate", Timeout: "10s",
		// DiagnosisPlaybookSeriesID intentionally empty.
	}

	resp := r.Run(context.Background(), f)
	// Should fail with "no agent URL configured", not a playbook error.
	if resp.Error == nil {
		t.Fatal("expected error for missing agent URL, got nil")
	}
	if resp.Text != "" {
		t.Error("expected no text in fallback-error response")
	}
}
