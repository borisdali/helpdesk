package faultlib

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestRemediator(t *testing.T, serverURL string) *Remediator {
	t.Helper()
	return NewRemediator(&HarnessConfig{
		GatewayURL:    serverURL,
		GatewayAPIKey: "test-key",
		ConnStr:       "host=localhost port=5432 dbname=testdb user=postgres",
	})
}

// ── prior_run_id threading ────────────────────────────────────────────────────

func TestTriggerPlaybook_SendsPriorRunID(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if err := r.triggerPlaybook(context.Background(), "pb_test", "plr_triage01"); err != nil {
		t.Fatalf("triggerPlaybook: %v", err)
	}
	if gotBody["prior_run_id"] != "plr_triage01" {
		t.Errorf("prior_run_id = %v, want plr_triage01", gotBody["prior_run_id"])
	}
}

func TestTriggerPlaybook_OmitsPriorRunIDWhenEmpty(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := newTestRemediator(t, srv.URL)
	if err := r.triggerPlaybook(context.Background(), "pb_test", ""); err != nil {
		t.Fatalf("triggerPlaybook: %v", err)
	}
	if _, present := gotBody["prior_run_id"]; present {
		t.Error("prior_run_id should not be present in request body when empty")
	}
}
