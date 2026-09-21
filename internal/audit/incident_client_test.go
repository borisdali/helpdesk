package audit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFetchIncidentByEntryRunID_Found proves a 200 response from
// GET /v1/incidents/by-run/{runID} decodes into an *Incident with its
// v0.29 fields intact — the exact fields IncidentNarrative.IncidentRecord
// (cmd/gateway/incident_narrative.go) exists to surface.
func TestFetchIncidentByEntryRunID_Found(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Incident{ //nolint:errcheck
			IncidentID:      "inc_abc123",
			EntryRunID:      "plr_a3f7c1b2",
			Origin:          "real",
			Status:          "resolved",
			Attribution:     "lock_contention",
			BundlePath:      "/incidents/a3f9b2c1.tar.gz",
			DraftPlaybookID: "pb_generated_001",
		})
	}))
	defer srv.Close()

	inc := FetchIncidentByEntryRunID(srv.URL, "", "plr_a3f7c1b2")
	if inc == nil {
		t.Fatal("FetchIncidentByEntryRunID returned nil, want a populated *Incident")
	}
	if gotPath != "/v1/incidents/by-run/plr_a3f7c1b2" {
		t.Errorf("request path = %q, want /v1/incidents/by-run/plr_a3f7c1b2", gotPath)
	}
	if inc.Origin != "real" {
		t.Errorf("Origin = %q, want real", inc.Origin)
	}
	if inc.Status != "resolved" {
		t.Errorf("Status = %q, want resolved", inc.Status)
	}
	if inc.Attribution != "lock_contention" {
		t.Errorf("Attribution = %q, want lock_contention", inc.Attribution)
	}
	if inc.BundlePath != "/incidents/a3f9b2c1.tar.gz" {
		t.Errorf("BundlePath = %q, want /incidents/a3f9b2c1.tar.gz", inc.BundlePath)
	}
	if inc.DraftPlaybookID != "pb_generated_001" {
		t.Errorf("DraftPlaybookID = %q, want pb_generated_001", inc.DraftPlaybookID)
	}
}

// TestFetchIncidentByEntryRunID_NotFound proves a 404 (no incident row for
// this run — true for every pre-v0.29 run, or one never tracked as an
// incident) fails open: nil, not an error/panic.
func TestFetchIncidentByEntryRunID_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no incident for run_id", http.StatusNotFound)
	}))
	defer srv.Close()

	inc := FetchIncidentByEntryRunID(srv.URL, "", "plr_never_tracked")
	if inc != nil {
		t.Errorf("FetchIncidentByEntryRunID = %+v, want nil on 404", inc)
	}
}

// TestFetchIncidentByEntryRunID_EmptyAuditURL proves the function is a safe
// no-op when auditd isn't configured — mirrors every other Fetch* helper's
// "auditURL empty" guard, so a gateway with no --audit-url set doesn't start
// making requests to an empty base URL.
func TestFetchIncidentByEntryRunID_EmptyAuditURL(t *testing.T) {
	if inc := FetchIncidentByEntryRunID("", "", "plr_a3f7c1b2"); inc != nil {
		t.Errorf("FetchIncidentByEntryRunID with empty auditURL = %+v, want nil", inc)
	}
}

// TestFetchIncidentByEntryRunID_ServerError fails open on a 500, same as
// the 404 case — a transient auditd error must not break the narrative
// endpoint that calls this.
func TestFetchIncidentByEntryRunID_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	inc := FetchIncidentByEntryRunID(srv.URL, "", "plr_a3f7c1b2")
	if inc != nil {
		t.Errorf("FetchIncidentByEntryRunID = %+v, want nil on 500", inc)
	}
}

// TestFetchIncidentByEntryRunID_SendsAuthHeader proves the Bearer token is
// forwarded when an API key is supplied, matching the other Fetch* helpers.
func TestFetchIncidentByEntryRunID_SendsAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Incident{IncidentID: "inc_1", EntryRunID: "plr_1"}) //nolint:errcheck
	}))
	defer srv.Close()

	FetchIncidentByEntryRunID(srv.URL, "test-key-123", "plr_1")
	if gotAuth != "Bearer test-key-123" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer test-key-123")
	}
}
