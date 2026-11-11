package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/availability"
	"helpdesk/internal/infra"
)

var exposureAt = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func dur(d time.Duration, source string) infra.ResolvedDuration {
	return infra.ResolvedDuration{Set: true, Value: d, Source: source}
}

// statusArchivedAgo is a status whose newest archived WAL reached the repository
// this long before exposureAt.
func statusArchivedAgo(d time.Duration) GetPgBackRestStatusResult {
	return GetPgBackRestStatusResult{WALContinuity: &WALContinuity{
		ArchiveEndTime: exposureAt.Add(-d).Format(time.RFC3339),
	}}
}

func TestRPOExposure_AgeOfTheNewestArchivedWALAgainstTheRPO(t *testing.T) {
	obj := infra.ResolvedObjectives{RPO: dur(15*time.Minute, "environment:prod")}
	for _, tc := range []struct {
		name   string
		age    time.Duration
		status string
	}{
		{"fresh", 2 * time.Minute, ExposureWithin},
		{"exactly the rpo", 15 * time.Minute, ExposureWithin},
		{"a second over", 15*time.Minute + time.Second, ExposureMayExceed},
		{"long over", 3 * time.Hour, ExposureMayExceed},
		{"a repository clock ahead of ours", -time.Minute, ExposureWithin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := rpoExposure(statusArchivedAgo(tc.age), exposureAt, obj)
			if c.Status != tc.status || *c.ObjectiveSeconds != 900 {
				t.Fatalf("%+v", c)
			}
			if *c.MeasuredSeconds < 0 {
				t.Errorf("a negative exposure: %d", *c.MeasuredSeconds)
			}
		})
	}

	// Over the rpo it is an UPPER bound, and says so: an idle server writes no WAL.
	c := rpoExposure(statusArchivedAgo(3*time.Hour), exposureAt, obj)
	for _, frag := range []string{"3h", "more than the rpo of 15m", "upper bound", "idle server writes no WAL"} {
		if !strings.Contains(c.Detail, frag) {
			t.Errorf("detail %q lacks %q", c.Detail, frag)
		}
	}
	// And it is never called a proven breach.
	if c.Status == ExposureExceeds {
		t.Error("an upper bound was called a proven breach")
	}
}

func TestRPOExposure_WhatCannotBeCompared(t *testing.T) {
	obj := infra.ResolvedObjectives{RPO: dur(15*time.Minute, "database")}
	for name, res := range map[string]GetPgBackRestStatusResult{
		"no WAL read at all":      {},
		"no archive end time":     {WALContinuity: &WALContinuity{}},
		"an unreadable timestamp": {WALContinuity: &WALContinuity{ArchiveEndTime: "yesterday"}},
	} {
		c := rpoExposure(res, exposureAt, obj)
		if c.Status != ExposureNotComparable || c.MeasuredSeconds != nil || *c.ObjectiveSeconds != 900 || !strings.Contains(c.Detail, "rpo of 15m") {
			t.Errorf("%s: %+v", name, c)
		}
	}
	if c := rpoExposure(statusArchivedAgo(time.Hour), exposureAt, infra.ResolvedObjectives{}); c.Status != ExposureNoObjective || c.ObjectiveSeconds != nil {
		t.Errorf("no rpo: %+v", c)
	}
	// An rpo the config got wrong is not "no rpo": it says it could not be used.
	bad := infra.ResolvedObjectives{RPOInvalid: true, Problems: []string{"rpo: bad"}}
	if c := rpoExposure(statusArchivedAgo(time.Hour), exposureAt, bad); c.Status != ExposureNotComparable || !strings.Contains(c.Detail, "could not be used") {
		t.Errorf("invalid rpo: %+v", c)
	}
}

func TestRTOExposure_PastRestoresAgainstTheRTO(t *testing.T) {
	obj := infra.ResolvedObjectives{RTO: dur(30*time.Minute, "database")}

	within := rtoExposure(obj, restoresThatTook(10, 20, 25))
	if within.Status != ExposureWithin || *within.MeasuredSeconds != 25*60 || *within.ObjectiveSeconds != 1800 {
		t.Errorf("within: %+v", within)
	}
	exceeds := rtoExposure(obj, restoresThatTook(10, 20, 45))
	if exceeds.Status != ExposureExceeds || *exceeds.MeasuredSeconds != 45*60 || !strings.Contains(exceeds.Detail, "1 of the 3 past restores took longer than the rto of 30m") {
		t.Errorf("exceeds: %+v", exceeds)
	}
	// Too little history, an unreadable one and none at all say so, with why.
	for name, est := range map[string]*availability.RecoveryEstimate{
		"too few":     restoresThatTook(10, 20),
		"unreadable":  availability.UnavailableEstimate(errors.New("auditd down")),
		"no estimate": nil,
	} {
		c := rtoExposure(obj, est)
		if c.Status != ExposureNotComparable || c.MeasuredSeconds != nil || !strings.Contains(c.Detail, "not enough history of restores") {
			t.Errorf("%s: %+v", name, c)
		}
	}
	if c := rtoExposure(obj, availability.UnavailableEstimate(errors.New("auditd down"))); !strings.Contains(c.Detail, "could not be read (auditd down)") {
		t.Errorf("an unreadable history lost its reason: %q", c.Detail)
	}
	// No rto, and an rto the config got wrong (unset, with problems).
	if c := rtoExposure(infra.ResolvedObjectives{}, nil); c.Status != ExposureNoObjective || strings.Contains(c.Detail, "problems") {
		t.Errorf("no rto: %+v", c)
	}
	if c := rtoExposure(infra.ResolvedObjectives{Problems: []string{"rto: unreadable"}}, nil); c.Status != ExposureNoObjective || !strings.Contains(c.Detail, "config has problems") {
		t.Errorf("an unreadable rto read as none: %+v", c)
	}
}

func TestExposureOf_OverallStatus(t *testing.T) {
	both := infra.ResolvedObjectives{RPO: dur(15*time.Minute, "d"), RTO: dur(30*time.Minute, "d")}

	// Nothing declared, nothing said: the field is absent, not "not_assessed".
	if got := exposureOf(statusArchivedAgo(time.Hour), exposureAt, infra.ResolvedObjectives{}, nil); got != nil {
		t.Errorf("nothing declared: %+v", got)
	}
	// Both within.
	if got := exposureOf(statusArchivedAgo(time.Minute), exposureAt, both, restoresThatTook(10, 12, 14)); got.Status != ExposureWithin {
		t.Errorf("within: %+v", got)
	}
	// Either one is enough to be exposed.
	if got := exposureOf(statusArchivedAgo(time.Hour), exposureAt, both, restoresThatTook(10, 12, 14)); got.Status != ExposureExposed || got.RPO.Status != ExposureMayExceed || got.RTO.Status != ExposureWithin {
		t.Errorf("rpo only: %+v", got)
	}
	if got := exposureOf(statusArchivedAgo(time.Minute), exposureAt, both, restoresThatTook(10, 12, 90)); got.Status != ExposureExposed || got.RTO.Status != ExposureExceeds {
		t.Errorf("rto only: %+v", got)
	}
	// One within and one that cannot be compared is within: what could be checked held.
	if got := exposureOf(statusArchivedAgo(time.Minute), exposureAt, both, nil); got.Status != ExposureWithin || got.RTO.Status != ExposureNotComparable {
		t.Errorf("rto not comparable: %+v", got)
	}
	// Nothing could be checked: not assessed, never "within".
	if got := exposureOf(GetPgBackRestStatusResult{}, exposureAt, both, nil); got.Status != ExposureNotAssessed {
		t.Errorf("nothing checkable: %+v", got)
	}
	// An rpo the config got wrong still gets a block, so it is not read as no objective.
	if got := exposureOf(GetPgBackRestStatusResult{}, exposureAt, infra.ResolvedObjectives{RPOInvalid: true}, nil); got == nil || got.RPO.Status != ExposureNotComparable {
		t.Errorf("invalid rpo: %+v", got)
	}
}

// ── through the real config and the real tool ─────────────────────────────────

func withExposureClock(t *testing.T) {
	t.Helper()
	old := exposureNow
	exposureNow = func() time.Time { return exposureAt }
	t.Cleanup(func() { exposureNow = old })
}

func TestWithObjectiveExposure_ReadsTheServersObjectivesAndHistory(t *testing.T) {
	withExposureClock(t)
	budgetObjectives(t, "15m", "") // rpo 15m from the environment, rto 30m on the server
	calls := withEstimate(t, restoresThatTook(10, 20, 45), nil)

	res := statusArchivedAgo(2 * time.Hour)
	withObjectiveExposure(context.Background(), &res, "pgbackrest_db")
	x := res.ObjectiveExposure
	if x == nil || x.Status != ExposureExposed || x.RPO.Status != ExposureMayExceed || *x.RPO.MeasuredSeconds != 7200 || x.RTO.Status != ExposureExceeds {
		t.Fatalf("exposure = %+v", x)
	}
	// History is read for this server, set against its declared rto, at the clock's time.
	if len(calls.keys) != 1 || calls.keys[0] != "pgbackrest_db" || calls.rtos[0] == nil || *calls.rtos[0] != 30*time.Minute || !calls.nows[0].Equal(exposureAt) {
		t.Errorf("estimate read = %+v", calls)
	}

	// An unreadable history is said, not taken for none.
	withEstimate(t, nil, errors.New("auditd returned 500"))
	res = statusArchivedAgo(time.Minute)
	withObjectiveExposure(context.Background(), &res, "pgbackrest_db")
	if got := res.ObjectiveExposure; got == nil || got.RTO.Status != ExposureNotComparable || !strings.Contains(got.RTO.Detail, "could not be read (auditd returned 500)") {
		t.Errorf("unreadable history: %+v", got)
	}
}

func TestWithObjectiveExposure_SaysNothingWhenThereIsNothingToSay(t *testing.T) {
	withExposureClock(t)
	calls := withEstimate(t, restoresThatTook(10, 20, 30), nil)

	// Not a configured database.
	budgetObjectives(t, "15m", "")
	res := statusArchivedAgo(time.Hour)
	withObjectiveExposure(context.Background(), &res, "no-such-database")
	if res.ObjectiveExposure != nil {
		t.Errorf("an unknown target got %+v", res.ObjectiveExposure)
	}
	// No infrastructure config at all.
	infraConfig = nil
	withObjectiveExposure(context.Background(), &res, "pgbackrest_db")
	if res.ObjectiveExposure != nil {
		t.Errorf("no config got %+v", res.ObjectiveExposure)
	}
	// A configured server that declares neither objective.
	objectiveInfra(t, "", nil)
	withObjectiveExposure(context.Background(), &res, "pgbackrest_db")
	if res.ObjectiveExposure != nil {
		t.Errorf("no objectives got %+v", res.ObjectiveExposure)
	}
	// With no rto declared the history is not even read.
	objectiveInfra(t, "15m", nil)
	withObjectiveExposure(context.Background(), &res, "pgbackrest_db")
	if res.ObjectiveExposure == nil || res.ObjectiveExposure.RTO.Status != ExposureNoObjective || len(calls.keys) != 0 {
		t.Errorf("rpo only: %+v, history reads %d", res.ObjectiveExposure, len(calls.keys))
	}
}

// scriptedPgBackRest answers each pgBackRest command with the captured
// multi-timeline fixtures: info, the archive listing, the history file and a WAL
// segment header. Nothing is stubbed between it and the tool.
type scriptedPgBackRest struct{ t *testing.T }

func (s scriptedPgBackRest) Run(_ context.Context, name string, args []string, _ []string) (string, error) {
	joined := name + " " + strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "'info'"): // the docker wrapper quotes each argument
		return readFixture(s.t, "multi_timeline_info.json"), nil
	case strings.Contains(joined, ".history"):
		return readFixture(s.t, "multi_timeline_0000001E.history"), nil
	case strings.Contains(joined, "repo-get"):
		return readFixture(s.t, "multi_timeline_000000010000000000000002.gz"), nil
	case strings.Contains(joined, "--recurse"):
		return readFixture(s.t, "multi_timeline_listing.json"), nil
	case strings.Contains(joined, "repo-ls"):
		return readFixture(s.t, "multi_timeline_top.txt"), nil
	}
	return "", errors.New("unexpected command " + joined)
}

// The RPO check reads the archive end time that the real WAL-continuity code
// derives from a real repository listing, not one a test wrote in: the age of the
// newest archived segment, against the rpo, through the real tool.
func TestGetPgBackRestStatus_RPOExposureFromTheRealWALContinuity(t *testing.T) {
	budgetObjectives(t, "15m", "")
	withEstimate(t, nil, availability.ErrNoAudit)
	old := cmdRunner
	cmdRunner = scriptedPgBackRest{t}
	defer func() { cmdRunner = old }()

	// First learn when the newest segment reached the repository, from the real chain.
	withExposureClock(t)
	res, err := getPgBackRestStatusImpl(context.Background(), GetPgBackRestStatusArgs{Target: "pgbackrest_db", Stanza: "main"})
	if err != nil || res.WALContinuity == nil || res.WALContinuity.ArchiveEndTime == "" {
		t.Fatalf("the real chain gave no archive end time: %+v, %v", res.WALContinuity, err)
	}
	end, err := time.Parse(time.RFC3339, res.WALContinuity.ArchiveEndTime)
	if err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		age    time.Duration
		status string
	}{
		"fresh":        {5 * time.Minute, ExposureWithin},
		"the boundary": {15 * time.Minute, ExposureWithin},
		"stale":        {3 * time.Hour, ExposureMayExceed},
	} {
		exposureNow = func() time.Time { return end.Add(tc.age) }
		res, err := getPgBackRestStatusImpl(context.Background(), GetPgBackRestStatusArgs{Target: "pgbackrest_db", Stanza: "main"})
		if err != nil || res.ObjectiveExposure == nil || res.ObjectiveExposure.RPO == nil {
			t.Fatalf("%s: %+v, %v", name, res.ObjectiveExposure, err)
		}
		got := res.ObjectiveExposure.RPO
		if got.Status != tc.status || got.MeasuredSeconds == nil || *got.MeasuredSeconds != int64(tc.age/time.Second) {
			t.Errorf("%s: %+v, want %s at %s", name, got, tc.status, tc.age)
		}
	}
}

// The audit trail cuts a tool result at 8 KB, and a long list of backups is what
// reaches it. The exposure must come before that list, or it is the first thing
// a long history cuts off, and what the model quotes from it can no longer be
// found in the trail.
func TestObjectiveExposure_SurvivesTheAuditCutBehindALongBackupList(t *testing.T) {
	res := statusArchivedAgo(3 * time.Hour)
	res.ObjectiveExposure = exposureOf(res, exposureAt, infra.ResolvedObjectives{RPO: dur(15*time.Minute, "d"), RTO: dur(30*time.Minute, "d")}, restoresThatTook(10, 20, 45))
	for i := 0; i < 60; i++ {
		res.Backups = append(res.Backups, PgBackRestBackup{Label: "20260926-040922F", Type: "full", StartTime: exposureAt, StopTime: exposureAt, WALStart: "000000010000000000000003", WALStop: "000000010000000000000003", DatabaseBytes: 31048775})
	}
	raw, _ := json.Marshal(res)
	x, _ := json.Marshal(res.ObjectiveExposure)
	at := strings.Index(string(raw), string(x))
	if at < 0 {
		t.Fatalf("objective_exposure not found in the result")
	}
	if at+len(x) > 8192 {
		t.Errorf("objective_exposure ends at byte %d of a %d byte result: past the 8192 cut of the audit trail", at+len(x), len(raw))
	}
}

// The whole path: get_pgbackrest_status itself carries the block, backup_stale
// keeps its own meaning beside it, and the names are the ones a model, a
// playbook and the audit trail read.
func TestGetPgBackRestStatus_CarriesObjectiveExposureBesideBackupStale(t *testing.T) {
	withExposureClock(t)
	budgetObjectives(t, "15m", "")
	withEstimate(t, restoresThatTook(10, 20, 25), nil)
	defer withMockRunner(realPgBackRestInfoJSON, nil)()

	res, err := getPgBackRestStatusImpl(context.Background(), GetPgBackRestStatusArgs{Target: "pgbackrest_db", Stanza: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ObjectiveExposure == nil || res.ObjectiveExposure.RTO == nil || res.ObjectiveExposure.RTO.Status != ExposureWithin {
		t.Fatalf("exposure = %+v", res.ObjectiveExposure)
	}
	// The captured backup is from 2026-09-26: by the age check it is stale at the
	// default, and that answer is untouched by the new block.
	if !res.BackupStale {
		t.Error("backup_stale changed: the age check must keep its meaning")
	}
	raw, _ := json.Marshal(res)
	for _, name := range []string{`"objective_exposure":{"status":`, `"rpo":{"status":`, `"rto":{"status":"within"`, `"backup_stale":true`} {
		if !strings.Contains(string(raw), name) {
			t.Errorf("result lacks %s in %s", name, raw)
		}
	}
	// Compact: it rides in an audited result with a size cut.
	x, _ := json.Marshal(res.ObjectiveExposure)
	if len(x) > 900 {
		t.Errorf("objective_exposure is %d bytes; the audited tool result is cut at 8 KB", len(x))
	}

	// The callers that only need the stanza name do not pay for it.
	short, err := readPgBackRestStatus(context.Background(), GetPgBackRestStatusArgs{Target: "pgbackrest_db", Stanza: "main"}, false)
	if err != nil || short.ObjectiveExposure != nil {
		t.Errorf("withWAL=false carried exposure: %+v, %v", short.ObjectiveExposure, err)
	}
}
