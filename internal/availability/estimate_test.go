package availability

import (
	"context"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

const restoreSeries = "pbs_pgbackrest_pitr_remediate"

// restoreRun is a run of the restore series that took d, ended `endedAgo` ago.
func restoreRun(outcome string, d, endedAgo time.Duration) audit.RunSpan {
	end := now.Add(-endedAgo)
	sp := audit.RunSpan{RunID: "plr_x", SeriesID: restoreSeries, Outcome: outcome, StartedAt: end.Add(-d)}
	if d > 0 {
		sp.CompletedAt = end
	}
	return sp
}

// listingOf is a listing with one incident per run set.
func listingOf(origin string, runSets ...[]audit.RunSpan) *audit.IncidentListing {
	l := &audit.IncidentListing{}
	for _, runs := range runSets {
		l.Incidents = append(l.Incidents, audit.IncidentWithSeries{Incident: &audit.Incident{IncidentID: "inc", Origin: origin}, Runs: runs})
	}
	return l
}

func resolved(minutes ...int) *audit.IncidentListing {
	var sets [][]audit.RunSpan
	for _, m := range minutes {
		sets = append(sets, []audit.RunSpan{restoreRun(audit.OutcomeResolved, time.Duration(m)*time.Minute, 24*time.Hour)})
	}
	return listingOf("real", sets...)
}

func rto(d time.Duration) *time.Duration { return &d }

func TestEstimateRecovery_PercentilesAreNearestRank(t *testing.T) {
	tests := []struct {
		name               string
		minutes            []int
		p50, p90, min, max int64
	}{
		{"three", []int{30, 10, 20}, 20 * 60, 30 * 60, 10 * 60, 30 * 60}, // unsorted in: ranks 2 and 3 of 3
		{"ten", []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}, 50 * 60, 90 * 60, 10 * 60, 100 * 60},
		{"one value repeated", []int{25, 25, 25, 25}, 25 * 60, 25 * 60, 25 * 60, 25 * 60},
		{"a long tail is the p90 and the max, not the p50", []int{10, 11, 12, 13, 14, 15, 16, 17, 18, 120}, 14 * 60, 18 * 60, 10 * 60, 120 * 60},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := EstimateRecovery(resolved(tc.minutes...), []string{restoreSeries}, now, EstimateWindow, nil)
			if e.Status != EstimateEstimated || e.Samples != len(tc.minutes) {
				t.Fatalf("status %s, samples %d (%s)", e.Status, e.Samples, e.Basis)
			}
			if *e.P50Seconds != tc.p50 || *e.P90Seconds != tc.p90 || *e.MinSeconds != tc.min || *e.MaxSeconds != tc.max {
				t.Errorf("p50 %d p90 %d min %d max %d; want %d %d %d %d", *e.P50Seconds, *e.P90Seconds, *e.MinSeconds, *e.MaxSeconds, tc.p50, tc.p90, tc.min, tc.max)
			}
		})
	}
}

// Below three samples a percentile is just the one or two numbers there are,
// and is not offered as an estimate.
func TestEstimateRecovery_NeedsEnoughHistory(t *testing.T) {
	for n, want := range map[int]string{0: EstimateNone, 1: EstimateTooFew, 2: EstimateTooFew, 3: EstimateEstimated, 4: EstimateEstimated} {
		var mins []int
		for i := 0; i < n; i++ {
			mins = append(mins, 10+i)
		}
		e := EstimateRecovery(resolved(mins...), []string{restoreSeries}, now, EstimateWindow, nil)
		if e.Status != want || e.Samples != n {
			t.Errorf("%d samples = %s (%d)", n, e.Status, e.Samples)
		}
		if want != EstimateEstimated && (e.P50Seconds != nil || e.P90Seconds != nil || e.MaxSeconds != nil) {
			t.Errorf("%d samples: a summary was offered: %+v", n, e)
		}
	}
	none := EstimateRecovery(nil, []string{restoreSeries}, now, EstimateWindow, nil)
	if none.Status != EstimateNone || !strings.Contains(none.Basis, "no resolved restore of this database in the last 90d among real incidents") {
		t.Errorf("nil listing: %+v", none)
	}
	few := EstimateRecovery(resolved(10, 20), []string{restoreSeries}, now, EstimateWindow, nil)
	if !strings.Contains(few.Basis, "only 2 resolved restore(s)") || !strings.Contains(few.Basis, "3 are needed for an estimate") {
		t.Errorf("too few: %q", few.Basis)
	}
}

// Only what was a recovery counts, and what was left out is said.
func TestEstimateRecovery_CountsOnlyRealResolvedFinishedRunsOfTheSeries(t *testing.T) {
	r := func(outcome string, minutes int) []audit.RunSpan {
		return []audit.RunSpan{restoreRun(outcome, time.Duration(minutes)*time.Minute, 24*time.Hour)}
	}
	l := listingOf("real",
		r(audit.OutcomeResolved, 10), r(audit.OutcomeResolved, 20), r(audit.OutcomeResolved, 30),
		r(audit.OutcomeAbandoned, 99), // failed: took a time, was not a recovery
		r(audit.OutcomeNoAction, 98),  // did nothing
		r(audit.OutcomeResolved, 0),   // has not finished (no completion)
		[]audit.RunSpan{{SeriesID: "pbs_vacuum_triage", Outcome: audit.OutcomeResolved, StartedAt: now.Add(-48 * time.Hour), CompletedAt: now.Add(-47 * time.Hour)}}, // another series
	)
	// A faulttest incident and an entry with no incident are not counted either.
	l.Incidents = append(l.Incidents,
		audit.IncidentWithSeries{Incident: &audit.Incident{Origin: audit.IncidentOriginFaulttest}, Runs: r(audit.OutcomeResolved, 77)},
		audit.IncidentWithSeries{Runs: r(audit.OutcomeResolved, 66)})

	e := EstimateRecovery(l, []string{restoreSeries}, now, EstimateWindow, nil)
	if e.Samples != 3 || *e.MaxSeconds != 30*60 {
		t.Fatalf("samples %d max %v: %s", e.Samples, e.MaxSeconds, e.Basis)
	}
	if e.Excluded != 3 {
		t.Errorf("excluded = %d, want the 3 runs of the series that did not recover or had not finished", e.Excluded)
	}
	if !strings.Contains(e.Basis, "3 other run(s) did not recover or had not finished and are not counted") {
		t.Errorf("basis = %q", e.Basis)
	}
	// A restore to the latest backup is the same kind of recovery.
	both := EstimateRecovery(listingOf("real", []audit.RunSpan{{SeriesID: "pbs_pgbackrest_restore_remediate", Outcome: audit.OutcomeResolved,
		StartedAt: now.Add(-3 * time.Hour), CompletedAt: now.Add(-3*time.Hour + 5*time.Minute)}}), RestoreSeries, now, EstimateWindow, nil)
	if both.Samples != 1 {
		t.Errorf("restore-to-latest was not counted with RestoreSeries: %+v", both)
	}
}

func TestEstimateRecovery_OnlyLooksBackAsFarAsTheWindow(t *testing.T) {
	r := func(d, endedAgo time.Duration) []audit.RunSpan {
		return []audit.RunSpan{restoreRun(audit.OutcomeResolved, d, endedAgo)}
	}
	l := listingOf("real",
		r(10*time.Minute, 24*time.Hour), r(20*time.Minute, 24*time.Hour), r(30*time.Minute, 24*time.Hour),
		r(500*time.Minute, 100*24*time.Hour), // finished 100 days ago: outside 90
		r(500*time.Minute, 91*24*time.Hour),
	)
	e := EstimateRecovery(l, []string{restoreSeries}, now, EstimateWindow, nil)
	if e.Samples != 3 || *e.MaxSeconds != 30*60 {
		t.Errorf("samples %d max %v: an old restore was counted", e.Samples, e.MaxSeconds)
	}
	if e.Excluded != 0 {
		t.Errorf("a restore outside the window was reported as excluded: %d", e.Excluded)
	}
	// A narrower window sees fewer.
	if e := EstimateRecovery(l, []string{restoreSeries}, now, 12*time.Hour, nil); e.Samples != 0 {
		t.Errorf("a 12h window counted %d", e.Samples)
	}
}

// The declared rto against what has been demonstrated: a count of the restores
// that took longer, not an inference from a percentile.
func TestEstimateRecovery_SetAgainstTheDeclaredRTO(t *testing.T) {
	l := resolved(10, 20, 30, 40)
	within := EstimateRecovery(l, []string{restoreSeries}, now, EstimateWindow, rto(40*time.Minute))
	if within.VsRTO != "within" || *within.OverRTO != 0 || *within.RTOSeconds != 2400 ||
		!strings.Contains(within.VsRTODetail, "none of the 4 past restores took longer than the rto of 40m (the longest took 40m)") {
		t.Errorf("an rto equal to the longest: %+v", within)
	}
	over := EstimateRecovery(l, []string{restoreSeries}, now, EstimateWindow, rto(25*time.Minute))
	if over.VsRTO != "exceeds" || *over.OverRTO != 2 || !strings.Contains(over.VsRTODetail, "2 of the 4 past restores took longer than the rto of 25m (the longest took 40m)") {
		t.Errorf("an rto two restores broke: %+v", over)
	}
	// No rto declared, or no estimate: no comparison.
	if none := EstimateRecovery(l, []string{restoreSeries}, now, EstimateWindow, nil); none.VsRTO != "" || none.OverRTO != nil || none.RTOSeconds != nil {
		t.Errorf("a comparison with no rto: %+v", none)
	}
	few := EstimateRecovery(resolved(10), []string{restoreSeries}, now, EstimateWindow, rto(time.Minute))
	if few.VsRTO != "" || few.OverRTO != nil {
		t.Errorf("a comparison from one sample: %+v", few)
	}
}

func TestEstimateRecovery_SaysWhenTheListingWasCutOff(t *testing.T) {
	l := resolved(10, 20, 30)
	l.Truncated = true
	e := EstimateRecovery(l, []string{restoreSeries}, now, EstimateWindow, nil)
	if !e.Truncated || !strings.Contains(e.Basis, "cut off at its page limit, so older restores may be missing") {
		t.Errorf("%+v", e)
	}
}

func TestEstimateRecovery_BasisReadsAsASentence(t *testing.T) {
	e := EstimateRecovery(resolved(10, 20, 30, 40, 50, 60, 70, 80, 90, 100), []string{restoreSeries}, now, EstimateWindow, nil)
	want := "10 resolved restores of this database in the last 90d (real incidents): p50 50m, p90 1h30m, longest 1h40m"
	if e.Basis != want {
		t.Errorf("\n got  %q\n want %q", e.Basis, want)
	}
}

func TestPercentile(t *testing.T) {
	s := []time.Duration{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	for p, want := range map[float64]time.Duration{0: 1, 0.1: 1, 0.5: 5, 0.9: 9, 0.95: 10, 1: 10, 2: 10} {
		if got := percentile(s, p); got != want {
			t.Errorf("percentile(%v) = %v, want %v", p, got, want)
		}
	}
	if got := percentile([]time.Duration{7}, 0.9); got != 7 {
		t.Errorf("one value = %v", got)
	}
}

// ── the client ────────────────────────────────────────────────────────────────

func TestClient_RecoveryEstimateAsksForRealRunsOfTheDatabaseOverTheWindow(t *testing.T) {
	end := func(m int) string { return now.Add(-time.Duration(m) * time.Hour).Format(time.RFC3339) }
	start := func(m, took int) string {
		return now.Add(-time.Duration(m)*time.Hour - time.Duration(took)*time.Minute).Format(time.RFC3339)
	}
	run := func(h, took int) string {
		return `{"series_id": "` + restoreSeries + `", "outcome": "resolved", "started_at": "` + start(h, took) + `", "completed_at": "` + end(h) + `"}`
	}
	aud := &recordingAuditd{mine: `{"count": 3, "incidents": [
	  {"incident_id": "a", "origin": "real", "runs": [` + run(30, 10) + `]},
	  {"incident_id": "b", "origin": "real", "runs": [` + run(60, 20) + `]},
	  {"incident_id": "c", "origin": "real", "runs": [` + run(90, 30) + `]}]}`}
	srv := aud.serve(t)
	c := &Client{URL: srv.URL, APIKey: "svc-key"}

	e, err := c.RecoveryEstimate(context.Background(), "prod-orders", []string{restoreSeries}, rto(25*time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != EstimateEstimated || e.Samples != 3 || *e.P50Seconds != 20*60 || e.VsRTO != "exceeds" || *e.OverRTO != 1 {
		t.Errorf("estimate = %+v", e)
	}
	q := aud.queries[0]
	if q.Get("target_key") != "prod-orders" || q.Get("origin") != "real" || q.Get("include") != "runs" || q.Get("unattributed") != "" {
		t.Errorf("query = %v", q)
	}
	since, err := time.Parse(time.RFC3339, q.Get("since"))
	if err != nil || now.Add(-EstimateWindow).Sub(since) > time.Second || since.Sub(now.Add(-EstimateWindow)) > time.Second {
		t.Errorf("since = %q (%v), want %s before now", q.Get("since"), err, EstimateWindow)
	}
	if aud.auth[0] != "Bearer svc-key" {
		t.Errorf("authorization = %q", aud.auth[0])
	}
}

func TestClient_RecoveryEstimateErrors(t *testing.T) {
	if _, err := (&Client{}).RecoveryEstimate(context.Background(), "x", RestoreSeries, nil, now); err == nil {
		t.Error("no audit service: no error, an estimate would read as no history")
	}
	bad := &recordingAuditd{status: 500}
	srv := bad.serve(t)
	if _, err := (&Client{URL: srv.URL}).RecoveryEstimate(context.Background(), "x", RestoreSeries, nil, now); err == nil {
		t.Error("an auditd failure read as an estimate")
	}
}
