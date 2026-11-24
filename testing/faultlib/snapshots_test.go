package faultlib

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The harness records the healthy reading through the gateway's direct tool route,
// against the connection the fault's prompt uses, before anything is injected.
func TestCaptureSnapshots_CallsEachToolThroughTheGateway(t *testing.T) {
	var paths, auths, users []string
	var conns []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		auths = append(auths, r.Header.Get("Authorization"))
		users = append(users, r.Header.Get("X-User"))
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		conns = append(conns, body["connection_string"])
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &HarnessConfig{ViaGateway: true, GatewayURL: srv.URL + "/", GatewayAPIKey: "k", OperatorID: "faulttest", AgentConnStr: "faulttest-db"}
	f := Failure{ID: "db-backup-archiving-broken", PreSnapshots: []string{"get_pg_settings", "get_baseline"}}
	if err := CaptureSnapshots(context.Background(), cfg, f); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(paths, ","); got != "/api/v1/db/get_pg_settings,/api/v1/db/get_baseline" {
		t.Errorf("paths = %s", got)
	}
	for i := range paths {
		if auths[i] != "Bearer k" || users[i] != "faulttest" || conns[i] != "faulttest-db" {
			t.Errorf("call %d: auth %q user %q connection %q", i, auths[i], users[i], conns[i])
		}
	}
}

// Without the gateway there is no audit trail to record into, and a fault that
// lists nothing has nothing to record: neither is an error and neither calls out.
func TestCaptureSnapshots_DoesNothingWhenThereIsNothingToRecordInto(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()

	listed := Failure{ID: "x", PreSnapshots: []string{"get_pg_settings"}}
	for name, c := range map[string]struct {
		cfg *HarnessConfig
		f   Failure
	}{
		"direct run":     {&HarnessConfig{GatewayURL: srv.URL}, listed},
		"no gateway url": {&HarnessConfig{ViaGateway: true}, listed},
		"nothing listed": {&HarnessConfig{ViaGateway: true, GatewayURL: srv.URL}, Failure{ID: "x"}},
	} {
		if err := CaptureSnapshots(context.Background(), c.cfg, c.f); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if called {
		t.Error("the gateway was called")
	}
}

// A snapshot that could not be recorded stops the fault before it is injected, and
// the error names the tool and what the gateway said.
func TestCaptureSnapshots_AFailureNamesTheToolAndTheReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unknown tool: get_pg_settings", http.StatusBadRequest)
	}))
	defer srv.Close()

	cfg := &HarnessConfig{ViaGateway: true, GatewayURL: srv.URL}
	err := CaptureSnapshots(context.Background(), cfg, Failure{ID: "db-x", PreSnapshots: []string{"get_pg_settings"}})
	if err == nil || !strings.Contains(err.Error(), "get_pg_settings") || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "db-x") {
		t.Errorf("error = %v", err)
	}
}
