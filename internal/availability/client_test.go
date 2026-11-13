package availability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"helpdesk/internal/infra"
)

type recordingAuditd struct {
	mu      sync.Mutex
	queries []url.Values
	auth    []string
	mine    string
	loose   string
	status  int
}

func (a *recordingAuditd) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.queries = append(a.queries, r.URL.Query())
		a.auth = append(a.auth, r.Header.Get("Authorization"))
		a.mu.Unlock()
		if r.URL.Path != "/v1/incidents" {
			http.NotFound(w, r)
			return
		}
		if a.status != 0 {
			http.Error(w, "boom", a.status)
			return
		}
		if r.URL.Query().Get("unattributed") == "true" {
			w.Write([]byte(a.loose)) //nolint:errcheck
			return
		}
		w.Write([]byte(a.mine)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClient_IncidentsAsksForRealIncidentsAndAFullPage(t *testing.T) {
	aud := &recordingAuditd{mine: `{"incidents": [], "count": 0}`}
	srv := aud.serve(t)
	c := &Client{URL: srv.URL + "/", APIKey: "svc-key"}
	caller := url.Values{"target_key": {"prod-orders"}, "origin": {"faulttest"}, "limit": {"5"}}

	if _, err := c.Incidents(context.Background(), caller); err != nil {
		t.Fatal(err)
	}
	q := aud.queries[0]
	// Only real incidents ever count, and a full page: whatever the caller said.
	if q.Get("origin") != "real" || q.Get("limit") != "200" || q.Get("target_key") != "prod-orders" {
		t.Errorf("query = %v", q)
	}
	// The caller's own values are not changed under it.
	if caller.Get("origin") != "faulttest" || caller.Get("limit") != "5" {
		t.Errorf("the caller's query was modified: %v", caller)
	}
	if aud.auth[0] != "Bearer svc-key" {
		t.Errorf("authorization = %q", aud.auth[0])
	}
}

func TestClient_IncidentsErrors(t *testing.T) {
	if _, err := (*Client)(nil).Incidents(context.Background(), nil); !errors.Is(err, ErrNoAudit) {
		t.Errorf("nil client: %v", err)
	}
	if _, err := (&Client{}).Incidents(context.Background(), nil); !errors.Is(err, ErrNoAudit) {
		t.Errorf("no URL: %v", err)
	}
	down := &Client{URL: "http://127.0.0.1:1"}
	if _, err := down.Incidents(context.Background(), nil); err == nil {
		t.Error("an unreachable auditd was not an error")
	}
	bad := &recordingAuditd{status: http.StatusInternalServerError}
	srv := bad.serve(t)
	if _, err := (&Client{URL: srv.URL}).Incidents(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "auditd returned 500: boom") {
		t.Errorf("a 500: %v", err)
	}
	junk := &recordingAuditd{mine: `<html>`}
	srv2 := junk.serve(t)
	if _, err := (&Client{URL: srv2.URL}).Incidents(context.Background(), nil); err == nil {
		t.Error("an answer that is not a listing was not an error")
	}
}

func TestClient_LedgerUsesTheBudgetsWindowAndMapsTheListing(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	aud := &recordingAuditd{
		mine: `{"count": 3, "truncated": false, "incidents": [
		  {"incident_id": "inc_restore", "origin": "real", "status": "resolved", "series_id": "pbs_db_restart_triage",
		   "series_ids": ["pbs_db_restart_triage", "pbs_pgbackrest_restore_remediate"], "detected_at": "` + at(5*time.Hour) + `", "resolved_at": "` + at(5*time.Hour-15*time.Minute) + `"},
		  {"incident_id": "inc_old_auditd", "origin": "real", "status": "resolved", "series_id": "pbs_db_restart_triage",
		   "detected_at": "` + at(3*time.Hour) + `", "resolved_at": "` + at(3*time.Hour-6*time.Minute) + `"},
		  {"incident_id": "inc_vacuum", "origin": "real", "status": "resolved", "series_ids": ["pbs_vacuum_triage"],
		   "detected_at": "` + at(9*time.Hour) + `", "resolved_at": "` + at(9*time.Hour-time.Minute) + `"}]}`,
		loose: `{"count": 2, "truncated": true, "incidents": []}`,
	}
	srv := aud.serve(t)
	c := &Client{URL: srv.URL}
	budget := infra.ComputeBudget(99.9, 7*24*time.Hour, "environment:prod", 30*time.Minute, true)

	l, err := c.Ledger(context.Background(), "prod-orders", budget, now)
	if err != nil {
		t.Fatal(err)
	}
	// 15 minutes (restore) and 6 minutes (an older auditd sent no series_ids: the entry series is used).
	if l.ChargedSeconds != 15*60+6*60 || len(l.Charged) != 2 || len(l.None) != 1 {
		t.Errorf("charged %ds in %d, none %d", l.ChargedSeconds, len(l.Charged), len(l.None))
	}
	if l.WindowSeconds != 7*86400 || l.WindowSource != "environment:prod" || l.Database != "prod-orders" {
		t.Errorf("window %d from %q for %q", l.WindowSeconds, l.WindowSource, l.Database)
	}
	// What was asked, in two queries: this database's (with series) and the unattributed count.
	if len(aud.queries) != 2 {
		t.Fatalf("queries = %v", aud.queries)
	}
	var mine, loose url.Values
	for _, q := range aud.queries {
		if q.Get("unattributed") == "true" {
			loose = q
		} else {
			mine = q
		}
	}
	if mine.Get("target_key") != "prod-orders" || mine.Get("include") != "series" || loose.Get("target_key") != "" {
		t.Errorf("mine = %v, loose = %v", mine, loose)
	}
	// Only real incidents can be missing from a ledger: faulttest ones never count.
	if loose.Get("origin") != "real" {
		t.Errorf("the unattributed count asked for origin %q, want real", loose.Get("origin"))
	}
	since, err := time.Parse(time.RFC3339, mine.Get("since"))
	if err != nil || now.Add(-7*24*time.Hour).Sub(since) > time.Second || since.Sub(now.Add(-7*24*time.Hour)) > time.Second {
		t.Errorf("since = %q (%v), want 7 days before now", mine.Get("since"), err)
	}
	// Either page being full makes the ledger say so; the unattributed ones are counted.
	if !l.Truncated || l.Unattributed != 2 {
		t.Errorf("truncated %v, unattributed %d", l.Truncated, l.Unattributed)
	}
}

// An entry that carries no incident at all (a listing with an empty object in it)
// is skipped, not dereferenced.
func TestClient_LedgerSkipsAListingEntryWithNoIncident(t *testing.T) {
	aud := &recordingAuditd{
		mine:  `{"incidents": [{}, {"series_ids": ["pbs_vacuum_triage"]}], "count": 2}`,
		loose: `{"incidents": []}`,
	}
	srv := aud.serve(t)
	l, err := (&Client{URL: srv.URL}).Ledger(context.Background(), "x", budget(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Charged)+len(l.None)+len(l.Degraded)+len(l.Unclassified)+len(l.Unconfirmed) != 0 {
		t.Errorf("an empty entry became an incident: %+v", l)
	}
}

func TestClient_LedgerWithNoBudgetUsesTheDefaultWindow(t *testing.T) {
	aud := &recordingAuditd{mine: `{"incidents": []}`, loose: `{"incidents": []}`}
	srv := aud.serve(t)
	l, err := (&Client{URL: srv.URL}).Ledger(context.Background(), "scratch", infra.AvailabilityBudget{Detail: "no availability_slo is set: no budget"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if l.WindowSeconds != 30*86400 || l.WindowSource != infra.SourceBuiltin || l.BudgetSeconds != nil {
		t.Errorf("window %d from %q, budget %v", l.WindowSeconds, l.WindowSource, l.BudgetSeconds)
	}
}

// A ledger is never built from a half-read picture.
func TestClient_LedgerFailsWhenEitherReadFails(t *testing.T) {
	if _, err := (&Client{}).Ledger(context.Background(), "x", infra.AvailabilityBudget{}, time.Now()); !errors.Is(err, ErrNoAudit) {
		t.Errorf("no auditd: %v", err)
	}
	// Only the second (unattributed) read fails.
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/incidents", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("unattributed") == "true" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"incidents": []}`)) //nolint:errcheck
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if _, err := (&Client{URL: srv.URL}).Ledger(context.Background(), "x", infra.AvailabilityBudget{}, time.Now()); err == nil {
		t.Error("a ledger was returned although the unattributed count could not be read")
	}
}
