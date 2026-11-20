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
	"helpdesk/internal/infra"
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

// The reminder reads drills through auditd's own handlers over a range far longer
// than the estimate's: a drill 143 days old is found, a failed attempt is named
// until a re-verification replaces it, and what the list filters out is what the
// reminder does not see.
func TestDrills_TheReminderReadsThemThroughAuditd(t *testing.T) {
	srv, _ := newDrillServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/drills", srv.handleRecordDrill)
	mux.HandleFunc("GET /v1/drills", srv.handleListDrills)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	now := time.Now().UTC()
	record := func(source, status string, daysAgo int, extra map[string]any) string {
		fin := now.Add(-time.Duration(daysAgo) * 24 * time.Hour)
		b := map[string]any{
			"source_key": source, "clone_key": source + "-clone", "source_system_id": "7001", "clone_system_id": "7001",
			"method": "snapshot", "status": status, "finished_at": fin.Format(time.RFC3339),
		}
		if status == "refused" {
			b["checks"] = []map[string]string{{"name": "same_system", "result": "pass"}, {"name": "archive_guard", "result": "fail"}}
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
	reminder := infra.DrillReminder{Applies: true, Interval: infra.DefaultDrillInterval, Source: infra.SourceBuiltin}
	client := &availability.Client{URL: ts.URL}
	currency := func(db string) *availability.DrillCurrency {
		return client.DrillCurrency(context.Background(), db, reminder, availability.DrillInputs{}, now)
	}

	// An old drill, past the estimate's 90 days, and a refusal since.
	record("prod-orders", "verified", 143, nil)
	refused := record("prod-orders", "refused", 2, nil)
	record("prod-other", "verified", 5, nil) // another database: never seen here
	got := currency("prod-orders")
	if got.State != availability.DrillOverdue || got.AgeDays == nil || *got.AgeDays != 143 {
		t.Fatalf("a 143-day-old drill must be found and be overdue: %+v", got)
	}
	if got.LastAttempt == nil || got.LastAttempt.DrillID != refused || len(got.LastAttempt.Failed) != 1 || got.LastAttempt.Failed[0] != "archive_guard" {
		t.Errorf("the refusal after it is not named with what failed: %+v", got.LastAttempt)
	}
	if !strings.Contains(got.Detail, "last verified drill 143 days ago (limit 90 days)") || !strings.Contains(got.Detail, "was refused on archive_guard and does not count") {
		t.Errorf("detail: %q", got.Detail)
	}

	// Put right: a re-verification that replaces the refusal. The reminder is current
	// and the refusal is no longer an attempt that is named.
	record("prod-orders", "verified", 1, map[string]any{"supersedes": refused})
	got = currency("prod-orders")
	if got.State != availability.DrillCurrent || got.LastAttempt != nil || got.AgeDays == nil || *got.AgeDays != 1 {
		t.Errorf("after the re-verification: %+v", got)
	}
	// Another database has its own history.
	if other := currency("prod-other"); other.State != availability.DrillCurrent || *other.AgeDays != 5 {
		t.Errorf("prod-other: %+v", other)
	}
	// And one nobody has drilled is overdue, not an error.
	if none := currency("prod-never"); none.State != availability.DrillNever || !none.Overdue {
		t.Errorf("prod-never: %+v", none)
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

// One restore is one drill: the same source, clone, start and finish recorded
// twice would count twice in the estimate. The first live run did it by reusing a
// shell variable.
func TestDrillHandlers_TheSameRestoreRecordedTwiceIsRefused(t *testing.T) {
	srv, store := newDrillServer(t)
	post := func(b map[string]any) *httptest.ResponseRecorder {
		return callPV(srv.handleRecordDrill, http.MethodPost, "/v1/drills", b, nil)
	}
	// One body, built once: drillBody takes its times from the clock, so a second
	// call could land in the next second and no longer be the same restore.
	base := drillBody("prod-db")
	copyOf := func() map[string]any {
		c := map[string]any{}
		for k, v := range base {
			c[k] = v
		}
		return c
	}
	first := post(copyOf())
	if first.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", first.Code, first.Body.String())
	}
	var saved audit.DrillRecord
	json.NewDecoder(first.Body).Decode(&saved) //nolint:errcheck

	dup := copyOf()
	dup["ticket"] = "DRILL-AGAIN"
	rec := post(dup)
	if rec.Code != http.StatusConflict {
		t.Fatalf("the same restore twice: %d %s, want 409", rec.Code, rec.Body.String())
	}
	for _, want := range []string{saved.DrillID, "count it twice", "supersedes"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("message does not say %q: %s", want, rec.Body.String())
		}
	}

	// Nothing was written for the refused record: one row, one event.
	rows, _ := srv.store.List(audit.DrillQuery{SourceKey: "prod-db"})
	events, _ := store.Query(context.Background(), audit.QueryOptions{EventType: audit.EventTypeRestoreDrill})
	if len(rows) != 1 || len(events) != 1 {
		t.Errorf("rows=%d events=%d, want 1 and 1", len(rows), len(events))
	}

	// Another finish is another restore.
	other := copyOf()
	fin, _ := time.Parse(time.RFC3339, base["finished_at"].(string))
	other["finished_at"] = fin.Add(10 * time.Minute).Format(time.RFC3339)
	other["started_at"] = fin.Add(9 * time.Minute).Format(time.RFC3339)
	if rec := post(other); rec.Code != http.StatusCreated {
		t.Errorf("a different restore: %d %s", rec.Code, rec.Body.String())
	}

	// Naming the drill it replaces re-records the same restore, and the old row
	// then stops counting: the sample is still one.
	again := copyOf()
	again["supersedes"] = saved.DrillID
	if rec := post(again); rec.Code != http.StatusCreated {
		t.Fatalf("re-record with supersedes: %d %s", rec.Code, rec.Body.String())
	}
	all, _ := srv.store.List(audit.DrillQuery{SourceKey: "prod-db"})
	counted := 0
	for _, d := range audit.Current(all) {
		if d.Counted() && d.FinishedAt.Equal(fin) {
			counted++
		}
	}
	if counted != 1 {
		t.Errorf("the restore counts %d times after the re-record, want 1", counted)
	}
}

func TestDrillHandlers_AnAttemptThatDidNotCountNeverBlocksTheGoodRecord(t *testing.T) {
	srv, _ := newDrillServer(t)
	post := func(b map[string]any) *httptest.ResponseRecorder {
		return callPV(srv.handleRecordDrill, http.MethodPost, "/v1/drills", b, nil)
	}
	// What the first live run did: an unverifiable attempt, then the verified one with
	// the same times (a refusal put right without naming the earlier record).
	base := drillBody("prod-db") // built once: its times come from the clock
	with := func(status string) map[string]any {
		c := map[string]any{}
		for k, v := range base {
			c[k] = v
		}
		c["status"] = status
		return c
	}
	if rec := post(with("unverifiable")); rec.Code != http.StatusCreated {
		t.Fatalf("unverifiable attempt: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(with("verified")); rec.Code != http.StatusCreated {
		t.Errorf("verified record after an unverifiable one with the same times: %d %s, want 201", rec.Code, rec.Body.String())
	}
	// And a refused re-record of an already counted restore is just another attempt on the record.
	refused := with("refused")
	if rec := post(refused); rec.Code != http.StatusCreated {
		t.Errorf("refused attempt after a verified one: %d %s, want 201", rec.Code, rec.Body.String())
	}
}

func TestDrillHandlers_ListGetAndCurrent(t *testing.T) {
	srv, _ := newDrillServer(t)
	refusedBody := drillBody("prod-db")
	refusedBody["status"] = "refused"
	rec := callPV(srv.handleRecordDrill, http.MethodPost, "/v1/drills", refusedBody, nil)
	var refused audit.DrillRecord
	if err := json.NewDecoder(rec.Body).Decode(&refused); err != nil {
		t.Fatalf("decode refused drill: %v", err)
	}

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
