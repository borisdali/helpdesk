package availability

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

const gib = int64(1) << 30

// ptr prints an optional number of seconds for a failure message.
func ptr(p *int64) string {
	if p == nil {
		return "none"
	}
	return fmt.Sprintf("%ds", *p)
}

func mustListing(t *testing.T, raw string) *audit.IncidentListing {
	t.Helper()
	var l audit.IncidentListing
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		t.Fatal(err)
	}
	return &l
}

func drillsJSON(t *testing.T, drills []audit.DrillRecord) string {
	t.Helper()
	b, err := json.Marshal(drills)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// drill is a verified pgBackRest drill of "prod-orders" that took the given
// seconds, finished daysAgo days before now, on a clone of the given size.
func drill(id string, seconds int64, daysAgo int, bytes int64) audit.DrillRecord {
	fin := now.Add(-time.Duration(daysAgo) * 24 * time.Hour)
	return audit.DrillRecord{
		DrillID: id, SourceKey: "prod-orders", CloneKey: "orders-clone", SourceSystemID: "7001", CloneSystemID: "7001",
		Method: audit.DrillMethodPgBackRest, Status: audit.DrillVerified,
		StartedAt: fin.Add(-time.Duration(seconds) * time.Second), FinishedAt: fin, RecordedAt: fin,
		DurationSeconds: seconds, DurationSource: audit.DrillDurationReported, RestoreBytes: bytes,
	}
}

func drillsTook(secs ...int64) []audit.DrillRecord {
	var out []audit.DrillRecord
	for i, s := range secs {
		out = append(out, drill("drl_"+string(rune('a'+i)), s, 5+i, 10*gib))
	}
	return out
}

func noRealRestores() RecoveryEstimate {
	return EstimateRecovery(nil, RestoreSeries, now, EstimateWindow, nil)
}

func TestAddDrills_PlanFromDrillsOnlyWhenRealRestoresAreTooFew(t *testing.T) {
	in := DrillInputs{RestoreBytes: 10 * gib}

	// Three drills and no real restore: the longest drill plans the restore.
	e := noRealRestores()
	AddDrills(&e, drillsTook(60, 90, 120), in, now, nil)
	if e.Status != EstimateNone {
		t.Errorf("the real restores' status must stay what it was: %q", e.Status)
	}
	if e.Drills == nil || e.Drills.Status != EstimateEstimated || e.Drills.Samples != 3 || *e.Drills.P50Seconds != 90 {
		t.Fatalf("drills: %+v", e.Drills)
	}
	if e.PlanningSource != PlanningFromDrills || e.PlanningBasis != PlanningLongest || *e.PlanningSeconds != 120 {
		t.Errorf("planning %v from %q (%q)", ptr(e.PlanningSeconds), e.PlanningSource, e.PlanningBasis)
	}
	if e.Samples != 0 {
		t.Errorf("drills leaked into the real restore count: %d", e.Samples)
	}

	// Ten drills: a p90.
	e = noRealRestores()
	AddDrills(&e, drillsTook(10, 20, 30, 40, 50, 60, 70, 80, 90, 600), in, now, nil)
	if e.PlanningBasis != PlanningP90 || *e.PlanningSeconds != 90 || *e.Drills.P90Seconds != 90 {
		t.Errorf("ten drills: planning %v (%q)", ptr(e.PlanningSeconds), e.PlanningBasis)
	}

	// Two drills are too few, like two real restores.
	e = noRealRestores()
	AddDrills(&e, drillsTook(60, 90), in, now, nil)
	if e.Drills.Status != EstimateTooFew || e.PlanningSeconds != nil || e.PlanningSource != "" {
		t.Errorf("two drills: %+v planning %v from %q", e.Drills, ptr(e.PlanningSeconds), e.PlanningSource)
	}
	if !strings.Contains(e.Drills.Basis, "only 2 restore drill(s)") || !strings.Contains(e.Drills.Basis, "3 are needed") {
		t.Errorf("basis %q", e.Drills.Basis)
	}

	// None: said, not left blank.
	e = noRealRestores()
	AddDrills(&e, nil, in, now, nil)
	if e.Drills.Status != EstimateNone || !strings.Contains(e.Drills.Basis, "no restore drill of this server recorded") {
		t.Errorf("none: %+v", e.Drills)
	}
}

func TestAddDrills_RealRestoresWin(t *testing.T) {
	end := func(h int) string { return now.Add(-time.Duration(h) * time.Hour).Format(time.RFC3339) }
	start := func(h, took int) string {
		return now.Add(-time.Duration(h)*time.Hour - time.Duration(took)*time.Minute).Format(time.RFC3339)
	}
	run := func(h, took int) string {
		return `{"series_id": "` + restoreSeries + `", "outcome": "resolved", "started_at": "` + start(h, took) + `", "completed_at": "` + end(h) + `"}`
	}
	l := mustListing(t, `{"count":3,"incidents":[{"incident_id":"a","origin":"real","runs":[`+run(30, 10)+`]},{"incident_id":"b","origin":"real","runs":[`+run(60, 20)+`]},{"incident_id":"c","origin":"real","runs":[`+run(90, 30)+`]}]}`)
	e := EstimateRecovery(l, []string{restoreSeries}, now, EstimateWindow, nil)
	before := *e.PlanningSeconds
	AddDrills(&e, drillsTook(5, 6, 7), DrillInputs{RestoreBytes: 10 * gib}, now, nil)
	if e.PlanningSource != PlanningFromRestores || *e.PlanningSeconds != before || e.Samples != 3 {
		t.Errorf("three real restores must keep the plan: %v from %q, %d samples", ptr(e.PlanningSeconds), e.PlanningSource, e.Samples)
	}
	if e.Drills.Status != EstimateEstimated || e.Drills.Samples != 3 {
		t.Errorf("the drills are still shown beside them: %+v", e.Drills)
	}
}

func TestAddDrills_WhatDoesNotCount(t *testing.T) {
	in := DrillInputs{RestoreBytes: 10 * gib}
	mk := func(edit func(*audit.DrillRecord)) audit.DrillRecord {
		d := drill("drl_x", 100, 5, 10*gib)
		edit(&d)
		return d
	}
	cases := []struct {
		name         string
		d            audit.DrillRecord
		in           DrillInputs
		recorded     int
		counted      int
		reasonKey    string
		reasonSubstr string
	}{
		{"a clean drill", mk(func(*audit.DrillRecord) {}), in, 1, 1, "", ""},
		{"refused", mk(func(d *audit.DrillRecord) { d.Status = audit.DrillRefused }), in, 0, 0, notCountedUnverified, "refused or unverifiable"},
		{"unverifiable", mk(func(d *audit.DrillRecord) { d.Status = audit.DrillUnverifiable }), in, 0, 0, notCountedUnverified, ""},
		{"verified with warnings counts", mk(func(d *audit.DrillRecord) { d.Status = audit.DrillVerifiedWithWarnings }), in, 1, 1, "", ""},
		{"a snapshot clone", mk(func(d *audit.DrillRecord) { d.Method = audit.DrillMethodSnapshot }), in, 1, 0, notCountedMethod, "not a pgBackRest restore"},
		{"a cloud clone", mk(func(d *audit.DrillRecord) { d.Method = audit.DrillMethodCloud }), in, 1, 0, notCountedMethod, ""},
		{"no duration", mk(func(d *audit.DrillRecord) { d.DurationSeconds = 0 }), in, 1, 0, notCountedNoDuration, "no duration recorded"},
		{"older than the window", mk(func(d *audit.DrillRecord) { d.FinishedAt = now.Add(-91 * 24 * time.Hour) }), in, 0, 0, notCountedOld, "older than the window"},
		{"just inside the window", mk(func(d *audit.DrillRecord) { d.FinishedAt = now.Add(-89 * 24 * time.Hour) }), in, 1, 1, "", ""},
		{"size 19% over counts", mk(func(d *audit.DrillRecord) { d.RestoreBytes = 10*gib + gib*19/10 }), in, 1, 1, "", ""},
		{"size 25% over", mk(func(d *audit.DrillRecord) { d.RestoreBytes = 10*gib + gib*25/10 }), in, 1, 0, notCountedSize, "outside the size"},
		{"size 25% under", mk(func(d *audit.DrillRecord) { d.RestoreBytes = 10*gib - gib*25/10 }), in, 1, 0, notCountedSize, ""},
		{"drill size unknown", mk(func(d *audit.DrillRecord) { d.RestoreBytes = 0 }), in, 1, 0, notCountedSizeUnknown, "could not be compared"},
		{"restore size unknown", mk(func(*audit.DrillRecord) {}), DrillInputs{}, 1, 0, notCountedSizeUnknown, ""},
		{"another system", mk(func(d *audit.DrillRecord) { d.SourceSystemID = "9999" }), DrillInputs{RestoreBytes: 10 * gib, SystemID: "7001"}, 0, 0, notCountedOtherSystem, "earlier system"},
	}
	for _, c := range cases {
		e := noRealRestores()
		AddDrills(&e, []audit.DrillRecord{c.d}, c.in, now, nil)
		d := e.Drills
		if d.Recorded != c.recorded || d.Samples != c.counted {
			t.Errorf("%s: recorded %d counted %d, want %d and %d (%v)", c.name, d.Recorded, d.Samples, c.recorded, c.counted, d.NotCounted)
		}
		if c.reasonKey != "" {
			if d.NotCounted[c.reasonKey] != 1 {
				t.Errorf("%s: not counted %v, want 1 under %q", c.name, d.NotCounted, c.reasonKey)
			}
			if c.reasonSubstr != "" && !strings.Contains(d.Basis, c.reasonSubstr) {
				t.Errorf("%s: basis %q lacks %q", c.name, d.Basis, c.reasonSubstr)
			}
		} else if len(d.NotCounted) != 0 {
			t.Errorf("%s: unexpected reasons %v", c.name, d.NotCounted)
		}
	}
}

func TestAddDrills_ASupersededDrillIsNotCountedTwice(t *testing.T) {
	old := drill("drl_old", 500, 20, 10*gib)
	old.Status = audit.DrillRefused
	redo := drill("drl_redo", 100, 19, 10*gib)
	redo.Supersedes = "drl_old"
	e := noRealRestores()
	AddDrills(&e, []audit.DrillRecord{old, redo}, DrillInputs{RestoreBytes: 10 * gib}, now, nil)
	if e.Drills.Samples != 1 || e.Drills.NotCounted != nil {
		t.Errorf("the refused drill the redo replaces must disappear: %+v", e.Drills)
	}
}

func TestAddDrills_MatchingToTheServer(t *testing.T) {
	oldSystem := drill("drl_old", 900, 40, 10*gib)
	oldSystem.SourceSystemID = "1111"
	newer := drillsTook(60, 70, 80) // system 7001, the newer ones
	all := append([]audit.DrillRecord{oldSystem}, newer...)

	// Identifier known: exact.
	e := noRealRestores()
	AddDrills(&e, all, DrillInputs{RestoreBytes: 10 * gib, SystemID: "7001"}, now, nil)
	if e.Drills.MatchedBy != "system_identifier" || e.Drills.Samples != 3 || e.Drills.NotCounted[notCountedOtherSystem] != 1 {
		t.Errorf("by identifier: %+v", e.Drills)
	}
	// A server since rebuilt: the old identifier's drills do not count, and the
	// identifier the caller read is what decides.
	e = noRealRestores()
	AddDrills(&e, all, DrillInputs{RestoreBytes: 10 * gib, SystemID: "1111"}, now, nil)
	if e.Drills.Samples != 1 || e.Drills.NotCounted[notCountedOtherSystem] != 3 {
		t.Errorf("identifier of the old system: %+v", e.Drills)
	}

	// Not known: matched by name, taking the newest drill's system as this one, and
	// the estimate says it did not check.
	e = noRealRestores()
	AddDrills(&e, all, DrillInputs{RestoreBytes: 10 * gib}, now, nil)
	if e.Drills.MatchedBy != "name" || e.Drills.Samples != 3 || e.Drills.NotCounted[notCountedOtherSystem] != 1 {
		t.Errorf("by name: %+v", e.Drills)
	}
	if !strings.Contains(e.Drills.Basis, "matched by name, the source's system identifier was not checked") {
		t.Errorf("basis does not say it was not checked: %q", e.Drills.Basis)
	}
}

func TestAddDrills_AgainstTheDeclaredRTO(t *testing.T) {
	e := noRealRestores()
	AddDrills(&e, drillsTook(600, 1200, 2400), DrillInputs{RestoreBytes: 10 * gib}, now, rto(30*time.Minute))
	d := e.Drills
	if d.VsRTO != "exceeds" || *d.OverRTO != 1 || !strings.Contains(d.VsRTODetail, "1 of the 3 drills took longer than the rto of 30m") {
		t.Errorf("vs rto: %+v", d)
	}
	e = noRealRestores()
	AddDrills(&e, drillsTook(600, 700, 800), DrillInputs{RestoreBytes: 10 * gib}, now, rto(30*time.Minute))
	if e.Drills.VsRTO != "within" || *e.Drills.OverRTO != 0 {
		t.Errorf("within: %+v", e.Drills)
	}
}

func TestAddDrills_AllLeftOutIsExplained(t *testing.T) {
	a := drill("drl_a", 100, 3, 10*gib)
	a.Method = audit.DrillMethodSnapshot
	b := drill("drl_b", 100, 4, 100*gib)
	c := drill("drl_c", 100, 5, 10*gib)
	c.Status = audit.DrillRefused
	e := noRealRestores()
	AddDrills(&e, []audit.DrillRecord{a, b, c}, DrillInputs{RestoreBytes: 10 * gib}, now, nil)
	if e.Drills.Status != EstimateNone || e.Drills.Samples != 0 || e.Drills.Recorded != 2 {
		t.Fatalf("%+v", e.Drills)
	}
	for _, want := range []string{"no restore drill of this server that counts", "1 refused or unverifiable", "1 not a pgBackRest restore", "1 outside the size"} {
		if !strings.Contains(e.Drills.Basis, want) {
			t.Errorf("basis %q lacks %q", e.Drills.Basis, want)
		}
	}
	if e.PlanningSeconds != nil {
		t.Error("nothing counts, so nothing plans the restore")
	}
}

// ── the edges of "comparable" ─────────────────────────────────────────────────

// The size tolerance is inclusive at 20% either way and exclusive past it. Sizes
// are in bytes, so the edge is exact: 12 GiB is exactly 20% over 10 GiB.
func TestAddDrills_SizeToleranceEdges(t *testing.T) {
	restore := 10 * gib
	for name, c := range map[string]struct {
		bytes int64
		count bool
	}{
		"exactly 20% over":   {restore + restore/5, true},
		"a byte past 20%":    {restore + restore/5 + 1, false},
		"exactly 20% under":  {restore - restore/5, true},
		"a byte past -20%":   {restore - restore/5 - 1, false},
		"the same size":      {restore, true},
		"half":               {restore / 2, false},
		"ten times":          {restore * 10, false},
		"one byte":           {1, false},
		"a very small drill": {restore / 1000, false},
	} {
		e := noRealRestores()
		AddDrills(&e, []audit.DrillRecord{drill("drl_x", 100, 5, c.bytes)}, DrillInputs{RestoreBytes: restore}, now, nil)
		if counted := e.Drills.Samples == 1; counted != c.count {
			t.Errorf("%s: counted=%v, want %v", name, counted, c.count)
		}
	}
}

func TestAddDrills_WindowEdge(t *testing.T) {
	at := func(age time.Duration) audit.DrillRecord {
		d := drill("drl_x", 100, 0, 10*gib)
		d.FinishedAt, d.RecordedAt = now.Add(-age), now.Add(-age)
		return d
	}
	for name, c := range map[string]struct {
		age   time.Duration
		count bool
	}{
		"exactly 90 days":   {DrillWindow, true},
		"a second past":     {DrillWindow + time.Second, false},
		"a second inside":   {DrillWindow - time.Second, true},
		"just now":          {0, true},
		"in the future too": {-time.Hour, true}, // a clock a little ahead is not "old"
	} {
		e := noRealRestores()
		AddDrills(&e, []audit.DrillRecord{at(c.age)}, DrillInputs{RestoreBytes: 10 * gib}, now, nil)
		if counted := e.Drills.Samples == 1; counted != c.count {
			t.Errorf("%s: counted=%v, want %v", name, counted, c.count)
		}
	}
	// With no finish time a drill is as old as it was recorded.
	d := drill("drl_x", 100, 0, 10*gib)
	d.FinishedAt, d.RecordedAt = time.Time{}, now.Add(-100*24*time.Hour)
	e := noRealRestores()
	AddDrills(&e, []audit.DrillRecord{d}, DrillInputs{RestoreBytes: 10 * gib}, now, nil)
	if e.Drills.Samples != 0 || e.Drills.NotCounted[notCountedOld] != 1 {
		t.Errorf("a drill with no finish time recorded 100 days ago: %+v", e.Drills)
	}
}

// Two drills are too few, three enough; nine plan from the longest, ten from a p90.
func TestAddDrills_SampleCountEdges(t *testing.T) {
	steps := func(n int) []int64 {
		var s []int64
		for i := 1; i <= n; i++ {
			s = append(s, int64(i)*10)
		}
		return s
	}
	for n, want := range map[int]struct {
		status string
		basis  string
	}{
		2:  {EstimateTooFew, ""},
		3:  {EstimateEstimated, PlanningLongest},
		9:  {EstimateEstimated, PlanningLongest},
		10: {EstimateEstimated, PlanningP90},
		11: {EstimateEstimated, PlanningP90},
	} {
		e := noRealRestores()
		AddDrills(&e, drillsTook(steps(n)...), DrillInputs{RestoreBytes: 10 * gib}, now, nil)
		if e.Drills.Status != want.status || e.Drills.PlanningBasis != want.basis {
			t.Errorf("%d drills: %q/%q, want %q/%q", n, e.Drills.Status, e.Drills.PlanningBasis, want.status, want.basis)
		}
		if want.status == EstimateEstimated && (want.basis == PlanningP90) != (e.Drills.P90Seconds != nil) {
			t.Errorf("%d drills: a p90 is offered only from %d, got %v", n, MinP90Samples, ptr(e.Drills.P90Seconds))
		}
	}
}

// The posture check has no restore in view: drills are sized against the newest.
func TestAddDrills_SizeFromTheNewestDrill(t *testing.T) {
	newest := drill("drl_new", 100, 1, 20*gib)
	older := drill("drl_old", 100, 30, 10*gib) // half the newest: not comparable
	near := drill("drl_near", 90, 10, 19*gib)  // within 20% of the newest
	snap := drill("drl_snap", 100, 0, 200*gib) // the very newest, but a snapshot: sets no size
	snap.Method = audit.DrillMethodSnapshot
	e := noRealRestores()
	AddDrills(&e, []audit.DrillRecord{snap, newest, near, older}, DrillInputs{SizeFromNewestDrill: true}, now, nil)
	d := e.Drills
	if d.Samples != 2 || d.NotCounted[notCountedSize] != 1 || d.NotCounted[notCountedMethod] != 1 {
		t.Errorf("sized against the newest pgBackRest drill: %+v", d)
	}
	if !strings.Contains(d.Basis, "sized against the newest drill") {
		t.Errorf("the basis does not say what it was sized against: %q", d.Basis)
	}
	// An explicit size wins over the flag.
	e = noRealRestores()
	AddDrills(&e, []audit.DrillRecord{newest, older}, DrillInputs{RestoreBytes: 10 * gib, SizeFromNewestDrill: true}, now, nil)
	if e.Drills.Samples != 1 || strings.Contains(e.Drills.Basis, "sized against the newest") {
		t.Errorf("an explicit size must win: %+v", e.Drills)
	}
	// No drill has a size to go by: not comparable, and said so.
	nosize := drill("drl_ns", 100, 1, 0)
	e = noRealRestores()
	AddDrills(&e, []audit.DrillRecord{nosize}, DrillInputs{SizeFromNewestDrill: true}, now, nil)
	if e.Drills.Samples != 0 || e.Drills.NotCounted[notCountedSizeUnknown] != 1 {
		t.Errorf("no size anywhere: %+v", e.Drills)
	}
	// The newest drill of another system sets no size for this one.
	other := drill("drl_other", 100, 0, 500*gib)
	other.SourceSystemID = "9999"
	e = noRealRestores()
	AddDrills(&e, []audit.DrillRecord{other, newest}, DrillInputs{SizeFromNewestDrill: true, SystemID: "7001"}, now, nil)
	if e.Drills.Samples != 1 || e.Drills.NotCounted[notCountedOtherSystem] != 1 {
		t.Errorf("another system's size leaked in: %+v", e.Drills)
	}
}

func TestAddDrills_NamesTheDrillsItCounted(t *testing.T) {
	e := noRealRestores()
	all := drillsTook(60, 90, 120)
	snap := drill("drl_snap", 100, 3, 10*gib)
	snap.Method = audit.DrillMethodSnapshot
	AddDrills(&e, append(all, snap), DrillInputs{RestoreBytes: 10 * gib}, now, nil)
	got := strings.Join(e.Drills.DrillIDs, ",")
	if got != "drl_a,drl_b,drl_c" {
		t.Errorf("ids = %q: only the drills that count, so a plan can be traced to them", got)
	}
}

// Whatever drills say, nothing a plan reads from this server's real restores
// changes: the two sets are never mixed, field by field.
func TestAddDrills_NeverTouchesTheRealRestoreFigures(t *testing.T) {
	end := func(h int) string { return now.Add(-time.Duration(h) * time.Hour).Format(time.RFC3339) }
	start := func(h, took int) string {
		return now.Add(-time.Duration(h)*time.Hour - time.Duration(took)*time.Minute).Format(time.RFC3339)
	}
	run := func(h, took int) string {
		return `{"series_id": "` + restoreSeries + `", "outcome": "resolved", "started_at": "` + start(h, took) + `", "completed_at": "` + end(h) + `"}`
	}
	for name, raw := range map[string]string{
		"none":  `{"count":0,"incidents":[]}`,
		"two":   `{"count":2,"incidents":[{"incident_id":"a","origin":"real","runs":[` + run(30, 10) + `]},{"incident_id":"b","origin":"real","runs":[` + run(60, 20) + `]}]}`,
		"three": `{"count":3,"incidents":[{"incident_id":"a","origin":"real","runs":[` + run(30, 10) + `]},{"incident_id":"b","origin":"real","runs":[` + run(60, 20) + `]},{"incident_id":"c","origin":"real","runs":[` + run(90, 50) + `]}]}`,
	} {
		l := mustListing(t, raw)
		before := EstimateRecovery(l, []string{restoreSeries}, now, EstimateWindow, rto(30*time.Minute))
		after := EstimateRecovery(l, []string{restoreSeries}, now, EstimateWindow, rto(30*time.Minute))
		AddDrills(&after, drillsTook(1, 2, 3, 4, 5, 6, 7, 8, 9, 10), DrillInputs{RestoreBytes: 10 * gib}, now, rto(30*time.Minute))
		// Everything but the drills and, when real restores are too few, the planning figure.
		sameInt := func(a, b *int64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
		if after.Status != before.Status || after.Samples != before.Samples || after.Excluded != before.Excluded ||
			after.Basis != before.Basis || after.VsRTO != before.VsRTO || after.VsRTODetail != before.VsRTODetail ||
			!sameInt(after.P50Seconds, before.P50Seconds) || !sameInt(after.P90Seconds, before.P90Seconds) ||
			!sameInt(after.MinSeconds, before.MinSeconds) || !sameInt(after.MaxSeconds, before.MaxSeconds) {
			t.Errorf("%s: drills changed the real-restore figures:\n before %+v\n after  %+v", name, before, after)
		}
		if before.Status == EstimateEstimated && (after.PlanningSource != PlanningFromRestores || !sameInt(after.PlanningSeconds, before.PlanningSeconds)) {
			t.Errorf("%s: enough real restores, yet the plan moved to %q", name, after.PlanningSource)
		}
	}
}

// ── the verdict ───────────────────────────────────────────────────────────────

func drillEstimate(secs ...int64) *RecoveryEstimate {
	e := noRealRestores()
	AddDrills(&e, drillsTook(secs...), DrillInputs{RestoreBytes: 10 * gib}, now, nil)
	return &e
}

func TestCompareToBudget_PlansFromDrillsAndSaysItIsACopy(t *testing.T) {
	l := ledgerSpending(rtoBudget(), 10*time.Minute) // 33m12s left
	in := inputs(rtoBudget(), l)

	// Three drills, the longest 40m: more than is left.
	in.Estimate = drillEstimate(20*60, 30*60, 40*60)
	v := CompareToBudget(in)
	if v.Status != BudgetMayExceed || v.DurationBasis != DurationFromDrillLongest || *v.DurationSeconds != 40*60 {
		t.Fatalf("%+v", v)
	}
	for _, frag := range []string{"only 33m12s left", "this server's restore drills have taken up to 40m, the longest of 3 drills, demonstrated on a copy, not on this server"} {
		if !strings.Contains(v.Detail, frag) {
			t.Errorf("detail %q lacks %q", v.Detail, frag)
		}
	}
	if strings.Contains(v.Detail, "past restores") {
		t.Errorf("drills described as this server's past restores: %q", v.Detail)
	}

	// Quick drills fit.
	in.Estimate = drillEstimate(5*60, 6*60, 7*60)
	v = CompareToBudget(in)
	if v.Status != BudgetWithin || v.DurationBasis != DurationFromDrillLongest ||
		!strings.Contains(v.Detail, "enough for a restore as long as this server's drills took (7m, the longest of 3 drills, demonstrated on a copy, not on this server)") {
		t.Errorf("quick: %+v", v)
	}

	// Ten: a p90.
	in.Estimate = drillEstimate(60, 120, 180, 240, 300, 360, 420, 480, 540, 3600)
	v = CompareToBudget(in)
	if v.DurationBasis != DurationFromDrillP90 || *v.DurationSeconds != 540 || !strings.Contains(v.Detail, "at the p90 of 10 drills") {
		t.Errorf("ten: %+v", v)
	}
	if got := durationPhrase(v); got != "the p90 of 10 drills on a copy" {
		t.Errorf("phrase %q", got)
	}
	in.Estimate = drillEstimate(60, 120, 180)
	if got := durationPhrase(CompareToBudget(in)); got != "the longest of 3 drills on a copy" {
		t.Errorf("phrase %q", got)
	}
}

func TestCompareToBudget_RealRestoresOutrankDrills(t *testing.T) {
	l := ledgerSpending(rtoBudget(), 10*time.Minute)
	in := inputs(rtoBudget(), l)
	e := estimateOf(20, 25, 30) // three real restores
	AddDrills(e, drillsTook(5, 6, 7), DrillInputs{RestoreBytes: 10 * gib}, now, nil)
	in.Estimate = e
	v := CompareToBudget(in)
	if v.DurationBasis != DurationFromHistoryLongest || *v.DurationSeconds != 30*60 {
		t.Errorf("drills replaced real history: %+v", v)
	}
}

func TestCompareToBudget_DrillsTooFewFallBackToTheRTO(t *testing.T) {
	l := ledgerSpending(rtoBudget(), 20*time.Minute)
	in := inputs(rtoBudget(), l)
	in.Estimate = drillEstimate(60, 70)
	if v := CompareToBudget(in); v.DurationBasis != DurationFromRTO {
		t.Errorf("two drills: %+v", v)
	}
}

// ── the client ────────────────────────────────────────────────────────────────

func TestClient_RecoveryEstimateReadsTheDrillsOfTheDatabase(t *testing.T) {
	aud := &recordingAuditd{mine: `{"count": 0, "incidents": []}`, drills: drillsJSON(t, drillsTook(60, 90, 120))}
	srv := aud.serve(t)
	c := &Client{URL: srv.URL, APIKey: "svc-key"}
	e, err := c.RecoveryEstimate(context.Background(), "prod-orders", RestoreSeries, nil, now, DrillInputs{RestoreBytes: 10 * gib})
	if err != nil {
		t.Fatal(err)
	}
	if e.Drills == nil || e.Drills.Samples != 3 || e.PlanningSource != PlanningFromDrills || *e.PlanningSeconds != 120 {
		t.Fatalf("estimate: %+v", e)
	}
	var q map[string][]string
	for _, got := range aud.queries {
		if got.Get("source") != "" {
			q = got
		}
	}
	if q == nil || q["source"][0] != "prod-orders" || q["current"][0] != "true" || q["limit"][0] != "500" {
		t.Errorf("drills query = %v", q)
	}
	since, err := time.Parse(time.RFC3339, q["since"][0])
	if err != nil || now.Add(-DrillWindow).Sub(since) > time.Second || since.Sub(now.Add(-DrillWindow)) > time.Second {
		t.Errorf("since = %v (%v), want %s before now", q["since"], err, DrillWindow)
	}
	for i, a := range aud.auth {
		if a != "Bearer svc-key" {
			t.Errorf("request %d authorization = %q", i, a)
		}
	}
}

// A drills read that fails does not fail the estimate and is not read as "no
// drills": it is reported as unavailable, and the real restores stand.
func TestClient_RecoveryEstimateSurvivesUnreadableDrills(t *testing.T) {
	aud := &recordingAuditd{mine: `{"count": 0, "incidents": []}`, drillsStatus: 500}
	srv := aud.serve(t)
	e, err := (&Client{URL: srv.URL}).RecoveryEstimate(context.Background(), "prod-orders", RestoreSeries, nil, now, DrillInputs{RestoreBytes: 10 * gib})
	if err != nil {
		t.Fatalf("an unreadable drills list failed the estimate: %v", err)
	}
	if e.Drills == nil || e.Drills.Status != EstimateUnavailable || !strings.Contains(e.Drills.Basis, "could not be read") {
		t.Errorf("drills: %+v", e.Drills)
	}
	if e.PlanningSeconds != nil {
		t.Error("nothing plans the restore when drills cannot be read and there is no history")
	}
}

func TestClient_DrillsErrors(t *testing.T) {
	if _, err := (&Client{}).Drills(context.Background(), "x", now); err != ErrNoAudit {
		t.Errorf("no audit service: %v", err)
	}
	aud := &recordingAuditd{drills: `not json`}
	srv := aud.serve(t)
	if _, err := (&Client{URL: srv.URL}).Drills(context.Background(), "x", now); err == nil {
		t.Error("an unreadable answer read as a list")
	}
}
