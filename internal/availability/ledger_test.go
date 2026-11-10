package availability

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
	"helpdesk/playbooks"
)

var now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// at is a time on the day of the ledger, hours and minutes before now.
func ago(d time.Duration) time.Time { return now.Add(-d) }

// 99.9% over 30 days: 43m12s (2592s), and a 30m rto uses most of it.
func budget() infra.AvailabilityBudget {
	return infra.ComputeBudget(99.9, 30*24*time.Hour, "environment:prod", 30*time.Minute, true)
}

func params(incs ...Incident) Params {
	return Params{Database: "prod-orders", Budget: budget(), Incidents: incs, Now: now, Classify: playbooks.AvailabilityImpact}
}

// outage is a resolved restore that took `d`, ending `endedAgo` before now.
func outage(id string, d, endedAgo time.Duration) Incident {
	end := ago(endedAgo)
	return Incident{ID: id, Status: audit.IncidentStatusResolved, Origin: audit.IncidentOriginReal,
		SeriesIDs:  []string{"pbs_db_restart_triage", "pbs_pgbackrest_restore_remediate"},
		DetectedAt: end.Add(-d), ResolvedAt: end}
}

func secs(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestCompute_ChargesAResolvedOutageForItsDuration(t *testing.T) {
	l := Compute(params(outage("inc_1", 20*time.Minute, time.Hour)))
	if len(l.Charged) != 1 || l.Charged[0].DurationSeconds != 1200 || l.ChargedSeconds != 1200 {
		t.Fatalf("charged = %+v (%ds)", l.Charged, l.ChargedSeconds)
	}
	if l.BudgetSeconds == nil || *l.BudgetSeconds != 2592 {
		t.Fatalf("budget = %v", secs(l.BudgetSeconds))
	}
	if *l.RemainingSeconds != 1392 || *l.RemainingWorstCaseSeconds != 1392 || l.Overspent {
		t.Errorf("remaining = %v / worst %v, overspent %v", secs(l.RemainingSeconds), secs(l.RemainingWorstCaseSeconds), l.Overspent)
	}
	if l.UsedShare == nil || *l.UsedShare < 0.4629 || *l.UsedShare > 0.4630 {
		t.Errorf("used share = %v", l.UsedShare)
	}
	if l.Basis != Basis || !strings.Contains(l.Basis, "from detection to recovery") {
		t.Errorf("basis = %q", l.Basis)
	}
	if l.WindowSeconds != 30*86400 || l.WindowSource != "environment:prod" || l.Database != "prod-orders" {
		t.Errorf("window = %d from %q for %q", l.WindowSeconds, l.WindowSource, l.Database)
	}
}

// What an incident cost is decided by its worst run, not its first.
func TestCompute_AnIncidentIsClassifiedByItsWorstRun(t *testing.T) {
	mk := func(id string, series ...string) Incident {
		return Incident{ID: id, Status: audit.IncidentStatusResolved, SeriesIDs: series,
			DetectedAt: ago(5 * time.Hour), ResolvedAt: ago(4 * time.Hour)}
	}
	l := Compute(params(
		mk("restore", "pbs_pgbackrest_pitr_triage", "pbs_pgbackrest_pitr_remediate"), // none then outage
		mk("triage-only", "pbs_pgbackrest_pitr_triage"),                              // none
		mk("locks", "pbs_lock_chain_triage", "pbs_lock_chain_remediate"),             // degraded
		mk("vacuum", "pbs_vacuum_triage", "pbs_vacuum_remediate"),                    // none
		mk("custom", "pbs_made_up_by_someone"),                                       // unclassified
		mk("no-series"),                                                              // unclassified
		mk("mixed", "pbs_made_up_by_someone", "pbs_db_restart_triage"),               // an unknown run does not hide a known one
	))
	ids := func(es []Entry) string {
		var s []string
		for _, e := range es {
			s = append(s, e.IncidentID)
		}
		return strings.Join(s, ",")
	}
	// Order within a bucket is by detection time, and these are all equal, so check as sets.
	has := func(es []Entry, want ...string) bool {
		if len(es) != len(want) {
			return false
		}
		for _, w := range want {
			if !strings.Contains(ids(es), w) {
				return false
			}
		}
		return true
	}
	if !has(l.Charged, "restore", "mixed") {
		t.Errorf("charged = %s, want restore and mixed", ids(l.Charged))
	}
	if !has(l.None, "triage-only", "vacuum") {
		t.Errorf("none = %s", ids(l.None))
	}
	if !has(l.Degraded, "locks") {
		t.Errorf("degraded = %s", ids(l.Degraded))
	}
	if !has(l.Unclassified, "custom", "no-series") {
		t.Errorf("unclassified = %s", ids(l.Unclassified))
	}
	if len(l.Unclassified) != 2 || !strings.Contains(strings.Join(l.Notes, "\n"), "2 incident(s) ran only playbooks with no declared availability impact") {
		t.Errorf("notes = %v", l.Notes)
	}
}

// An outage that did not end in a confirmed recovery has an unknown length: it
// is listed, not charged, and the worst case counts it.
func TestCompute_AnOutageThatEndedUnconfirmedIsNotCharged(t *testing.T) {
	for _, status := range []string{audit.IncidentStatusEscalated, audit.IncidentStatusNeedsHuman, audit.IncidentStatusAbandoned} {
		t.Run(status, func(t *testing.T) {
			inc := outage("inc_x", 15*time.Minute, time.Hour)
			inc.Status = status
			l := Compute(params(outage("inc_ok", 10*time.Minute, 3*time.Hour), inc))
			if l.ChargedSeconds != 600 || len(l.Charged) != 1 {
				t.Errorf("charged = %+v", l.Charged)
			}
			if len(l.Unconfirmed) != 1 || l.Unconfirmed[0].DurationSeconds != 900 || !strings.Contains(l.Unconfirmed[0].Why, status) {
				t.Fatalf("unconfirmed = %+v", l.Unconfirmed)
			}
			if *l.RemainingSeconds != 2592-600 || *l.RemainingWorstCaseSeconds != 2592-600-900 {
				t.Errorf("remaining %v, worst case %v", secs(l.RemainingSeconds), secs(l.RemainingWorstCaseSeconds))
			}
			if !strings.Contains(strings.Join(l.Notes, "\n"), "remaining_worst_case counts them") {
				t.Errorf("notes = %v", l.Notes)
			}
		})
	}
	// A degraded incident that ended unconfirmed is still just degraded: only outages matter here.
	d := Incident{ID: "d", Status: audit.IncidentStatusNeedsHuman, SeriesIDs: []string{"pbs_lock_chain_triage"}, DetectedAt: ago(2 * time.Hour), ResolvedAt: ago(time.Hour)}
	if l := Compute(params(d)); len(l.Degraded) != 1 || len(l.Unconfirmed) != 0 {
		t.Errorf("degraded unconfirmed: %+v / %+v", l.Degraded, l.Unconfirmed)
	}
}

func TestCompute_OpenIncidents(t *testing.T) {
	open := func(id string, openFor time.Duration) Incident {
		return Incident{ID: id, Status: audit.IncidentStatusOpen, SeriesIDs: []string{"pbs_db_restart_triage"}, DetectedAt: ago(openFor)}
	}
	t.Run("a recent open outage is charged up to now", func(t *testing.T) {
		l := Compute(params(open("inc_open", 7*time.Minute)))
		if len(l.Charged) != 1 || !l.Charged[0].Ongoing || l.Charged[0].DurationSeconds != 420 {
			t.Fatalf("charged = %+v", l.Charged)
		}
		if !strings.Contains(strings.Join(l.Notes, "\n"), "1 outage incident(s) are still open and are counted up to now") {
			t.Errorf("notes = %v", l.Notes)
		}
	})
	t.Run("an outage open for more than a day is probably a dead run: unconfirmed", func(t *testing.T) {
		l := Compute(params(open("inc_stale", 30*time.Hour)))
		if len(l.Charged) != 0 || len(l.Unconfirmed) != 1 || !strings.Contains(l.Unconfirmed[0].Why, "open for more than 1d") {
			t.Fatalf("charged %+v, unconfirmed %+v", l.Charged, l.Unconfirmed)
		}
		if l.ChargedSeconds != 0 || *l.RemainingSeconds != 2592 {
			t.Errorf("a stale open incident drained the budget: remaining %v", secs(l.RemainingSeconds))
		}
		if *l.RemainingWorstCaseSeconds >= 0 {
			t.Errorf("the worst case ignores a 30h-open outage: %v", secs(l.RemainingWorstCaseSeconds))
		}
	})
	t.Run("the staleness limit can be set", func(t *testing.T) {
		p := params(open("inc_x", 2*time.Hour))
		p.StaleOpen = time.Hour
		if l := Compute(p); len(l.Unconfirmed) != 1 {
			t.Errorf("unconfirmed = %+v", l.Unconfirmed)
		}
	})
}

// Only the part of an incident inside the window is spent.
func TestCompute_ClipsToTheWindow(t *testing.T) {
	window := 30 * 24 * time.Hour
	started := ago(window + time.Hour) // began an hour before the window
	straddle := Incident{ID: "straddle", Status: audit.IncidentStatusResolved, SeriesIDs: []string{"pbs_db_restart_triage"},
		DetectedAt: started, ResolvedAt: started.Add(time.Hour + 10*time.Minute)}
	before := Incident{ID: "before", Status: audit.IncidentStatusResolved, SeriesIDs: []string{"pbs_db_restart_triage"},
		DetectedAt: ago(window + 3*time.Hour), ResolvedAt: ago(window + time.Hour)}
	future := Incident{ID: "future", Status: audit.IncidentStatusResolved, SeriesIDs: []string{"pbs_db_restart_triage"},
		DetectedAt: ago(5 * time.Minute), ResolvedAt: now.Add(time.Hour)} // a clock ahead of ours
	l := Compute(params(straddle, before, future))
	got := map[string]int64{}
	for _, e := range l.Charged {
		got[e.IncidentID] = e.DurationSeconds
	}
	if got["straddle"] != 600 {
		t.Errorf("straddle = %ds, want only the 10 minutes inside the window", got["straddle"])
	}
	if _, ok := got["before"]; ok {
		t.Error("an incident that ended before the window began was counted")
	}
	if got["future"] != 300 {
		t.Errorf("future = %ds, want 5 minutes (clipped to now)", got["future"])
	}
	if !l.WindowStart.Equal(now.Add(-window)) || !l.AsOf.Equal(now) {
		t.Errorf("window %v .. %v", l.WindowStart, l.AsOf)
	}
}

func TestCompute_FaulttestIncidentsNeverCount(t *testing.T) {
	ft := outage("ft", time.Hour, time.Hour)
	ft.Origin = audit.IncidentOriginFaulttest
	real := outage("real", 10*time.Minute, 2*time.Hour)
	noOrigin := outage("noorigin", 5*time.Minute, 3*time.Hour)
	noOrigin.Origin = ""
	l := Compute(params(ft, real, noOrigin))
	if l.ChargedSeconds != 900 || len(l.Charged) != 2 || l.ExcludedFaulttest != 1 {
		t.Errorf("charged %ds (%d), excluded %d", l.ChargedSeconds, len(l.Charged), l.ExcludedFaulttest)
	}
	if !strings.Contains(strings.Join(l.Notes, "\n"), "1 faulttest incident(s) are never counted") {
		t.Errorf("notes = %v", l.Notes)
	}
}

func TestCompute_AnOverspentBudget(t *testing.T) {
	l := Compute(params(outage("a", 30*time.Minute, 5*time.Hour), outage("b", 30*time.Minute, time.Hour)))
	if !l.Overspent || *l.RemainingSeconds != 2592-3600 || *l.RemainingSeconds >= 0 {
		t.Errorf("remaining %v, overspent %v", secs(l.RemainingSeconds), l.Overspent)
	}
	if l.UsedShare == nil || *l.UsedShare <= 1 {
		t.Errorf("used share = %v, want over 100%%", l.UsedShare)
	}
}

// With nothing to measure against, the spend is still listed and nothing is
// shown as remaining.
func TestCompute_NoBudget(t *testing.T) {
	p := params(outage("a", 20*time.Minute, time.Hour))
	p.Budget = infra.AvailabilityBudget{Detail: "no availability_slo is set: no budget"}
	l := Compute(p)
	if l.BudgetSeconds != nil || l.RemainingSeconds != nil || l.RemainingWorstCaseSeconds != nil || l.UsedShare != nil || l.Overspent {
		t.Errorf("a budget appeared from nothing: %+v", l)
	}
	if l.ChargedSeconds != 1200 {
		t.Errorf("the spend was not listed: %d", l.ChargedSeconds)
	}
	if l.WindowSeconds != 30*86400 || l.WindowSource != infra.SourceBuiltin {
		t.Errorf("window = %d from %q, want the default, called that", l.WindowSeconds, l.WindowSource)
	}
	if !strings.Contains(strings.Join(l.Notes, "\n"), "no downtime budget could be computed (no availability_slo is set: no budget)") {
		t.Errorf("notes = %v", l.Notes)
	}
}

func TestCompute_SaysWhatItMayBeMissing(t *testing.T) {
	p := params()
	p.Truncated, p.Unattributed = true, 3
	l := Compute(p)
	all := strings.Join(l.Notes, "\n")
	for _, want := range []string{"3 incident(s) in the window could not be tied to any database", "cut off at its page limit"} {
		if !strings.Contains(all, want) {
			t.Errorf("notes lack %q: %v", want, l.Notes)
		}
	}
	if !l.Truncated || l.Unattributed != 3 {
		t.Errorf("truncated %v, unattributed %d", l.Truncated, l.Unattributed)
	}
}

func TestCompute_ListsAreInDetectionOrder(t *testing.T) {
	l := Compute(params(outage("late", time.Minute, time.Hour), outage("early", time.Minute, 5*time.Hour), outage("mid", time.Minute, 3*time.Hour)))
	if len(l.Charged) != 3 || l.Charged[0].IncidentID != "early" || l.Charged[1].IncidentID != "mid" || l.Charged[2].IncidentID != "late" {
		t.Errorf("order = %+v", l.Charged)
	}
}

func TestCompute_AClosedIncidentWithNoEndCountsAsZero(t *testing.T) {
	inc := outage("odd", time.Hour, time.Hour)
	inc.ResolvedAt = time.Time{}
	l := Compute(params(inc))
	if len(l.Charged) != 1 || l.Charged[0].DurationSeconds != 0 {
		t.Errorf("charged = %+v", l.Charged)
	}
	if !strings.Contains(strings.Join(l.Notes, "\n"), "no end time and count as zero") {
		t.Errorf("notes = %v", l.Notes)
	}
}

// An empty ledger is a valid one, with lists, not nulls, for whoever reads it.
func TestCompute_EmptyLedgerShape(t *testing.T) {
	b, err := json.Marshal(Compute(params()))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"charged", "unconfirmed", "degraded", "none", "unclassified"} {
		list, ok := m[k].([]any)
		if !ok || len(list) != 0 {
			t.Errorf("%s = %#v, want an empty list", k, m[k])
		}
	}
	if m["charged_seconds"].(float64) != 0 || m["remaining_seconds"].(float64) != 2592 || m["basis"] == "" {
		t.Errorf("ledger = %s", b)
	}
}

func TestImpactOf(t *testing.T) {
	if got := impactOf([]string{"pbs_vacuum_triage", "pbs_db_restart_triage"}, playbooks.AvailabilityImpact); got != "outage" {
		t.Errorf("worst of none and outage = %q", got)
	}
	if got := impactOf(nil, playbooks.AvailabilityImpact); got != ImpactUnclassified {
		t.Errorf("no series = %q", got)
	}
	if got := impactOf([]string{"pbs_vacuum_triage"}, nil); got != ImpactUnclassified {
		t.Errorf("no classifier = %q", got)
	}
}
