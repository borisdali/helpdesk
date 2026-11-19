package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/availability"
)

func newDrillServer(t *testing.T) (*restoreDrillServer, *audit.Store) {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ds, err := audit.NewRestoreDrillStore(store.DB(), false)
	if err != nil {
		t.Fatal(err)
	}
	return &restoreDrillServer{store: ds, auditStore: store}, store
}

func drillBody(source string) map[string]any {
	fin := time.Now().UTC().Add(-time.Hour)
	return map[string]any{
		"source_key": source, "clone_key": source + "-clone",
		"source_system_id": "7001", "clone_system_id": "7001",
		"method": "pgbackrest", "status": "verified",
		"finished_at": fin.Format(time.RFC3339), "started_at": fin.Add(-time.Minute).Format(time.RFC3339),
		"duration_seconds": 60, "duration_source": "reported",
		"ticket": "INC-77",
	}
}

func TestDrillHandlers_RecordWritesRowAndHashedEvent(t *testing.T) {
	srv, store := newDrillServer(t)
	rec := callPV(srv.handleRecordDrill, http.MethodPost, "/v1/drills", drillBody("prod-db"), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("record: %d %s", rec.Code, rec.Body.String())
	}
	var saved audit.DrillRecord
	if err := json.NewDecoder(rec.Body).Decode(&saved); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(saved.DrillID, "drl_") || saved.AuditEventID == "" {
		t.Fatalf("id / event link missing: %+v", saved)
	}

	events, err := store.Query(context.Background(), audit.QueryOptions{EventType: audit.EventTypeRestoreDrill})
	if err != nil || len(events) != 1 {
		t.Fatalf("restore_drill events: %d (%v)", len(events), err)
	}
	ev := events[0]
	if ev.EventID != saved.AuditEventID || ev.RestoreDrill == nil || ev.RestoreDrill.DrillID != saved.DrillID || ev.RestoreDrill.Ticket != "INC-77" {
		t.Errorf("event does not carry the drill: %+v", ev)
	}
	if ev.EventHash == "" || !audit.VerifyEventHash(&ev) {
		t.Error("event is not hash-covered")
	}
	// Editing what was verified must break the hash.
	ev.RestoreDrill.Status = audit.DrillRefused
	if audit.VerifyEventHash(&ev) {
		t.Error("changing the drill did not change the event hash")
	}
}

// The gateway marshals a DrillRecord, and Go writes an unset time as
// "0001-01-01T00:00:00Z" rather than leaving it out. A drill recorded with only
// a stated duration therefore arrives with two such times; they must come back
// as "not given", not as the year 1.
func TestDrillHandlers_UnsetTimesFromTheGatewayStayUnset(t *testing.T) {
	srv, _ := newDrillServer(t)
	rec := audit.DrillRecord{
		SourceKey: "prod-db", CloneKey: "prod-clone", SourceSystemID: "7001", CloneSystemID: "7001",
		Method: audit.DrillMethodSnapshot, Status: audit.DrillVerified,
		DurationSeconds: 120, DurationSource: audit.DrillDurationReported,
	}
	wire, err := json.Marshal(rec) // exactly what the gateway sends
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire), "0001-01-01T00:00:00Z") {
		t.Fatalf("this test assumes unset times are written out; the wire form is %s", wire)
	}
	resp := callPV(srv.handleRecordDrill, http.MethodPost, "/v1/drills", string(wire), nil)
	if resp.Code != http.StatusCreated {
		t.Fatalf("%d %s", resp.Code, resp.Body.String())
	}
	var saved audit.DrillRecord
	json.NewDecoder(resp.Body).Decode(&saved) //nolint:errcheck
	got, err := srv.store.Get(saved.DrillID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.StartedAt.IsZero() || !got.FinishedAt.IsZero() || got.DurationSeconds != 120 {
		t.Errorf("unset times came back as %v / %v (duration %d)", got.StartedAt, got.FinishedAt, got.DurationSeconds)
	}
	// A listing with an age filter still finds it: it falls back to recorded_at.
	list := callPV(srv.handleListDrills, http.MethodGet, "/v1/drills?source=prod-db&since="+time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), nil, nil)
	var drills []audit.DrillRecord
	json.NewDecoder(list.Body).Decode(&drills) //nolint:errcheck
	if len(drills) != 1 {
		t.Errorf("a drill with no finish time is not found by an age filter: %d", len(drills))
	}
}

// The estimate reads drills through auditd's own handlers: drills recorded
// there plan a restore that has too few real ones, and what the handlers
// filter (the source, the age, a superseded record) is what the estimate sees.
func TestDrills_FeedTheEstimateThroughAuditd(t *testing.T) {
	srv, _ := newDrillServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/drills", srv.handleRecordDrill)
	mux.HandleFunc("GET /v1/drills", srv.handleListDrills)
	mux.HandleFunc("GET /v1/incidents", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"count":0,"incidents":[]}`)) //nolint:errcheck
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	now := time.Now().UTC()
	record := func(source string, took int, daysAgo int, method string, extra map[string]any) string {
		fin := now.Add(-time.Duration(daysAgo) * 24 * time.Hour)
		b := map[string]any{
			"source_key": source, "clone_key": source + "-clone", "source_system_id": "7001", "clone_system_id": "7001",
			"method": method, "status": "verified", "duration_seconds": took, "duration_source": "reported",
			"restore_bytes": 10 << 30, "finished_at": fin.Format(time.RFC3339),
		}
		for k, v := range extra {
			b[k] = v
		}
		raw, _ := json.Marshal(b)
		resp, err := http.Post(ts.URL+"/v1/drills", "application/json", strings.NewReader(string(raw)))
		if err != nil || resp.StatusCode != http.StatusCreated {
			t.Fatalf("recording a drill: %v %v", err, resp)
		}
		var saved audit.DrillRecord
		json.NewDecoder(resp.Body).Decode(&saved) //nolint:errcheck
		resp.Body.Close()
		return saved.DrillID
	}
	record("prod-orders", 60, 5, "pgbackrest", nil)
	record("prod-orders", 90, 10, "pgbackrest", nil)
	record("prod-orders", 120, 20, "pgbackrest", nil)
	record("prod-orders", 9999, 30, "snapshot", nil)    // proof of recovery, not counted
	record("prod-orders", 9999, 120, "pgbackrest", nil) // too old
	record("prod-other", 5, 1, "pgbackrest", nil)       // another database
	refused := record("prod-orders", 9999, 15, "pgbackrest", map[string]any{"status": "refused"})
	record("prod-orders", 100, 14, "pgbackrest", map[string]any{"supersedes": refused}) // the redo of the refused one

	e, err := (&availability.Client{URL: ts.URL}).RecoveryEstimate(context.Background(), "prod-orders", availability.RestoreSeries, nil, now,
		availability.DrillInputs{RestoreBytes: 10 << 30})
	if err != nil {
		t.Fatal(err)
	}
	d := e.Drills
	if d == nil || d.Status != availability.EstimateEstimated || d.Samples != 4 || d.Recorded != 5 {
		t.Fatalf("drills: %+v", d)
	}
	// 60, 90, 100, 120 count; the snapshot is recorded but not counted; the one
	// older than 90 days is not even recorded for the window.
	if d.NotCounted["method"] != 1 || *d.MaxSeconds != 120 || e.PlanningSource != availability.PlanningFromDrills || *e.PlanningSeconds != 120 {
		t.Errorf("not counted %v, longest %v, planning %v from %q", d.NotCounted, ptrOr(d.MaxSeconds), ptrOr(e.PlanningSeconds), e.PlanningSource)
	}
	// Another database's drill never leaks in.
	other, err := (&availability.Client{URL: ts.URL}).RecoveryEstimate(context.Background(), "prod-other", availability.RestoreSeries, nil, now,
		availability.DrillInputs{RestoreBytes: 10 << 30})
	if err != nil || other.Drills.Samples != 1 || other.Drills.Status != availability.EstimateTooFew {
		t.Errorf("prod-other drills = %+v, %v", other.Drills, err)
	}
}

func ptrOr(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestDrillHandlers_RejectsBeforeWritingAnEvent(t *testing.T) {
	srv, store := newDrillServer(t)
	cases := map[string]func(map[string]any){
		"clone is source": func(b map[string]any) { b["clone_key"] = b["source_key"] },
		"bad method":      func(b map[string]any) { b["method"] = "dump" },
		"different ids":   func(b map[string]any) { b["clone_system_id"] = "9" },
		"unknown superseded": func(b map[string]any) {
			b["supersedes"] = "drl_nope"
		},
	}
	for name, edit := range cases {
		b := drillBody("prod-db")
		edit(b)
		if rec := callPV(srv.handleRecordDrill, http.MethodPost, "/v1/drills", b, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
	if rec := callPV(srv.handleRecordDrill, http.MethodPost, "/v1/drills", "{nope", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON: %d", rec.Code)
	}
	events, _ := store.Query(context.Background(), audit.QueryOptions{EventType: audit.EventTypeRestoreDrill})
	if len(events) != 0 {
		t.Errorf("%d restore_drill events left behind by refused records", len(events))
	}
}

func TestDrillHandlers_DuplicateIDConflicts(t *testing.T) {
	srv, _ := newDrillServer(t)
	b := drillBody("prod-db")
	b["drill_id"] = "drl_dup00001"
	if rec := callPV(srv.handleRecordDrill, http.MethodPost, "/v1/drills", b, nil); rec.Code != http.StatusCreated {
		t.Fatalf("first: %d", rec.Code)
	}
	if rec := callPV(srv.handleRecordDrill, http.MethodPost, "/v1/drills", b, nil); rec.Code != http.StatusConflict {
		t.Errorf("second: %d, want 409", rec.Code)
	}
}

func TestDrillHandlers_ListGetAndCurrent(t *testing.T) {
	srv, _ := newDrillServer(t)
	refusedBody := drillBody("prod-db")
	refusedBody["status"] = "refused"
	rec := callPV(srv.handleRecordDrill, http.MethodPost, "/v1/drills", refusedBody, nil)
	var refused audit.DrillRecord
	json.NewDecoder(rec.Body).Decode(&refused)

	again := drillBody("prod-db")
	again["supersedes"] = refused.DrillID
	if rec := callPV(srv.handleRecordDrill, http.MethodPost, "/v1/drills", again, nil); rec.Code != http.StatusCreated {
		t.Fatalf("re-verify: %d %s", rec.Code, rec.Body.String())
	}
	callPV(srv.handleRecordDrill, http.MethodPost, "/v1/drills", drillBody("other-db"), nil)

	list := func(query string) []audit.DrillRecord {
		t.Helper()
		rec := callPV(srv.handleListDrills, http.MethodGet, "/v1/drills"+query, nil, nil)
		var out []audit.DrillRecord
		if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&out) != nil {
			t.Fatalf("list %s: %d %s", query, rec.Code, rec.Body.String())
		}
		return out
	}
	if got := list("?source=prod-db"); len(got) != 2 {
		t.Errorf("source filter: %d, want 2", len(got))
	}
	if got := list("?source=prod-db&current=true"); len(got) != 1 || got[0].Status != audit.DrillVerified {
		t.Errorf("current=true should keep only the re-verification: %+v", got)
	}
	if got := list("?system_id=7001&status=refused"); len(got) != 1 {
		t.Errorf("system_id+status: %d", len(got))
	}
	if got := list("?source=nobody"); got == nil || len(got) != 0 {
		t.Errorf("empty list must be [] not null: %#v", got)
	}
	if rec := callPV(srv.handleListDrills, http.MethodGet, "/v1/drills?since=yesterday", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad since: %d", rec.Code)
	}

	if rec := callPV(srv.handleGetDrill, http.MethodGet, "/x", nil, map[string]string{"drillID": refused.DrillID}); rec.Code != http.StatusOK {
		t.Errorf("get: %d", rec.Code)
	}
	if rec := callPV(srv.handleGetDrill, http.MethodGet, "/x", nil, map[string]string{"drillID": "drl_none"}); rec.Code != http.StatusNotFound {
		t.Errorf("get unknown: %d, want 404", rec.Code)
	}
}
