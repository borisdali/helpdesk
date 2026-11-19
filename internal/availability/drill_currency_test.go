package availability

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
)

func quarter() infra.DrillReminder {
	return infra.DrillReminder{Applies: true, Interval: infra.DefaultDrillInterval, Source: infra.SourceBuiltin}
}

func agoDays(id string, days int) audit.DrillRecord { return drill(id, 100, days, 10*gib) }

func TestAssessDrillCurrency_CurrentOverdueAndNever(t *testing.T) {
	cur := AssessDrillCurrency([]audit.DrillRecord{agoDays("drl_a", 10)}, quarter(), DrillInputs{}, now)
	if cur.State != DrillCurrent || cur.Overdue || *cur.AgeDays != 10 || cur.LastDrillID != "drl_a" || cur.LimitDays != 90 ||
		cur.Detail != "last verified drill 10 days ago (limit 90 days); matched by name, the source's system identifier was not checked" {
		t.Errorf("current: %+v", cur)
	}
	over := AssessDrillCurrency([]audit.DrillRecord{agoDays("drl_a", 143)}, quarter(), DrillInputs{}, now)
	if over.State != DrillOverdue || !over.Overdue || *over.AgeDays != 143 || !strings.HasPrefix(over.Detail, "last verified drill 143 days ago (limit 90 days)") {
		t.Errorf("overdue: %+v", over)
	}
	never := AssessDrillCurrency(nil, quarter(), DrillInputs{}, now)
	if never.State != DrillNever || !never.Overdue || never.LastVerifiedAt != nil || never.AgeDays != nil ||
		never.Detail != "no verified restore drill of this server is recorded (limit 90 days)" {
		t.Errorf("never: %+v", never)
	}
}

func TestAssessDrillCurrency_TheEdgeAndTheWording(t *testing.T) {
	at := func(age time.Duration) audit.DrillRecord {
		d := agoDays("drl_a", 0)
		d.FinishedAt, d.RecordedAt = now.Add(-age), now.Add(-age)
		return d
	}
	for name, c := range map[string]struct {
		age   time.Duration
		state string
	}{
		"exactly the limit":  {infra.DefaultDrillInterval, DrillCurrent},
		"a second past":      {infra.DefaultDrillInterval + time.Second, DrillOverdue},
		"just now":           {0, DrillCurrent},
		"a clock ahead":      {-time.Hour, DrillCurrent},
		"a day under":        {infra.DefaultDrillInterval - 24*time.Hour, DrillCurrent},
		"a day and a second": {infra.DefaultDrillInterval + 24*time.Hour + time.Second, DrillOverdue},
	} {
		if got := AssessDrillCurrency([]audit.DrillRecord{at(c.age)}, quarter(), DrillInputs{}, now); got.State != c.state {
			t.Errorf("%s: %s, want %s", name, got.State, c.state)
		}
	}
	// One day is "1 day".
	one := infra.DrillReminder{Applies: true, Interval: 24 * time.Hour, Source: infra.SourceDatabase}
	got := AssessDrillCurrency([]audit.DrillRecord{at(24 * time.Hour)}, one, DrillInputs{}, now)
	if !strings.HasPrefix(got.Detail, "last verified drill 1 day ago (limit 1 day)") {
		t.Errorf("singular: %q", got.Detail)
	}
	if got.Source != infra.SourceDatabase {
		t.Errorf("source = %q", got.Source)
	}
}

// Practising the procedure is what the reminder asks, so any verified drill counts,
// not only one that is comparable enough to plan a restore from.
func TestAssessDrillCurrency_AnyVerifiedDrillCounts(t *testing.T) {
	snap := agoDays("drl_s", 5)
	snap.Method, snap.RestoreBytes = audit.DrillMethodSnapshot, 0
	warn := agoDays("drl_w", 6)
	warn.Status = audit.DrillVerifiedWithWarnings
	huge := agoDays("drl_h", 7)
	huge.RestoreBytes = 900 * gib
	for name, d := range map[string]audit.DrillRecord{"a snapshot": snap, "with warnings": warn, "of another size": huge} {
		if got := AssessDrillCurrency([]audit.DrillRecord{d}, quarter(), DrillInputs{}, now); got.State != DrillCurrent {
			t.Errorf("%s: %+v", name, got)
		}
	}
	// And the newest of several decides, not the first listed.
	got := AssessDrillCurrency([]audit.DrillRecord{agoDays("drl_old", 200), agoDays("drl_new", 3), agoDays("drl_mid", 100)}, quarter(), DrillInputs{}, now)
	if got.LastDrillID != "drl_new" || *got.AgeDays != 3 {
		t.Errorf("the newest must decide: %+v", got)
	}
}

func TestAssessDrillCurrency_AFailedAttemptIsNamed(t *testing.T) {
	refused := agoDays("drl_bad", 2)
	refused.Status = audit.DrillRefused
	refused.Checks = []audit.DrillCheck{{Name: "same_system", Result: "pass"}, {Name: "archive_guard", Result: "fail"}, {Name: "consistent", Result: "fail"}}
	unverifiable := agoDays("drl_gone", 1)
	unverifiable.Status = audit.DrillUnverifiable

	// A refusal after the last good drill is named, with what failed.
	got := AssessDrillCurrency([]audit.DrillRecord{agoDays("drl_ok", 100), refused}, quarter(), DrillInputs{}, now)
	if got.State != DrillOverdue || got.LastAttempt == nil || got.LastAttempt.DrillID != "drl_bad" || len(got.LastAttempt.Failed) != 2 {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.Detail, "the latest attempt, drl_bad (2 days ago), was refused on archive_guard, consistent and does not count") {
		t.Errorf("detail: %q", got.Detail)
	}
	// With no good drill at all, it is still named.
	got = AssessDrillCurrency([]audit.DrillRecord{refused}, quarter(), DrillInputs{}, now)
	if got.State != DrillNever || got.LastAttempt == nil || !strings.Contains(got.Detail, "no verified restore drill of this server is recorded") || !strings.Contains(got.Detail, "drl_bad") {
		t.Errorf("never with an attempt: %+v", got)
	}
	// Unverifiable has no failed check to name, and says what it was.
	got = AssessDrillCurrency([]audit.DrillRecord{unverifiable}, quarter(), DrillInputs{}, now)
	if !strings.Contains(got.Detail, "was unverifiable and does not count") || len(got.LastAttempt.Failed) != 0 {
		t.Errorf("unverifiable: %q", got.Detail)
	}
	// An attempt that was put right since (a good drill after it) is history.
	got = AssessDrillCurrency([]audit.DrillRecord{agoDays("drl_ok", 1), refused}, quarter(), DrillInputs{}, now)
	if got.LastAttempt != nil || got.State != DrillCurrent || strings.Contains(got.Detail, "attempt") {
		t.Errorf("a put-right attempt is still named: %+v", got)
	}
	// A refusal that a re-verification superseded is not an attempt at all.
	redo := agoDays("drl_redo", 1)
	redo.Supersedes = "drl_bad"
	got = AssessDrillCurrency([]audit.DrillRecord{refused, redo}, quarter(), DrillInputs{}, now)
	if got.LastAttempt != nil {
		t.Errorf("a superseded refusal is named: %+v", got.LastAttempt)
	}
}

func TestAssessDrillCurrency_MatchingToTheServer(t *testing.T) {
	old := agoDays("drl_oldsys", 100)
	old.SourceSystemID = "1111"
	recent := agoDays("drl_newsys", 5) // system 7001
	// By name: the newest drill's system is this one, so the old system's drill does not count.
	got := AssessDrillCurrency([]audit.DrillRecord{old, recent}, quarter(), DrillInputs{}, now)
	if got.LastDrillID != "drl_newsys" || got.MatchedBy != "name" {
		t.Errorf("by name: %+v", got)
	}
	// Identifier known and it is the old system's: the recent drill is not this server's.
	got = AssessDrillCurrency([]audit.DrillRecord{old, recent}, quarter(), DrillInputs{SystemID: "1111"}, now)
	if got.LastDrillID != "drl_oldsys" || got.State != DrillOverdue || got.MatchedBy != "system_identifier" {
		t.Errorf("by identifier: %+v", got)
	}
	// Known, and no drill is of it: never drilled, whatever the name says.
	got = AssessDrillCurrency([]audit.DrillRecord{old, recent}, quarter(), DrillInputs{SystemID: "rebuilt"}, now)
	if got.State != DrillNever || strings.Contains(got.Detail, "matched by name") {
		t.Errorf("a rebuilt server: %+v", got)
	}
}

func TestAssessDrillCurrency_ReadsTheSystemIDOnlyWhenThereIsAVerifiedDrill(t *testing.T) {
	reads := 0
	in := DrillInputs{ReadSystemID: func() string { reads++; return "7001" }}
	refused := agoDays("drl_r", 3)
	refused.Status = audit.DrillRefused
	AssessDrillCurrency(nil, quarter(), in, now)
	AssessDrillCurrency([]audit.DrillRecord{refused}, quarter(), in, now)
	if reads != 0 {
		t.Errorf("read %d times with nothing verified to match", reads)
	}
	AssessDrillCurrency([]audit.DrillRecord{agoDays("drl_a", 3), agoDays("drl_b", 4)}, quarter(), in, now)
	if reads != 1 {
		t.Errorf("read %d times, want once", reads)
	}
}

func TestAssessDrillCurrency_OffAndNotApplicable(t *testing.T) {
	if got := AssessDrillCurrency(nil, infra.DrillReminder{}, DrillInputs{}, now); got != nil {
		t.Errorf("a database with no objective got %+v", got)
	}
	got := AssessDrillCurrency(nil, infra.DrillReminder{Off: true, Source: infra.SourceDatabase}, DrillInputs{}, now)
	if got == nil || got.State != DrillReminderOff || got.Overdue || !strings.Contains(got.Detail, "turned off") {
		t.Errorf("off: %+v", got)
	}
}

func TestClient_DrillCurrencyReadsTwoYearsBackAndSurvivesFailure(t *testing.T) {
	aud := &recordingAuditd{drills: drillsJSON(t, []audit.DrillRecord{agoDays("drl_a", 143)})}
	srv := aud.serve(t)
	c := &Client{URL: srv.URL, APIKey: "k"}
	got := c.DrillCurrency(context.Background(), "prod-orders", quarter(), DrillInputs{}, now)
	if got.State != DrillOverdue || *got.AgeDays != 143 {
		t.Errorf("%+v", got)
	}
	q := aud.queries[0]
	since, err := time.Parse(time.RFC3339, q.Get("since"))
	if err != nil || q.Get("source") != "prod-orders" || q.Get("current") != "true" ||
		now.Add(-DrillCurrencyLookback).Sub(since) > time.Second || since.Sub(now.Add(-DrillCurrencyLookback)) > time.Second {
		t.Errorf("query %v (since %v, %v): a 143-day-old drill needs more than the estimate's 90-day window", q, since, err)
	}

	// An unreadable list is not "never drilled".
	bad := &recordingAuditd{drillsStatus: 500}
	badSrv := bad.serve(t)
	got = (&Client{URL: badSrv.URL}).DrillCurrency(context.Background(), "x", quarter(), DrillInputs{}, now)
	if got.State != DrillUnreadable || got.Overdue || !strings.Contains(got.Detail, "could not be read") {
		t.Errorf("unreadable: %+v", got)
	}
	// No audit service, the same.
	got = (&Client{}).DrillCurrency(context.Background(), "x", quarter(), DrillInputs{}, now)
	if got.State != DrillUnreadable || !strings.Contains(got.Detail, errors.New("no audit service is configured").Error()) {
		t.Errorf("no audit: %+v", got)
	}
	// Off or not applicable: auditd is not even asked.
	none := &recordingAuditd{}
	noneSrv := none.serve(t)
	if got := (&Client{URL: noneSrv.URL}).DrillCurrency(context.Background(), "x", infra.DrillReminder{}, DrillInputs{}, now); got != nil || len(none.queries) != 0 {
		t.Errorf("not applicable: %+v, %d queries", got, len(none.queries))
	}
}
