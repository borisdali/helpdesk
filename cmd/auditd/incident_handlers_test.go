package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/availability"
	"helpdesk/internal/infra"
	"helpdesk/playbooks"
)

func newIncidentServer(t *testing.T) *incidentServer {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	is, err := audit.NewIncidentStore(store.DB(), false)
	if err != nil {
		t.Fatalf("NewIncidentStore: %v", err)
	}
	return &incidentServer{store: is}
}

func TestIncidentHandlers_Create_OK(t *testing.T) {
	srv := newIncidentServer(t)

	body, _ := json.Marshal(map[string]any{"trace_id": "tr_abc", "entry_run_id": "plr_xyz"})
	req := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleCreate(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var inc audit.Incident
	if err := json.NewDecoder(rec.Body).Decode(&inc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if inc.IncidentID == "" {
		t.Error("incident_id should be set")
	}
	if inc.Origin != audit.IncidentOriginReal {
		t.Errorf("origin = %q, want %q (default)", inc.Origin, audit.IncidentOriginReal)
	}
	if inc.Status != audit.IncidentStatusOpen {
		t.Errorf("status = %q, want %q (default)", inc.Status, audit.IncidentStatusOpen)
	}
}

func TestIncidentHandlers_Create_SeriesIDSurvivesRoundTrip(t *testing.T) {
	srv := newIncidentServer(t)

	body, _ := json.Marshal(map[string]any{"entry_run_id": "plr_xyz", "series_id": "pbs_db_max_connections_triage"})
	createReq := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(body))
	createRec := httptest.NewRecorder()
	srv.handleCreate(createRec, createReq)
	var created audit.Incident
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.SeriesID != "pbs_db_max_connections_triage" {
		t.Errorf("create response series_id = %q, want pbs_db_max_connections_triage", created.SeriesID)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/incidents/"+created.IncidentID, nil)
	getReq.SetPathValue("incidentID", created.IncidentID)
	getRec := httptest.NewRecorder()
	srv.handleGet(getRec, getReq)
	var fetched audit.Incident
	if err := json.NewDecoder(getRec.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if fetched.SeriesID != "pbs_db_max_connections_triage" {
		t.Errorf("GET series_id = %q, want pbs_db_max_connections_triage", fetched.SeriesID)
	}
}

// The gateway sends the database an incident is about in the create body; it
// must survive the HTTP handler and come back on both GET and the list filter.
func TestIncidentHandlers_Create_TargetSurvivesRoundTripAndFilters(t *testing.T) {
	srv := newIncidentServer(t)

	for _, body := range []map[string]any{
		{"entry_run_id": "plr_a", "target_key": "prod-main", "target_input": "host=db1 port=5432 password=***"},
		{"entry_run_id": "plr_b", "target_key": "prod-main", "target_input": "prod-main"},
		{"entry_run_id": "plr_c", "target_key": "vm-pg", "target_input": "vm-pg"},
		{"entry_run_id": "plr_d", "target_input": "no-such-server"},
	} {
		data, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		srv.handleCreate(rec, httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(data)))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %v: %d %s", body, rec.Code, rec.Body.String())
		}
	}

	list := func(query string) []audit.Incident {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.handleList(rec, httptest.NewRequest(http.MethodGet, "/v1/incidents"+query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("list%s: %d %s", query, rec.Code, rec.Body.String())
		}
		var result struct {
			Incidents []audit.Incident `json:"incidents"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return result.Incidents
	}

	prod := list("?target_key=prod-main")
	if len(prod) != 2 {
		t.Fatalf("?target_key=prod-main = %d incidents, want 2", len(prod))
	}
	for _, inc := range prod {
		if inc.TargetKey != "prod-main" || inc.TargetInput == "" {
			t.Errorf("incident lost its target: %+v", inc)
		}
	}
	if got := list("?target_key=nothing-here"); len(got) != 0 {
		t.Errorf("an unknown key matched %d incidents", len(got))
	}
	// Without the filter the unresolved incident is still listed, with its input.
	all := list("")
	if len(all) != 4 {
		t.Fatalf("unfiltered list = %d, want 4", len(all))
	}
	unresolved := 0
	for _, inc := range all {
		if inc.TargetKey == "" {
			unresolved++
			if inc.TargetInput != "no-such-server" {
				t.Errorf("the unresolved incident lost its input: %+v", inc)
			}
		}
	}
	if unresolved != 1 {
		t.Errorf("unresolved incidents = %d, want 1", unresolved)
	}
}

// newLedgerServer is an incident server that can also read each incident's
// playbook runs, as auditd wires it.
func newLedgerServer(t *testing.T) (*incidentServer, *audit.PlaybookRunStore) {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	is, err := audit.NewIncidentStore(store.DB(), false)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := audit.NewPlaybookRunStore(store.DB(), false)
	if err != nil {
		t.Fatal(err)
	}
	return &incidentServer{store: is, runs: runs}, runs
}

type listedIncidents struct {
	Incidents []struct {
		audit.Incident
		SeriesIDs []string `json:"series_ids"`
	} `json:"incidents"`
	Count     int  `json:"count"`
	Truncated bool `json:"truncated"`
}

func listIncidents(t *testing.T, srv *incidentServer, query string) (listedIncidents, int) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.handleList(rec, httptest.NewRequest(http.MethodGet, "/v1/incidents"+query, nil))
	var out listedIncidents
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return out, rec.Code
}

func TestIncidentHandlers_List_IncludeSeriesCarriesEveryRunsSeries(t *testing.T) {
	srv, runs := newLedgerServer(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	for _, r := range []audit.PlaybookRun{
		{RunID: "plr_e1", PlaybookID: "pb_1", SeriesID: "pbs_db_restart_triage", StartedAt: t0},
		{RunID: "plr_h1", PlaybookID: "pb_2", SeriesID: "pbs_pgbackrest_restore_remediate", PriorRunID: "plr_e1", EntryRunID: "plr_e1", StartedAt: t0.Add(time.Minute)},
	} {
		r := r
		if err := runs.Record(ctx, &r); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := json.Marshal(map[string]any{"entry_run_id": "plr_e1", "series_id": "pbs_db_restart_triage", "target_key": "prod-main"})
	rec := httptest.NewRecorder()
	srv.handleCreate(rec, httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(data)))
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}

	// Without the option the shape is as it always was: no series_ids.
	plain, _ := listIncidents(t, srv, "")
	if len(plain.Incidents) != 1 || plain.Incidents[0].SeriesIDs != nil {
		t.Errorf("a plain listing carries series: %+v", plain.Incidents)
	}
	with, code := listIncidents(t, srv, "?include=series")
	if code != http.StatusOK || len(with.Incidents) != 1 {
		t.Fatalf("%d %+v", code, with)
	}
	got := with.Incidents[0]
	if got.TargetKey != "prod-main" || got.EntryRunID != "plr_e1" {
		t.Errorf("the incident's own fields were lost: %+v", got.Incident)
	}
	if len(got.SeriesIDs) != 2 || got.SeriesIDs[0] != "pbs_db_restart_triage" || got.SeriesIDs[1] != "pbs_pgbackrest_restore_remediate" {
		t.Errorf("series_ids = %v", got.SeriesIDs)
	}
}

// With no run store the option is ignored, not an error: the listing still works.
func TestIncidentHandlers_List_IncludeSeriesWithoutARunStoreIsIgnored(t *testing.T) {
	srv := newIncidentServer(t)
	data, _ := json.Marshal(map[string]any{"entry_run_id": "plr_x"})
	srv.handleCreate(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(data)))
	got, code := listIncidents(t, srv, "?include=series")
	if code != http.StatusOK || len(got.Incidents) != 1 || got.Incidents[0].SeriesIDs != nil {
		t.Errorf("%d %+v", code, got)
	}
}

func TestIncidentHandlers_List_SinceAndUnattributed(t *testing.T) {
	srv, _ := newLedgerServer(t)
	for _, body := range []map[string]any{
		{"entry_run_id": "old", "detected_at": "2026-10-01T00:00:00Z", "resolved_at": "2026-10-01T01:00:00Z", "target_key": "a"},
		{"entry_run_id": "new", "detected_at": "2026-11-09T00:00:00Z", "resolved_at": "2026-11-09T01:00:00Z", "target_key": "a"},
		{"entry_run_id": "open", "detected_at": "2026-11-09T02:00:00Z", "target_input": "no-such-server"},
	} {
		data, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		srv.handleCreate(rec, httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(data)))
		if rec.Code != http.StatusCreated {
			t.Fatal(rec.Body.String())
		}
	}
	runs := func(query string) map[string]bool {
		t.Helper()
		got, code := listIncidents(t, srv, query)
		if code != http.StatusOK {
			t.Fatalf("%s: %d", query, code)
		}
		out := map[string]bool{}
		for _, inc := range got.Incidents {
			out[inc.EntryRunID] = true
		}
		return out
	}
	if got := runs("?since=2026-11-01T00:00:00Z"); !got["new"] || !got["open"] || got["old"] {
		t.Errorf("since kept %v, want new and open but not old", got)
	}
	if got := runs("?unattributed=true"); len(got) != 1 || !got["open"] {
		t.Errorf("unattributed = %v, want only the incident with no database", got)
	}
	if _, code := listIncidents(t, srv, "?since=yesterday"); code != http.StatusBadRequest {
		t.Errorf("a bad since: %d, want 400", code)
	}
}

// A full page may have left incidents out; the listing says so.
func TestIncidentHandlers_List_SaysWhenThePageIsFull(t *testing.T) {
	srv, _ := newLedgerServer(t)
	for i := 0; i < 3; i++ {
		data, _ := json.Marshal(map[string]any{"entry_run_id": "plr_" + string(rune('a'+i))})
		srv.handleCreate(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(data)))
	}
	if got, _ := listIncidents(t, srv, "?limit=3"); got.Count != 3 || !got.Truncated {
		t.Errorf("a page of exactly the limit: %+v (it may have left some out)", got)
	}
	if got, _ := listIncidents(t, srv, "?limit=10"); got.Count != 3 || got.Truncated {
		t.Errorf("a page under the limit: %+v", got)
	}
}

// The whole ledger path on real stores, with nothing mocked on the data side:
// incidents and runs written the way the gateway writes them (created at the
// entry, closed by a PATCH), read back through the listing the gateway uses, and
// run through the ledger. It checks the assumptions the unit tests of each piece
// share: that the stored times, statuses and series mean what the ledger takes
// them to mean.
func TestLedgerPipeline_RealStores(t *testing.T) {
	srv, runs := newLedgerServer(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }

	create := func(body map[string]any) {
		t.Helper()
		data, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		srv.handleCreate(rec, httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(data)))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %v: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	// closeIt is what the gateway does when the last run of an incident ends.
	closeIt := func(entryRun, status string, ended time.Duration) {
		t.Helper()
		got, err := srv.store.GetByEntryRunID(ctx, entryRun)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(map[string]any{"status": status, "resolved_at": at(ended)})
		req := httptest.NewRequest(http.MethodPatch, "/v1/incidents/"+got.IncidentID, bytes.NewReader(data))
		req.SetPathValue("incidentID", got.IncidentID)
		rec := httptest.NewRecorder()
		srv.handleUpdate(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("close %s: %d %s", entryRun, rec.Code, rec.Body.String())
		}
	}
	record := func(id, series, prior, entry string, started time.Duration) {
		t.Helper()
		if err := runs.Record(ctx, &audit.PlaybookRun{RunID: id, PlaybookID: "pb_" + id, SeriesID: series, PriorRunID: prior, EntryRunID: entry, StartedAt: now.Add(-started)}); err != nil {
			t.Fatal(err)
		}
	}

	// prod-orders: a PITR (triage that only reads, then the restore) that took 20
	// minutes; a vacuum; a restore that ended needing a human.
	record("plr_pitr", "pbs_pgbackrest_pitr_triage", "", "", 6*time.Hour)
	record("plr_pitr_h", "pbs_pgbackrest_pitr_remediate", "plr_pitr", "plr_pitr", 6*time.Hour-5*time.Minute)
	create(map[string]any{"entry_run_id": "plr_pitr", "series_id": "pbs_pgbackrest_pitr_triage", "target_key": "prod-orders", "detected_at": at(6 * time.Hour)})
	closeIt("plr_pitr", "resolved", 6*time.Hour-20*time.Minute)

	record("plr_vac", "pbs_vacuum_triage", "", "", 8*time.Hour)
	create(map[string]any{"entry_run_id": "plr_vac", "series_id": "pbs_vacuum_triage", "target_key": "prod-orders", "detected_at": at(8 * time.Hour)})
	closeIt("plr_vac", "resolved", 8*time.Hour-10*time.Minute)

	record("plr_human", "pbs_db_restart_triage", "", "", 3*time.Hour)
	create(map[string]any{"entry_run_id": "plr_human", "series_id": "pbs_db_restart_triage", "target_key": "prod-orders", "detected_at": at(3 * time.Hour)})
	closeIt("plr_human", "needs_human", 3*time.Hour-15*time.Minute)

	// Not prod-orders': a faulttest one on the same database, another database's,
	// one with no database, and one resolved before the window.
	record("plr_ft", "pbs_db_restart_triage", "", "", time.Hour)
	create(map[string]any{"entry_run_id": "plr_ft", "series_id": "pbs_db_restart_triage", "target_key": "prod-orders", "origin": "faulttest", "detected_at": at(time.Hour)})
	closeIt("plr_ft", "resolved", 30*time.Minute)
	record("plr_other", "pbs_db_restart_triage", "", "", time.Hour)
	create(map[string]any{"entry_run_id": "plr_other", "series_id": "pbs_db_restart_triage", "target_key": "prod-other", "detected_at": at(time.Hour)})
	record("plr_loose", "pbs_db_restart_triage", "", "", time.Hour)
	create(map[string]any{"entry_run_id": "plr_loose", "series_id": "pbs_db_restart_triage", "target_input": "no-such-server", "detected_at": at(time.Hour)})
	record("plr_ancient", "pbs_db_restart_triage", "", "", 40*24*time.Hour)
	create(map[string]any{"entry_run_id": "plr_ancient", "series_id": "pbs_db_restart_triage", "target_key": "prod-orders", "detected_at": at(40 * 24 * time.Hour)})
	closeIt("plr_ancient", "resolved", 39*24*time.Hour)

	// What the gateway asks for: a 30-day window, written with a zone offset to
	// check the since filter is not fooled by one.
	zone := time.FixedZone("IST", 5*3600+1800)
	since := now.Add(-30 * 24 * time.Hour).In(zone).Format(time.RFC3339)
	query := "?origin=real&target_key=prod-orders&include=series&limit=200&since=" + neturlEscape(since)
	listed, code := listIncidents(t, srv, query)
	if code != http.StatusOK || listed.Truncated {
		t.Fatalf("%d, truncated %v", code, listed.Truncated)
	}

	var incs []availability.Incident
	for _, i := range listed.Incidents {
		incs = append(incs, availability.Incident{ID: i.IncidentID, SeriesIDs: i.SeriesIDs, Status: i.Status, Origin: i.Origin,
			DetectedAt: i.DetectedAt, ResolvedAt: i.ResolvedAt})
	}
	l := availability.Compute(availability.Params{
		Database: "prod-orders", Now: now, Incidents: incs, Classify: playbooks.AvailabilityImpact,
		Budget: infra.ComputeBudget(99.9, 30*24*time.Hour, "environment:prod", 30*time.Minute, true),
	})

	if len(listed.Incidents) != 3 {
		t.Fatalf("the listing returned %d incidents, want prod-orders' three real, in-window ones", len(listed.Incidents))
	}
	// The PITR is charged for its 20 minutes: detection to the end of its last run,
	// and charged as an outage because of its second run, though its first only reads.
	if len(l.Charged) != 1 || l.Charged[0].DurationSeconds != 1200 || l.ChargedSeconds != 1200 {
		t.Errorf("charged = %+v", l.Charged)
	}
	if len(l.Unconfirmed) != 1 || l.Unconfirmed[0].DurationSeconds != 900 {
		t.Errorf("unconfirmed = %+v", l.Unconfirmed)
	}
	if len(l.None) != 1 || l.None[0].DurationSeconds != 600 {
		t.Errorf("none = %+v", l.None)
	}
	if l.RemainingSeconds == nil || *l.RemainingSeconds != 2592-1200 || *l.RemainingWorstCaseSeconds != 2592-1200-900 {
		t.Errorf("remaining %v / worst case %v", l.RemainingSeconds, l.RemainingWorstCaseSeconds)
	}

	// And the incidents no database could be found for are counted, not lost.
	loose, _ := listIncidents(t, srv, "?origin=real&unattributed=true&limit=200&since="+neturlEscape(since))
	if loose.Count != 1 || loose.Incidents[0].EntryRunID != "plr_loose" || loose.Incidents[0].TargetInput != "no-such-server" {
		t.Errorf("unattributed = %+v", loose)
	}
}

func neturlEscape(s string) string { return url.QueryEscape(s) }

func TestIncidentHandlers_List_StoreFailureIs500(t *testing.T) {
	store, err := audit.NewStore(audit.StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	is, _ := audit.NewIncidentStore(store.DB(), false)
	runs, _ := audit.NewPlaybookRunStore(store.DB(), false)
	srv := &incidentServer{store: is, runs: runs}
	store.Close() // every query now fails
	for _, q := range []string{"", "?include=series"} {
		if _, code := listIncidents(t, srv, q); code != http.StatusInternalServerError {
			t.Errorf("list%s with a dead database = %d, want 500, not an empty listing", q, code)
		}
	}
}

func TestIncidentHandlers_Create_InvalidJSON(t *testing.T) {
	srv := newIncidentServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()
	srv.handleCreate(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestIncidentHandlers_GetAndUpdate_RoundTrip(t *testing.T) {
	srv := newIncidentServer(t)

	body, _ := json.Marshal(map[string]any{"trace_id": "tr_abc", "origin": "faulttest"})
	createReq := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(body))
	createRec := httptest.NewRecorder()
	srv.handleCreate(createRec, createReq)
	var created audit.Incident
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	// GET before update.
	getReq := httptest.NewRequest(http.MethodGet, "/v1/incidents/"+created.IncidentID, nil)
	getReq.SetPathValue("incidentID", created.IncidentID)
	getRec := httptest.NewRecorder()
	srv.handleGet(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body: %s", getRec.Code, getRec.Body.String())
	}
	var fetched audit.Incident
	if err := json.NewDecoder(getRec.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if fetched.Origin != "faulttest" {
		t.Errorf("origin = %q, want faulttest", fetched.Origin)
	}

	// PATCH status + attribution.
	patchBody, _ := json.Marshal(map[string]any{"status": "resolved", "attribution": "oom-kill"})
	patchReq := httptest.NewRequest(http.MethodPatch, "/v1/incidents/"+created.IncidentID, bytes.NewReader(patchBody))
	patchReq.SetPathValue("incidentID", created.IncidentID)
	patchRec := httptest.NewRecorder()
	srv.handleUpdate(patchRec, patchReq)
	if patchRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH status = %d, want 204; body: %s", patchRec.Code, patchRec.Body.String())
	}

	// GET after update confirms the patch landed and origin (untouched) survived.
	getReq2 := httptest.NewRequest(http.MethodGet, "/v1/incidents/"+created.IncidentID, nil)
	getReq2.SetPathValue("incidentID", created.IncidentID)
	getRec2 := httptest.NewRecorder()
	srv.handleGet(getRec2, getReq2)
	var after audit.Incident
	if err := json.NewDecoder(getRec2.Body).Decode(&after); err != nil {
		t.Fatalf("decode second get response: %v", err)
	}
	if after.Status != "resolved" || after.Attribution != "oom-kill" {
		t.Errorf("after patch: status=%q attribution=%q, want resolved/oom-kill", after.Status, after.Attribution)
	}
	if after.Origin != "faulttest" {
		t.Errorf("origin = %q after unrelated patch, want unchanged faulttest", after.Origin)
	}
}

func TestIncidentHandlers_Get_NotFound(t *testing.T) {
	srv := newIncidentServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/incidents/inc_missing", nil)
	req.SetPathValue("incidentID", "inc_missing")
	rec := httptest.NewRecorder()
	srv.handleGet(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestIncidentHandlers_Update_NotFound(t *testing.T) {
	srv := newIncidentServer(t)
	body, _ := json.Marshal(map[string]any{"status": "resolved"})
	req := httptest.NewRequest(http.MethodPatch, "/v1/incidents/inc_missing", bytes.NewReader(body))
	req.SetPathValue("incidentID", "inc_missing")
	rec := httptest.NewRecorder()
	srv.handleUpdate(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestIncidentHandlers_Update_InvalidResolvedAt(t *testing.T) {
	srv := newIncidentServer(t)

	createReq := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader([]byte("{}")))
	createRec := httptest.NewRecorder()
	srv.handleCreate(createRec, createReq)
	var created audit.Incident
	json.NewDecoder(createRec.Body).Decode(&created) //nolint:errcheck

	body, _ := json.Marshal(map[string]any{"resolved_at": "not-a-timestamp"})
	req := httptest.NewRequest(http.MethodPatch, "/v1/incidents/"+created.IncidentID, bytes.NewReader(body))
	req.SetPathValue("incidentID", created.IncidentID)
	rec := httptest.NewRecorder()
	srv.handleUpdate(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unparseable resolved_at", rec.Code)
	}
}

func TestIncidentHandlers_List_Filters(t *testing.T) {
	srv := newIncidentServer(t)

	for _, body := range []map[string]any{
		{"origin": "real"},
		{"origin": "faulttest"},
		{"origin": "faulttest"},
	} {
		data, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(data))
		rec := httptest.NewRecorder()
		srv.handleCreate(rec, req)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/incidents?origin=faulttest", nil)
	rec := httptest.NewRecorder()
	srv.handleList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Incidents []audit.Incident `json:"incidents"`
		Count     int              `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Count != 2 {
		t.Errorf("count = %d, want 2", result.Count)
	}
}

func TestIncidentHandlers_List_SeriesIDQueryParam(t *testing.T) {
	srv := newIncidentServer(t)

	for _, body := range []map[string]any{
		{"series_id": "pbs_k8s_pod_crash_triage"},
		{"series_id": "pbs_k8s_pod_crash_triage"},
		{"series_id": "pbs_replication_lag"},
	} {
		data, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(data))
		rec := httptest.NewRecorder()
		srv.handleCreate(rec, req)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/incidents?series_id=pbs_k8s_pod_crash_triage", nil)
	rec := httptest.NewRecorder()
	srv.handleList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Incidents []audit.Incident `json:"incidents"`
		Count     int              `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Count != 2 {
		t.Errorf("count = %d, want 2", result.Count)
	}
}

func TestIncidentHandlers_List_LimitQueryParam(t *testing.T) {
	srv := newIncidentServer(t)

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader([]byte("{}")))
		rec := httptest.NewRecorder()
		srv.handleCreate(rec, req)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/incidents?limit=2", nil)
	rec := httptest.NewRecorder()
	srv.handleList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Incidents []audit.Incident `json:"incidents"`
		Count     int              `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Count != 2 {
		t.Errorf("count = %d, want 2 (?limit=2 should be forwarded to the store)", result.Count)
	}

	// A non-numeric limit is ignored rather than rejected — falls back to the
	// store's default rather than erroring.
	reqBad := httptest.NewRequest(http.MethodGet, "/v1/incidents?limit=notanumber", nil)
	recBad := httptest.NewRecorder()
	srv.handleList(recBad, reqBad)
	if recBad.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a non-numeric limit (should be ignored, not rejected)", recBad.Code)
	}
	var resultBad struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(recBad.Body).Decode(&resultBad); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resultBad.Count != 5 {
		t.Errorf("count = %d, want all 5 when limit is unparseable", resultBad.Count)
	}
}

func TestIncidentHandlers_GetByEntryRunID(t *testing.T) {
	srv := newIncidentServer(t)

	body, _ := json.Marshal(map[string]any{"entry_run_id": "plr_findme"})
	createReq := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(body))
	createRec := httptest.NewRecorder()
	srv.handleCreate(createRec, createReq)
	var created audit.Incident
	json.NewDecoder(createRec.Body).Decode(&created) //nolint:errcheck

	req := httptest.NewRequest(http.MethodGet, "/v1/incidents/by-run/plr_findme", nil)
	req.SetPathValue("runID", "plr_findme")
	rec := httptest.NewRecorder()
	srv.handleGetByEntryRunID(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var got audit.Incident
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.IncidentID != created.IncidentID {
		t.Errorf("incident_id = %q, want %q", got.IncidentID, created.IncidentID)
	}

	notFoundReq := httptest.NewRequest(http.MethodGet, "/v1/incidents/by-run/plr_never", nil)
	notFoundReq.SetPathValue("runID", "plr_never")
	notFoundRec := httptest.NewRecorder()
	srv.handleGetByEntryRunID(notFoundRec, notFoundReq)
	if notFoundRec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", notFoundRec.Code)
	}
}

func TestIncidentHandlers_GetByTraceID(t *testing.T) {
	srv := newIncidentServer(t)

	body, _ := json.Marshal(map[string]any{"trace_id": "tr_findme", "entry_run_id": "plr_x"})
	createReq := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(body))
	createRec := httptest.NewRecorder()
	srv.handleCreate(createRec, createReq)
	var created audit.Incident
	json.NewDecoder(createRec.Body).Decode(&created) //nolint:errcheck

	req := httptest.NewRequest(http.MethodGet, "/v1/incidents/by-trace/tr_findme", nil)
	req.SetPathValue("traceID", "tr_findme")
	rec := httptest.NewRecorder()
	srv.handleGetByTraceID(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var got audit.Incident
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.IncidentID != created.IncidentID {
		t.Errorf("incident_id = %q, want %q", got.IncidentID, created.IncidentID)
	}

	notFoundReq := httptest.NewRequest(http.MethodGet, "/v1/incidents/by-trace/tr_never", nil)
	notFoundReq.SetPathValue("traceID", "tr_never")
	notFoundRec := httptest.NewRecorder()
	srv.handleGetByTraceID(notFoundRec, notFoundReq)
	if notFoundRec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", notFoundRec.Code)
	}
}

func TestIncidentHandlers_Update_DraftPlaybookID(t *testing.T) {
	srv := newIncidentServer(t)

	createReq := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader([]byte("{}")))
	createRec := httptest.NewRecorder()
	srv.handleCreate(createRec, createReq)
	var created audit.Incident
	json.NewDecoder(createRec.Body).Decode(&created) //nolint:errcheck

	patchBody, _ := json.Marshal(map[string]any{"draft_playbook_id": "pb_draft01"})
	patchReq := httptest.NewRequest(http.MethodPatch, "/v1/incidents/"+created.IncidentID, bytes.NewReader(patchBody))
	patchReq.SetPathValue("incidentID", created.IncidentID)
	patchRec := httptest.NewRecorder()
	srv.handleUpdate(patchRec, patchReq)
	if patchRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH status = %d, want 204; body: %s", patchRec.Code, patchRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/incidents/"+created.IncidentID, nil)
	getReq.SetPathValue("incidentID", created.IncidentID)
	getRec := httptest.NewRecorder()
	srv.handleGet(getRec, getReq)
	var after audit.Incident
	json.NewDecoder(getRec.Body).Decode(&after) //nolint:errcheck
	if after.DraftPlaybookID != "pb_draft01" {
		t.Errorf("draft_playbook_id = %q, want pb_draft01", after.DraftPlaybookID)
	}
}

func TestIncidentHandlers_Update_TraceIDBackfill(t *testing.T) {
	srv := newIncidentServer(t)

	createReq := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader([]byte("{}")))
	createRec := httptest.NewRecorder()
	srv.handleCreate(createRec, createReq)
	var created audit.Incident
	json.NewDecoder(createRec.Body).Decode(&created) //nolint:errcheck
	if created.TraceID != "" {
		t.Fatalf("expected empty trace_id at creation, got %q", created.TraceID)
	}

	patchBody, _ := json.Marshal(map[string]any{"trace_id": "tr_backfilled01"})
	patchReq := httptest.NewRequest(http.MethodPatch, "/v1/incidents/"+created.IncidentID, bytes.NewReader(patchBody))
	patchReq.SetPathValue("incidentID", created.IncidentID)
	patchRec := httptest.NewRecorder()
	srv.handleUpdate(patchRec, patchReq)
	if patchRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH status = %d, want 204; body: %s", patchRec.Code, patchRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/incidents/"+created.IncidentID, nil)
	getReq.SetPathValue("incidentID", created.IncidentID)
	getRec := httptest.NewRecorder()
	srv.handleGet(getRec, getReq)
	var after audit.Incident
	json.NewDecoder(getRec.Body).Decode(&after) //nolint:errcheck
	if after.TraceID != "tr_backfilled01" {
		t.Errorf("trace_id = %q, want tr_backfilled01", after.TraceID)
	}
}
