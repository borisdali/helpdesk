package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
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
