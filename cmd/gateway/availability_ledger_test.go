package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"helpdesk/internal/availability"
	"helpdesk/internal/infra"
)

// ledgerAuditd stands in for auditd's incident listing and records every query.
type ledgerAuditd struct {
	mu         sync.Mutex
	queries    []url.Values
	mine       string // JSON of the incidents for a database
	loose      string // JSON of the unattributed incidents
	status     int    // non-zero: answer with this instead
	looseFails bool   // only the unattributed query fails
}

func (a *ledgerAuditd) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.queries = append(a.queries, r.URL.Query())
		a.mu.Unlock()
		if r.URL.Path != "/v1/incidents" {
			http.NotFound(w, r)
			return
		}
		if a.status != 0 {
			http.Error(w, "boom", a.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("unattributed") == "true" {
			if a.looseFails {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			w.Write([]byte(a.loose)) //nolint:errcheck
			return
		}
		w.Write([]byte(a.mine)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

func ledgerInfra() *infra.Config {
	return &infra.Config{
		Environments: map[string]infra.Environment{"prod": {Objectives: infra.Objectives{
			RTO:                &infra.Duration{Duration: 30 * time.Minute},
			AvailabilitySLO:    &infra.Percent{Value: 99.9},
			AvailabilityWindow: &infra.Duration{Duration: 7 * 24 * time.Hour},
		}}},
		DBServers: map[string]infra.DBServer{
			"prod-orders": {Name: "orders", ConnectionString: "host=db1.example.com port=5432 dbname=o password=s3cret", Environment: "prod"},
			"prod-other":  {Name: "other", ConnectionString: "host=db2.example.com port=5432 dbname=o", Environment: "prod"},
			"scratch":     {Name: "scratch", ConnectionString: "host=db3.example.com port=5433 dbname=s"},
		},
	}
}

func getLedger(t *testing.T, gw *Gateway, query string) (*httptest.ResponseRecorder, availability.Ledger) {
	t.Helper()
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/availability-ledger"+query, nil))
	var l availability.Ledger
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&l); err != nil {
			t.Fatalf("decode: %v\n%s", err, rec.Body.String())
		}
	}
	return rec, l
}

func hoursAgo(h float64) string {
	return time.Now().UTC().Add(-time.Duration(h * float64(time.Hour))).Format(time.RFC3339)
}

func TestAvailabilityLedger_BuildsTheLedgerFromAuditdAndTheObjectives(t *testing.T) {
	mine := `{"count": 3, "truncated": false, "incidents": [
	  {"incident_id": "inc_restore", "origin": "real", "status": "resolved", "series_id": "pbs_db_restart_triage",
	   "series_ids": ["pbs_db_restart_triage", "pbs_pgbackrest_restore_remediate"],
	   "detected_at": "` + hoursAgo(5) + `", "resolved_at": "` + hoursAgo(5-0.25) + `"},
	  {"incident_id": "inc_vacuum", "origin": "real", "status": "resolved", "series_ids": ["pbs_vacuum_triage"],
	   "detected_at": "` + hoursAgo(9) + `", "resolved_at": "` + hoursAgo(8.9) + `"},
	  {"incident_id": "inc_custom", "origin": "real", "status": "resolved", "series_id": "pbs_made_up",
	   "detected_at": "` + hoursAgo(20) + `", "resolved_at": "` + hoursAgo(19.9) + `"}]}`
	aud := &ledgerAuditd{mine: mine, loose: `{"count": 2, "truncated": false, "incidents": []}`}
	srv := aud.server(t)
	gw := &Gateway{auditURL: srv.URL, infra: ledgerInfra()}

	rec, l := getLedger(t, gw, "?database=prod-orders")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if l.Database != "prod-orders" {
		t.Errorf("database = %q", l.Database)
	}
	// 99.9% over the environment's 7 days: 604.8s, so 605s.
	if l.WindowSeconds != 7*86400 || l.WindowSource != "environment:prod" || l.BudgetSeconds == nil || *l.BudgetSeconds != 605 {
		t.Errorf("window %ds from %q, budget %v", l.WindowSeconds, l.WindowSource, l.BudgetSeconds)
	}
	// The restore is a 15m outage (900s), charged; vacuum is harmless; the made-up series is unclassified.
	if len(l.Charged) != 1 || l.Charged[0].IncidentID != "inc_restore" || l.Charged[0].DurationSeconds != 900 {
		t.Errorf("charged = %+v", l.Charged)
	}
	if len(l.None) != 1 || len(l.Unclassified) != 1 {
		t.Errorf("none %+v, unclassified %+v", l.None, l.Unclassified)
	}
	if !l.Overspent || l.RemainingSeconds == nil || *l.RemainingSeconds != 605-900 {
		t.Errorf("remaining = %v, overspent %v", l.RemainingSeconds, l.Overspent)
	}
	if l.Unattributed != 2 || !strings.Contains(strings.Join(l.Notes, "\n"), "2 incident(s) in the window could not be tied to any database") {
		t.Errorf("unattributed = %d, notes %v", l.Unattributed, l.Notes)
	}
	if !strings.Contains(l.Basis, "from detection to recovery") {
		t.Errorf("basis = %q", l.Basis)
	}
}

// What is asked of auditd: real incidents, for this database, over this window,
// with their series; and, separately, the ones no database could be found for.
func TestAvailabilityLedger_AsksAuditdForTheRightIncidents(t *testing.T) {
	aud := &ledgerAuditd{mine: `{"incidents": []}`, loose: `{"incidents": [], "count": 0}`}
	srv := aud.server(t)
	gw := &Gateway{auditURL: srv.URL, infra: ledgerInfra()}
	before := time.Now().UTC()

	if rec, _ := getLedger(t, gw, "?database=prod-orders"); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if len(aud.queries) != 2 {
		t.Fatalf("auditd was asked %d times: %v", len(aud.queries), aud.queries)
	}
	var mine, loose url.Values
	for _, q := range aud.queries {
		if q.Get("unattributed") == "true" {
			loose = q
		} else {
			mine = q
		}
	}
	if mine.Get("target_key") != "prod-orders" || mine.Get("origin") != "real" || mine.Get("include") != "series" || mine.Get("limit") != "200" {
		t.Errorf("the database's query = %v", mine)
	}
	if loose.Get("origin") != "real" || loose.Get("target_key") != "" {
		t.Errorf("the unattributed query = %v", loose)
	}
	// The window starts 7 days (this environment's) before now.
	since, err := time.Parse(time.RFC3339, mine.Get("since"))
	if err != nil {
		t.Fatalf("since = %q: %v", mine.Get("since"), err)
	}
	if d := before.Add(-7 * 24 * time.Hour).Sub(since); d > 5*time.Second || d < -5*time.Second {
		t.Errorf("since = %v, want 7 days before now", since)
	}
}

// However the database is named, the same ledger comes back: the key, its name,
// a connection string with any password.
func TestAvailabilityLedger_ResolvesTheDatabaseTheWayTheAgentsDo(t *testing.T) {
	aud := &ledgerAuditd{mine: `{"incidents": []}`, loose: `{"incidents": []}`}
	srv := aud.server(t)
	gw := &Gateway{auditURL: srv.URL, infra: ledgerInfra()}
	for _, db := range []string{
		"prod-orders", "orders",
		"host=db1.example.com port=5432 dbname=o password=another",
		"postgres://u:pw@db1.example.com:5432/o",
	} {
		_, l := getLedger(t, gw, "?database="+url.QueryEscape(db))
		if l.Database != "prod-orders" {
			t.Errorf("%q resolved to %q", db, l.Database)
		}
	}
	// A database with no objectives still gets a ledger: the spend, and no budget.
	_, l := getLedger(t, gw, "?database=scratch")
	if l.Database != "scratch" || l.BudgetSeconds != nil || l.WindowSource != infra.SourceBuiltin {
		t.Errorf("scratch: %+v", l)
	}
}

func TestAvailabilityLedger_Refusals(t *testing.T) {
	aud := &ledgerAuditd{mine: `{"incidents": []}`, loose: `{"incidents": []}`}
	srv := aud.server(t)
	gw := &Gateway{auditURL: srv.URL, infra: ledgerInfra()}
	for _, tc := range []struct {
		name, query string
		gw          *Gateway
		want        int
		msg         string
	}{
		{"no database", "", gw, http.StatusBadRequest, "database is required"},
		{"unknown database", "?database=nope", gw, http.StatusNotFound, "is not in the infrastructure configuration"},
		// Both prod servers listen on 5432: a bare port is not enough to say which.
		{"an ambiguous database", "?database=5432", gw, http.StatusConflict, "refusing to guess"},
		{"no infrastructure config", "?database=prod-orders", &Gateway{auditURL: srv.URL}, http.StatusNotFound, "no infrastructure configuration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := getLedger(t, tc.gw, tc.query)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.msg) {
				t.Errorf("%d %s, want %d containing %q", rec.Code, rec.Body.String(), tc.want, tc.msg)
			}
		})
	}
}

// If the incidents cannot be read, the ledger is not guessed at.
func TestAvailabilityLedger_AuditdFailureIsAnErrorNotAnEmptyLedger(t *testing.T) {
	aud := &ledgerAuditd{status: http.StatusInternalServerError}
	srv := aud.server(t)
	rec, _ := getLedger(t, &Gateway{auditURL: srv.URL, infra: ledgerInfra()}, "?database=prod-orders")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "could not read the incidents") {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
	rec, _ = getLedger(t, &Gateway{infra: ledgerInfra()}, "?database=prod-orders")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "no audit service is configured") {
		t.Errorf("no auditd: %d %s", rec.Code, rec.Body.String())
	}
}

// A ledger missing half of what it reports on is not returned as if whole: if the
// count of unattributed incidents cannot be read, or auditd answers with
// something that is not a listing, it is an error.
func TestAvailabilityLedger_AHalfReadLedgerIsAnError(t *testing.T) {
	for _, tc := range []struct {
		name string
		aud  *ledgerAuditd
	}{
		{"the unattributed query fails", &ledgerAuditd{mine: `{"incidents": []}`, looseFails: true}},
		{"auditd answers with something that is not JSON", &ledgerAuditd{mine: `<html>not a listing</html>`, loose: `{"incidents": []}`}},
		{"the unattributed answer is not JSON", &ledgerAuditd{mine: `{"incidents": []}`, loose: `nope`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := tc.aud.server(t)
			rec, _ := getLedger(t, &Gateway{auditURL: srv.URL, infra: ledgerInfra()}, "?database=prod-orders")
			if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "could not read the incidents") {
				t.Errorf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// The service key is sent, and a bare auditd that wants none is still read.
func TestAvailabilityLedger_SendsTheAuditKey(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		w.Write([]byte(`{"incidents": []}`)) //nolint:errcheck
	}))
	defer srv.Close()
	if rec, _ := getLedger(t, &Gateway{auditURL: srv.URL, auditAPIKey: "svc-key", infra: ledgerInfra()}, "?database=prod-orders"); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if len(got) != 2 || got[0] != "Bearer svc-key" || got[1] != "Bearer svc-key" {
		t.Errorf("authorization headers = %v", got)
	}
}

func TestAvailabilityLedger_SaysWhenAuditdsPageWasFull(t *testing.T) {
	aud := &ledgerAuditd{mine: `{"incidents": [], "truncated": true}`, loose: `{"incidents": [], "count": 0}`}
	srv := aud.server(t)
	_, l := getLedger(t, &Gateway{auditURL: srv.URL, infra: ledgerInfra()}, "?database=prod-orders")
	if !l.Truncated || !strings.Contains(strings.Join(l.Notes, "\n"), "cut off at its page limit") {
		t.Errorf("truncated = %v, notes %v", l.Truncated, l.Notes)
	}
}

// A listing with no series_ids (an older auditd) still classifies by the
// incident's own entry series.
func TestAvailabilityLedger_FallsBackToTheIncidentsEntrySeries(t *testing.T) {
	mine := `{"incidents": [{"incident_id": "inc_old", "origin": "real", "status": "resolved", "series_id": "pbs_db_restart_triage",
	  "detected_at": "` + hoursAgo(3) + `", "resolved_at": "` + hoursAgo(3-0.1) + `"}]}`
	aud := &ledgerAuditd{mine: mine, loose: `{"incidents": []}`}
	srv := aud.server(t)
	_, l := getLedger(t, &Gateway{auditURL: srv.URL, infra: ledgerInfra()}, "?database=prod-orders")
	if len(l.Charged) != 1 || l.Charged[0].DurationSeconds != 360 {
		t.Errorf("charged = %+v", l.Charged)
	}
}
