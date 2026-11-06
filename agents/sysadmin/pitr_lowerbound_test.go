package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// lbWAL is an archive of 16 MiB segments 0..20 on timeline 1, each archived a
// minute after the one before, the last at 12:20:00 UTC.
func lbWAL() *WALContinuity {
	end := time.Date(2026, 11, 4, 12, 20, 0, 0, time.UTC)
	segs := map[uint64]archivedSegment{}
	for s := uint64(0); s <= 20; s++ {
		segs[s] = archivedSegment{timeline: 1, time: end.Add(-time.Duration(20-s) * time.Minute).Unix()}
	}
	return &WALContinuity{
		Status: WALContinuous, SegmentSizeBytes: 16 << 20,
		ArchiveEndWAL: "000000010000000000000014", ArchiveEndTime: end.Format(time.RFC3339),
		segments: segs,
	}
}

const lbSegment = 16 << 20

func TestArchivedAt(t *testing.T) {
	w := lbWAL()
	// LSN 0/0A000100 is in segment 10, archived ten minutes before the end.
	tli, at, segno, ok := w.archivedAt(10*lbSegment + 0x100)
	if !ok || tli != 1 || segno != 10 || !at.Equal(time.Date(2026, 11, 4, 12, 10, 0, 0, time.UTC)) {
		t.Errorf("archivedAt = %d %v %d %v", tli, at, segno, ok)
	}
	// The first and last byte of a segment are in it; the next byte is not.
	if _, _, s, _ := w.archivedAt(11*lbSegment - 1); s != 10 {
		t.Errorf("last byte of segment 10 is in %d", s)
	}
	if _, _, s, _ := w.archivedAt(11 * lbSegment); s != 11 {
		t.Errorf("first byte of segment 11 is in %d", s)
	}
	// Not archived, or nothing to look in: no answer, never a guess.
	if _, _, _, ok := w.archivedAt(21 * lbSegment); ok {
		t.Error("a segment past the archive has an archive time")
	}
	w.segments[5] = archivedSegment{timeline: 1, time: 0}
	if _, _, _, ok := w.archivedAt(5 * lbSegment); ok {
		t.Error("a segment with no recorded time has one")
	}
	var none *WALContinuity
	if _, _, _, ok := none.archivedAt(0); ok {
		t.Error("a nil check answered")
	}
	if _, _, _, ok := (&WALContinuity{}).archivedAt(0); ok {
		t.Error("a check with no segment size or segments answered")
	}
	// In memory only: a plan's JSON does not carry thousands of segments.
	raw, _ := json.Marshal(w)
	if strings.Contains(string(raw), "segments") {
		t.Errorf("the segment map is in the JSON: %s", raw)
	}
}

// The real check fills the map from the repository listing.
func TestEvaluateWALContinuity_KeepsEachArchivedSegmentsTime(t *testing.T) {
	old := cmdRunner
	cmdRunner = fixtureRunner(t)
	defer func() { cmdRunner = old }()
	plan, err := planPITRImpl(context.Background(), PlanPITRArgs{RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:00Z"})
	if err != nil || plan.WALContinuity == nil {
		t.Fatalf("%+v, %v", plan, err)
	}
	w := plan.WALContinuity
	if len(w.segments) == 0 {
		t.Fatal("no segment times kept")
	}
	// The newest segment's time is the archive end time the check reports.
	_, logID, seg, err := parseSegmentName(w.ArchiveEndWAL)
	if err != nil {
		t.Fatal(err)
	}
	endSeg := logID*segsPerLogID(w.SegmentSizeBytes) + seg
	_, at, _, ok := w.archivedAt(endSeg * w.SegmentSizeBytes)
	if !ok || at.UTC().Format(time.RFC3339) != w.ArchiveEndTime {
		t.Errorf("end segment time = %v (%v), archive end time = %s", at, ok, w.ArchiveEndTime)
	}
	t.Logf("fixture: %d segments, archive end %s at %s, segment size %d", len(w.segments), w.ArchiveEndWAL, w.ArchiveEndTime, w.SegmentSizeBytes)
}

func TestLossAtLeast(t *testing.T) {
	w := lbWAL()
	lsn := uint64(10*lbSegment + 0x100) // segment 10, archived 12:10:00

	t.Run("on the archive's own clock: end of archive minus the segment's time, no allowance", func(t *testing.T) {
		secs, basis, ok := lossAtLeast(w, lsn, nil)
		if !ok || secs != 600 {
			t.Fatalf("secs = %d, ok = %v", secs, ok)
		}
		for _, want := range []string{"00000001000000000000000A", "2026-11-04T12:10:00Z", "nothing is archived before it is written", "the end of the archive"} {
			if !strings.Contains(basis, want) {
				t.Errorf("basis %q lacks %q", basis, want)
			}
		}
		if strings.Contains(basis, "allowance") {
			t.Errorf("an allowance for clock difference where the clocks are one: %q", basis)
		}
	})

	t.Run("from the running instance's clock: less the allowance for clock difference", func(t *testing.T) {
		live := &LivePreState{Time: "2026-11-04T12:40:00Z"}
		secs, basis, ok := lossAtLeast(w, lsn, live)
		if !ok || secs != 30*60-60 {
			t.Fatalf("secs = %d, ok = %v: want 30 minutes less the 1 minute allowance", secs, ok)
		}
		if !strings.Contains(basis, "running instance's clock") || !strings.Contains(basis, "allowance for clock difference") {
			t.Errorf("basis = %q", basis)
		}
	})

	t.Run("the allowance is a variable the bound follows", func(t *testing.T) {
		old := clockSkewMargin
		clockSkewMargin = 5 * time.Minute
		defer func() { clockSkewMargin = old }()
		if secs, _, ok := lossAtLeast(w, lsn, &LivePreState{Time: "2026-11-04T12:40:00Z"}); !ok || secs != 25*60 {
			t.Errorf("secs = %d, ok = %v", secs, ok)
		}
	})

	t.Run("a bound that is not positive is no bound", func(t *testing.T) {
		// 30 seconds after the segment arrived, with a one minute allowance.
		if _, _, ok := lossAtLeast(w, lsn, &LivePreState{Time: "2026-11-04T12:10:30Z"}); ok {
			t.Error("a bound of less than the allowance was reported")
		}
		// The very last segment, measured to the end of the archive: zero.
		if _, _, ok := lossAtLeast(w, 20*lbSegment, nil); ok {
			t.Error("a bound of zero was reported")
		}
	})

	t.Run("nothing to measure from", func(t *testing.T) {
		if _, _, ok := lossAtLeast(w, 25*lbSegment, nil); ok {
			t.Error("a target past the archive has a bound")
		}
		if _, _, ok := lossAtLeast(nil, lsn, nil); ok {
			t.Error("no check, a bound")
		}
		bad := lbWAL()
		bad.ArchiveEndTime = "garbage"
		if _, _, ok := lossAtLeast(bad, lsn, nil); ok {
			t.Error("an unreadable archive end time gave a bound")
		}
		// An unreadable instance clock falls back to the archive's.
		if secs, _, ok := lossAtLeast(w, lsn, &LivePreState{Time: "garbage"}); !ok || secs != 600 {
			t.Errorf("fallback: secs = %d, ok = %v", secs, ok)
		}
	})
}

func TestEstimateDataLoss_LSNCarriesTheLowerBound(t *testing.T) {
	w := lbWAL()
	target := RecoveryTarget{Type: TargetLSN, Value: "0/0A000100"}

	e := estimateDataLoss(target, "0/0A000100", w, nil, lossNow)
	if e == nil || e.WindowAtLeastSeconds == nil || *e.WindowAtLeastSeconds != 600 || e.AtLeastBasis == "" {
		t.Fatalf("estimate = %+v", e)
	}
	// An LSN still has no exact span, and the figures say which way they are wrong.
	if e.WindowSeconds != nil {
		t.Errorf("an exact window for an LSN: %d", *e.WindowSeconds)
	}
	for _, want := range []string{"at most", "of WAL", "at least 10m 00s of history", "discarded"} {
		if !strings.Contains(e.Summary, want) {
			t.Errorf("summary %q lacks %q", e.Summary, want)
		}
	}
	if joined := strings.Join(e.Unmeasured, "|"); !strings.Contains(joined, "only a lower bound is known") {
		t.Errorf("unmeasured = %q", joined)
	}

	// Running: the bytes are exact and the bound is from the instance's clock.
	live := &LivePreState{LSN: "0/0B000100", Time: "2026-11-04T12:40:00Z"}
	e = estimateDataLoss(target, "0/0A000100", w, live, lossNow)
	if e == nil || *e.WindowAtLeastSeconds != 30*60-60 || *e.WALBytes != 16<<20 {
		t.Fatalf("live estimate = %+v", e)
	}
	if strings.Contains(e.Summary, "at most") {
		t.Errorf("a running instance's exact bytes are called an upper bound: %q", e.Summary)
	}

	// No bound where there is no segment: the old wording, unchanged.
	e = estimateDataLoss(RecoveryTarget{Type: TargetLSN}, "0/20000000", w, nil, lossNow)
	if e == nil || e.WindowAtLeastSeconds != nil || !strings.Contains(strings.Join(e.Unmeasured, "|"), "the time span (an LSN has no time") {
		t.Errorf("past the archive: %+v", e)
	}
	// A time target is untouched.
	e = estimateDataLoss(RecoveryTarget{Type: TargetTime, Value: "2026-11-04T12:00:00Z"}, "x", w, nil, lossNow)
	if e == nil || e.WindowAtLeastSeconds != nil || e.WindowSeconds == nil {
		t.Errorf("time target: %+v", e)
	}
}

func atLeast(secs int64) *DataLossEstimate {
	b := uint64(16 << 20)
	return &DataLossEstimate{WindowAtLeastSeconds: &secs, WALBytes: &b, UpperBound: true}
}

// A lower bound that exceeds the rpo is a proven breach, and enforcement applies.
func TestCompareToRPO_ALowerBoundCanProveABreach(t *testing.T) {
	fifteen := 15 * time.Minute

	over := compareToRPO(atLeast(4687), objWith(fifteen, "refuse"), true)
	if over.Status != RPOExceeds || over.Gate != GateRefuse {
		t.Fatalf("status=%s gate=%q: %s", over.Status, over.Gate, over.Detail)
	}
	for _, want := range []string{"at least 1h 18m", "exceeds it by at least 1h 03m", "a lower bound"} {
		if !strings.Contains(over.Detail, want) {
			t.Errorf("detail %q lacks %q", over.Detail, want)
		}
	}
	if g := compareToRPO(atLeast(4687), objWith(fifteen, "acknowledge"), true).Gate; g != GateAcknowledge {
		t.Errorf("acknowledge: gate = %q", g)
	}
	if g := compareToRPO(atLeast(4687), objWith(fifteen, "warn"), true).Gate; g != "" {
		t.Errorf("warn: gate = %q", g)
	}

	// Within the rpo, a lower bound proves nothing: it says what it knows and
	// is not enforced.
	under := compareToRPO(atLeast(300), objWith(fifteen, "refuse"), true)
	if under.Status != RPONotComparable || under.Gate != "" {
		t.Fatalf("status=%s gate=%q", under.Status, under.Gate)
	}
	for _, want := range []string{"at least 5m 00s of history", "within the objective", "the real loss may be more"} {
		if !strings.Contains(under.Detail, want) {
			t.Errorf("detail %q lacks %q", under.Detail, want)
		}
	}
	// Exactly at the rpo is not over it.
	if at := compareToRPO(atLeast(900), objWith(fifteen, "refuse"), true); at.Status != RPONotComparable {
		t.Errorf("a bound equal to the rpo proved a breach: %+v", at)
	}
	// An exact window still takes precedence over any bound.
	exact := atLeast(4687)
	w := int64(60)
	exact.WindowSeconds = &w
	if got := compareToRPO(exact, objWith(fifteen, "refuse"), true); got.Status != RPOWithin {
		t.Errorf("an exact window was ignored for the bound: %+v", got)
	}
}

// The whole path, on the fixture archive, for an LSN target inside it.
func TestPlanAndRestore_LSNTargetBreachIsEnforced(t *testing.T) {
	const lsnTarget = "0/4000000"
	plan := func(enf string) (PITRPlan, *liveSim) {
		enforced(t, "15m", enf)
		sim, _ := liveEnv(t)
		p, err := planPITRWithEstimate(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main",
			RecoveryTargetType: TargetLSN, RecoveryTargetVal: lsnTarget})
		if err != nil || p.Status != PITRFeasible {
			t.Fatalf("plan: %+v, %v", p, err)
		}
		return p, sim
	}
	restore := func(p PITRPlan, ack bool) (RestoreToPITResult, error) {
		return restoreToPITImpl(context.Background(), RestoreToPITArgs{Target: "pgbackrest_db", Stanza: "main",
			RecoveryTargetType: TargetLSN, RecoveryTargetVal: lsnTarget, PlanDigest: p.PlanDigest,
			AllowStopServing: true, AcknowledgeObjectiveBreach: ack})
	}

	// refuse: a proven breach is refused, acknowledged or not, before any stop.
	p, sim := plan("refuse")
	e := p.DataLossEstimate
	t.Logf("estimate: %s", e.Summary)
	if e.WindowAtLeastSeconds == nil || e.Objective == nil || e.Objective.Status != RPOExceeds || e.Objective.Gate != GateRefuse {
		t.Fatalf("estimate = %+v, objective = %+v", e, e.Objective)
	}
	for _, ack := range []bool{false, true} {
		res, err := restore(p, ack)
		if err == nil || !strings.Contains(err.Error(), "is refused") || sim.did("stop") || !sim.up {
			t.Errorf("refuse, ack=%v: err=%v order=%s up=%v", ack, err, sim.order(), sim.up)
		}
		if res.DataLoss == nil || res.DataLoss.WindowAtLeastSeconds == nil {
			t.Errorf("the refusal should carry the bound it rested on: %+v", res.DataLoss)
		}
	}

	// acknowledge: asks, and goes ahead once acknowledged.
	p, sim = plan("acknowledge")
	if _, err := restore(p, false); err == nil || !strings.Contains(err.Error(), "acknowledge_objective_breach") || sim.did("stop") {
		t.Errorf("acknowledge, not acknowledged: err=%v order=%s", err, sim.order())
	}
	if _, err := restore(p, true); err != nil || !sim.did("restore") {
		t.Errorf("acknowledge, acknowledged: err=%v order=%s", err, sim.order())
	}

	// warn: shown, never blocks.
	p, sim = plan("warn")
	if _, err := restore(p, false); err != nil || !sim.did("restore") {
		t.Errorf("warn: err=%v order=%s", err, sim.order())
	}
}

// The limit that remains, pinned: a target in WAL that is not archived yet has
// no segment to measure from, so it is still not enforced. The docs say so.
func TestPlanAndRestore_LSNTargetInUnarchivedWALIsStillNotComparable(t *testing.T) {
	enforced(t, "15m", "refuse")
	sim, _ := liveEnv(t)
	// Past the fixture archive's end, at or behind the instance's position.
	p, err := planPITRImpl(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main",
		RecoveryTargetType: TargetLSN, RecoveryTargetVal: "0/1F000000"})
	if err != nil || p.Status != PITRFeasible || !p.ArchiveFlushRequired {
		t.Skipf("the fixture archive does not end before this position (plan %+v, %v)", p, err)
	}
	pw, _ := planPITRWithEstimate(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main",
		RecoveryTargetType: TargetLSN, RecoveryTargetVal: "0/1F000000"})
	if o := pw.DataLossEstimate.Objective; o == nil || o.Status != RPONotComparable || o.Gate != "" || pw.DataLossEstimate.WindowAtLeastSeconds != nil {
		t.Errorf("objective = %+v, estimate = %+v", o, pw.DataLossEstimate)
	}
	if sim.did("stop") {
		t.Error("planning stopped the instance")
	}
}
