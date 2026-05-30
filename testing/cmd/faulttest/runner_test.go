package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRunnerBridge_PropagatesTraceID verifies that the cmd/faulttest Runner
// bridges the local ctxKeyFaultTraceID context slot into faultlib's slot so
// that faultlib.Runner sets the X-Trace-ID header on gateway requests.
func TestRunnerBridge_PropagatesTraceID(t *testing.T) {
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

	r := NewRunner(&HarnessConfig{
		GatewayURL: srv.URL, GatewayAPIKey: "key",
		ViaGateway: true,
	})
	ctx := context.WithValue(context.Background(), ctxKeyFaultTraceID{}, "trace-xyz")
	f := Failure{
		ID: "test", Category: "database",
		Prompt: "p", Timeout: "30s",
		DiagnosisPlaybookSeriesID: "pbs_x",
	}
	r.Run(ctx, f)

	if gotTraceID != "trace-xyz" {
		t.Errorf("X-Trace-ID = %q, want %q", gotTraceID, "trace-xyz")
	}
}
