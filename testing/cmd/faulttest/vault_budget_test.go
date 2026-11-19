package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/availability"
	"helpdesk/internal/infra"
	"helpdesk/playbooks"
)

var ledgerNow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func ledgerFixture(incs ...availability.Incident) availability.Ledger {
	return availability.Compute(availability.Params{
		Database: "prod-orders", Now: ledgerNow, Incidents: incs, Classify: playbooks.AvailabilityImpact,
		Budget: infra.ComputeBudget(99.9, 30*24*time.Hour, "environment:prod", 30*time.Minute, true),
	})
}

func lInc(id, status string, series []string, start, end time.Duration) availability.Incident {
	inc := availability.Incident{ID: id, Status: status, Origin: audit.IncidentOriginReal, SeriesIDs: series, DetectedAt: ledgerNow.Add(-start)}
	if end > 0 {
		inc.ResolvedAt = ledgerNow.Add(-end)
	}
	return inc
}

// The ticket a restore was approved under shows on its incident's line, charged
// or not, and a line with none has no trailing label.
func TestFormatLedger_ShowsTheTicketOnTheIncident(t *testing.T) {
	restart := []string{"pbs_db_restart_triage", "pbs_pgbackrest_restore_remediate"}
	a := lInc("inc_ticketed", audit.IncidentStatusResolved, restart, 5*time.Hour, 5*time.Hour-20*time.Minute)
	a.EntryRunID = "plr_a"
	b := lInc("inc_plain", audit.IncidentStatusResolved, restart, 3*time.Hour, 3*time.Hour-10*time.Minute)
	b.EntryRunID = "plr_b"
	c := lInc("inc_human", audit.IncidentStatusNeedsHuman, restart, 2*time.Hour, 2*time.Hour-10*time.Minute)
	c.EntryRunID = "plr_c"
	l := ledgerFixture(a, b, c)
	l.AttachTickets(map[string]string{"plr_a": "INC-1239", "plr_c": "CHG-7"})
	out := formatLedger(l)
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "inc_ticketed") && !strings.HasSuffix(line, "20m 00s  ticket INC-1239"):
			t.Errorf("charged line: %q", line)
		case strings.Contains(line, "inc_human") && !strings.HasSuffix(line, "ticket CHG-7"):
			t.Errorf("unconfirmed line: %q", line)
		case strings.Contains(line, "inc_plain") && strings.Contains(line, "ticket"):
			t.Errorf("a line with no ticket has a label: %q", line)
		}
	}
}

func TestFormatLedger_SaysWhetherADrillIsDue(t *testing.T) {
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	restart := []string{"pbs_db_restart_triage", "pbs_pgbackrest_restore_remediate"}
	with := func(c *availability.DrillCurrency) string {
		l := ledgerFixture(lInc("inc_a", audit.IncidentStatusResolved, restart, 5*time.Hour, 5*time.Hour-20*time.Minute))
		l.DrillCurrency = c
		return formatLedger(l)
	}
	age := 143
	out := with(&availability.DrillCurrency{State: availability.DrillOverdue, Overdue: true, AgeDays: &age,
		Detail: "last verified drill 143 days ago (limit 90 days); the latest attempt, drl_bad (2 days ago), was refused on archive_guard and does not count"})
	if !strings.Contains(flat(out), "Drills ⚠ DRILL OVERDUE: last verified drill 143 days ago (limit 90 days); the latest attempt, drl_bad (2 days ago), was refused on archive_guard and does not count") {
		t.Errorf("overdue:\n%s", out)
	}
	// The drill lines wrap, and continue under the text, not under the label.
	inDrills := false
	for i, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "  Drills") {
			inDrills = true
		} else if !strings.HasPrefix(l, "                          ") {
			inDrills = false
		}
		if inDrills && len([]rune(l)) > 90 {
			t.Errorf("line %d is %d wide:\n%s", i, len([]rune(l)), l)
		}
	}
	if !strings.Contains(out, "\n                          ") {
		t.Errorf("a long reminder did not wrap under its text:\n%s", out)
	}
	a := 12
	out = with(&availability.DrillCurrency{State: availability.DrillCurrent, AgeDays: &a, Detail: "last verified drill 12 days ago (limit 90 days)"})
	if !strings.Contains(flat(out), "Drills last verified drill 12 days ago (limit 90 days)") || strings.Contains(out, "OVERDUE") {
		t.Errorf("current:\n%s", out)
	}
	// Off, or none: no line, and the budget section reads as it always did.
	for name, c := range map[string]*availability.DrillCurrency{
		"off":  {State: availability.DrillReminderOff, Detail: "the drill reminder is turned off for this database (drill_interval 0d)"},
		"none": nil,
	} {
		if out := with(c); strings.Contains(out, "Drills") {
			t.Errorf("%s:\n%s", name, out)
		}
	}
}

func TestFormatLedger_Layout(t *testing.T) {
	restart := []string{"pbs_db_restart_triage", "pbs_pgbackrest_restore_remediate"}
	out := formatLedger(ledgerFixture(
		lInc("inc_restore", audit.IncidentStatusResolved, restart, 5*time.Hour, 5*time.Hour-20*time.Minute),
		lInc("inc_human", audit.IncidentStatusNeedsHuman, restart, 3*time.Hour, 3*time.Hour-15*time.Minute),
		lInc("inc_locks", audit.IncidentStatusResolved, []string{"pbs_lock_chain_triage"}, 9*time.Hour, 9*time.Hour-10*time.Minute),
		lInc("inc_vac", audit.IncidentStatusResolved, []string{"pbs_vacuum_triage"}, 10*time.Hour, 10*time.Hour-5*time.Minute),
		lInc("inc_custom", audit.IncidentStatusResolved, []string{"pbs_made_up"}, 11*time.Hour, 11*time.Hour-time.Minute),
	))
	for _, want := range []string{
		"Availability budget: prod-orders",
		"Window   30d [environment:prod], as of 2026-11-10 12:00 UTC",
		"Budget   availability 99.9% over 30d allows 43m12s of downtime per window",
		"Spent    20m 00s in 1 outage incident(s), 46% of the budget",
		"Left     23m 12s",
		"if the unconfirmed outages below count in full",
		"Charged (outages that count against the budget)",
		"inc_restore",
		"Unconfirmed (outages not charged: their real downtime is unknown)",
		"inc_human",
		"ended needs_human, not as a confirmed recovery",
		"Not charged: 1 degraded (10m 00s), 1 no availability impact (5m 00s), 1 no declared impact (1m 00s)",
		"! 1 incident(s) ran only playbooks with no declared availability impact",
		"Duration is from detection to recovery",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	// Charged comes before unconfirmed, and the incident that is not charged is not under Charged.
	charged, unconfirmed := strings.Index(out, "Charged ("), strings.Index(out, "Unconfirmed (")
	if charged < 0 || unconfirmed < charged {
		t.Errorf("sections out of order:\n%s", out)
	}
	if strings.Index(out, "inc_human") < unconfirmed {
		t.Errorf("the unconfirmed incident is listed as charged:\n%s", out)
	}
	if strings.Contains(out, "OVERSPENT") {
		t.Errorf("a budget with room left says it is overspent:\n%s", out)
	}
}

func TestFormatLedger_Overspent(t *testing.T) {
	restart := []string{"pbs_pgbackrest_restore_remediate"}
	out := formatLedger(ledgerFixture(
		lInc("a", audit.IncidentStatusResolved, restart, 6*time.Hour, 6*time.Hour-30*time.Minute),
		lInc("b", audit.IncidentStatusResolved, restart, 3*time.Hour, 3*time.Hour-30*time.Minute),
	))
	for _, want := range []string{"Spent    1h 00m in 2 outage incident(s), 139% of the budget", "Left     -16m 48s", "⚠ OVERSPENT"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
}

func TestFormatLedger_StillOpenIsSaid(t *testing.T) {
	out := formatLedger(ledgerFixture(lInc("inc_now", audit.IncidentStatusOpen, []string{"pbs_db_restart_triage"}, 7*time.Minute, 0)))
	if !strings.Contains(out, "open, still open") {
		t.Errorf("an open incident is not marked:\n%s", out)
	}
	if !strings.Contains(out, "are still open and are counted up to now") {
		t.Errorf("the note is missing:\n%s", out)
	}
}

func TestFormatLedger_NothingToShow(t *testing.T) {
	out := formatLedger(ledgerFixture())
	if !strings.Contains(out, "Spent    0s in 0 outage incident(s), 0% of the budget") || !strings.Contains(out, "Left     43m 12s") {
		t.Errorf("an empty ledger:\n%s", out)
	}
	for _, absent := range []string{"Charged (", "Unconfirmed (", "Not charged:"} {
		if strings.Contains(out, absent) {
			t.Errorf("an empty ledger has %q:\n%s", absent, out)
		}
	}
}

func TestFormatLedger_NoBudget(t *testing.T) {
	l := availability.Compute(availability.Params{
		Database: "scratch", Now: ledgerNow, Classify: playbooks.AvailabilityImpact,
		Budget: infra.AvailabilityBudget{Detail: "no availability_slo is set: no budget"},
	})
	out := formatLedger(l)
	if !strings.Contains(out, "Budget   no availability_slo is set: no budget") || strings.Contains(out, "Left ") || strings.Contains(out, "of the budget") {
		t.Errorf("a ledger with no budget claims one:\n%s", out)
	}
}

func TestFetchAvailabilityLedger(t *testing.T) {
	var gotAuth, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotQuery = r.Header.Get("Authorization"), r.URL.RawQuery
		switch r.URL.Query().Get("database") {
		case "ambiguous":
			http.Error(w, `{"error":"target \"5432\" matches multiple registered servers (a, b) — refusing to guess"}`, http.StatusConflict)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"database":"prod-orders","window_seconds":2592000,"window_source":"default","basis":"b","budget_detail":"d","charged":[],"unconfirmed":[],"degraded":[],"none":[],"unclassified":[]}`)) //nolint:errcheck
		}
	}))
	defer srv.Close()

	l, err := fetchAvailabilityLedger(srv.URL+"/", "k3y", "host=a port=5432 dbname=o")
	if err != nil || l.Database != "prod-orders" || l.WindowSeconds != 2592000 {
		t.Fatalf("%+v, %v", l, err)
	}
	if gotAuth != "Bearer k3y" || gotQuery != "database=host%3Da+port%3D5432+dbname%3Do" {
		t.Errorf("auth %q, query %q", gotAuth, gotQuery)
	}
	if _, err := fetchAvailabilityLedger(srv.URL, "", "ambiguous"); err == nil ||
		!strings.Contains(err.Error(), "gateway returned 409") || !strings.Contains(err.Error(), "refusing to guess") {
		t.Errorf("a refusal should reach the user whole: %v", err)
	}
}
