package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var lossNow = time.Date(2026, 11, 3, 2, 30, 0, 0, time.UTC)

func lossWAL() *WALContinuity {
	return &WALContinuity{
		Status:           WALContinuous,
		ArchiveEndWAL:    "000000010000000000000010", // segment 16
		ArchiveEndTime:   "2026-11-03T02:20:00Z",
		SegmentSizeBytes: 16 << 20,
	}
}

func TestEstimateDataLoss_TimeTarget(t *testing.T) {
	target := RecoveryTarget{Type: TargetTime, Value: "2026-11-03T01:59:00Z"}

	t.Run("down: the end of the archive, an upper bound", func(t *testing.T) {
		e := estimateDataLoss(target, "2026-11-03 01:59:00+00", lossWAL(), nil, lossNow)
		if e == nil {
			t.Fatal("no estimate")
		}
		if e.Basis != lossBasisArchive || !e.UpperBound {
			t.Errorf("basis=%q upper_bound=%v", e.Basis, e.UpperBound)
		}
		if e.WindowSeconds == nil || *e.WindowSeconds != 21*60 {
			t.Errorf("window = %v, want 1260s", e.WindowSeconds)
		}
		if e.WALBytes != nil {
			t.Errorf("a time target has no WAL position without reading WAL, got %d bytes", *e.WALBytes)
		}
		if !strings.Contains(e.Summary, "at most 21m 00s of history") {
			t.Errorf("summary = %q", e.Summary)
		}
	})

	t.Run("serving: the instance's own clock, exact", func(t *testing.T) {
		live := &LivePreState{Time: "2026-11-03T02:29:30Z", LSN: "0/12000000"}
		e := estimateDataLoss(target, "2026-11-03 01:59:00+00", lossWAL(), live, lossNow)
		if e == nil || e.Basis != lossBasisLive || e.UpperBound {
			t.Fatalf("estimate = %+v", e)
		}
		if *e.WindowSeconds != 30*60+30 {
			t.Errorf("window = %d, want 1830", *e.WindowSeconds)
		}
		if !strings.Contains(e.Summary, "30m 30s of history") || strings.Contains(e.Summary, "at most") {
			t.Errorf("summary = %q", e.Summary)
		}
	})

	t.Run("a live clock that cannot be read falls back to the archive", func(t *testing.T) {
		e := estimateDataLoss(target, "x", lossWAL(), &LivePreState{Time: "garbage"}, lossNow)
		if e == nil || e.Basis != lossBasisArchive {
			t.Errorf("estimate = %+v", e)
		}
	})

	t.Run("a target after the end is zero, never negative", func(t *testing.T) {
		late := RecoveryTarget{Type: TargetTime, Value: "2026-11-03T03:00:00Z"}
		e := estimateDataLoss(late, "x", lossWAL(), nil, lossNow)
		if e == nil || *e.WindowSeconds != 0 {
			t.Errorf("estimate = %+v", e)
		}
	})

	t.Run("nothing to measure from", func(t *testing.T) {
		if e := estimateDataLoss(target, "x", &WALContinuity{}, nil, lossNow); e != nil {
			t.Errorf("an archive with no end time still gave %+v", e)
		}
		if e := estimateDataLoss(RecoveryTarget{Type: TargetTime, Value: "soon"}, "x", lossWAL(), nil, lossNow); e != nil {
			t.Errorf("an unreadable target still gave %+v", e)
		}
	})
}

func TestEstimateDataLoss_LSNTarget(t *testing.T) {
	target := RecoveryTarget{Type: TargetLSN, Value: "0/0A000100"}

	t.Run("down: from the target to the end of the newest archived segment", func(t *testing.T) {
		e := estimateDataLoss(target, "0/0A000100", lossWAL(), nil, lossNow)
		if e == nil {
			t.Fatal("no estimate")
		}
		// Segment 10 holds the target; segment 16 is the last archived: the
		// bytes run to the end of segment 16.
		want := uint64(7*(16<<20) - 0x100)
		if e.WALBytes == nil || *e.WALBytes != want {
			t.Errorf("wal bytes = %v, want %d", e.WALBytes, want)
		}
		if e.Segments == nil || *e.Segments != 6 {
			t.Errorf("segments = %v, want 6", e.Segments)
		}
		if !e.UpperBound || e.WindowSeconds != nil {
			t.Errorf("upper_bound=%v window=%v", e.UpperBound, e.WindowSeconds)
		}
	})

	t.Run("serving: the position difference, exact", func(t *testing.T) {
		live := &LivePreState{LSN: "0/0B000100", Time: "2026-11-03T02:29:30Z"}
		e := estimateDataLoss(target, "0/0A000100", lossWAL(), live, lossNow)
		if e == nil || e.Basis != lossBasisLive || e.UpperBound {
			t.Fatalf("estimate = %+v", e)
		}
		if *e.WALBytes != 16<<20 {
			t.Errorf("wal bytes = %d, want 16 MiB", *e.WALBytes)
		}
		if e.To != "2026-11-03T02:29:30Z" {
			t.Errorf("to = %q", e.To)
		}
		if !strings.Contains(e.Summary, "16.0 MiB of WAL") {
			t.Errorf("summary = %q", e.Summary)
		}
	})

	t.Run("an instance behind the target loses nothing, not a wrapped-around number", func(t *testing.T) {
		live := &LivePreState{LSN: "0/09000000"}
		e := estimateDataLoss(target, "0/0A000100", lossWAL(), live, lossNow)
		if e == nil || *e.WALBytes != 0 {
			t.Errorf("estimate = %+v", e)
		}
	})

	t.Run("unknown segment size or end", func(t *testing.T) {
		w := lossWAL()
		w.SegmentSizeBytes = 0
		if e := estimateDataLoss(target, "0/0A000100", w, nil, lossNow); e != nil {
			t.Errorf("got %+v", e)
		}
		if e := estimateDataLoss(target, "0/0A000100", nil, nil, lossNow); e != nil {
			t.Errorf("got %+v", e)
		}
	})
}

func TestEstimateDataLoss_SaysWhatItDoesNotCount(t *testing.T) {
	e := estimateDataLoss(RecoveryTarget{Type: TargetTime, Value: "2026-11-03T01:59:00Z"}, "x", lossWAL(), nil, lossNow)
	joined := strings.Join(e.Unmeasured, "|")
	for _, want := range []string{"number of transactions", "which of the discarded writes were wanted"} {
		if !strings.Contains(joined, want) {
			t.Errorf("unmeasured %q lacks %q", joined, want)
		}
	}
	if !strings.Contains(e.Summary, "Transactions are not counted") {
		t.Errorf("summary = %q", e.Summary)
	}
}

// The dry run carries the estimate; the plan the restore recomputes around its
// ordered steps does not (it would ask the running instance an extra question
// each time), and an infeasible plan has nothing to estimate.
func TestPlanPITRWithEstimate_EndToEnd(t *testing.T) {
	old := cmdRunner
	cmdRunner = fixtureRunner(t)
	t.Cleanup(func() { cmdRunner = old })
	ctx := context.Background()

	for _, c := range []struct{ typ, val string }{
		{TargetTime, "2026-10-09T00:00:00Z"},
		{TargetLSN, "0/4000000"},
	} {
		plan, err := planPITRWithEstimate(ctx, PlanPITRArgs{RecoveryTargetType: c.typ, RecoveryTargetVal: c.val})
		if err != nil || plan.Status != PITRFeasible {
			t.Fatalf("%s: err=%v plan=%+v", c.typ, err, plan)
		}
		e := plan.DataLossEstimate
		if e == nil || e.Basis != lossBasisArchive || !e.UpperBound || e.Summary == "" {
			t.Errorf("%s: estimate = %+v", c.typ, e)
		}
		raw, _ := json.Marshal(plan)
		if !strings.Contains(string(raw), `"data_loss_estimate"`) {
			t.Errorf("%s: the estimate is not in the plan's JSON: %s", c.typ, raw)
		}
		bare, _ := planPITRImpl(ctx, PlanPITRArgs{RecoveryTargetType: c.typ, RecoveryTargetVal: c.val})
		if bare.DataLossEstimate != nil {
			t.Errorf("%s: planPITRImpl must not carry an estimate", c.typ)
		}
		if bare.PlanDigest != plan.PlanDigest {
			t.Errorf("%s: the estimate moved the digest: %s vs %s", c.typ, bare.PlanDigest, plan.PlanDigest)
		}
	}

	plan, _ := planPITRWithEstimate(ctx, PlanPITRArgs{RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-12T00:00:00Z"})
	if plan.Status != PITRNotFeasible || plan.DataLossEstimate != nil {
		t.Errorf("a plan that cannot be restored got an estimate: %+v", plan)
	}
}

func TestHumanUnits(t *testing.T) {
	for s, want := range map[int64]string{5: "5s", 65: "1m 05s", 3912: "1h 05m 12s", 90000: "1d 01h 00m"} {
		if got := humanSpan(s); got != want {
			t.Errorf("humanSpan(%d) = %q, want %q", s, got, want)
		}
	}
	for n, want := range map[uint64]string{512: "512 B", 2048: "2.0 KiB", 16 << 20: "16.0 MiB", 3 << 30: "3.0 GiB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
