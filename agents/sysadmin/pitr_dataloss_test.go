package main

import (
	"context"
	"encoding/json"
	"errors"
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

// A serving instance is asked where its history ends. The simulated one says
// 2026-10-28T10:00:00Z, 0/20000000; the target is 8 days 10 hours earlier.
func TestPlanPITRWithEstimate_ServingInstanceUsesItsOwnClock(t *testing.T) {
	withPgBackRestDockerInfra(t)
	sim, _ := liveEnv(t)
	args := PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget}

	plan, err := planPITRWithEstimate(context.Background(), args)
	if err != nil || plan.Status != PITRFeasible || !plan.InstanceServing {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	e := plan.DataLossEstimate
	if e == nil || e.Basis != lossBasisLive || e.UpperBound || e.To != "2026-10-28T10:00:00Z" {
		t.Fatalf("estimate = %+v", e)
	}
	if want := int64(8*86400 + 10*3600); e.WindowSeconds == nil || *e.WindowSeconds != want {
		t.Errorf("window = %v, want %d", e.WindowSeconds, want)
	}
	if !sim.did("preflight") {
		t.Error("the running instance was never asked")
	}

	// The keys helpdesk-client reads are part of the contract.
	raw, _ := json.Marshal(plan)
	var m struct {
		Estimate map[string]any `json:"data_loss_estimate"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"summary", "as_of", "unmeasured"} {
		if _, ok := m.Estimate[k]; !ok {
			t.Errorf("data_loss_estimate has no %q: %s", k, raw)
		}
	}
}

func TestPlanPITRWithEstimate_FallsBackToTheArchive(t *testing.T) {
	withPgBackRestDockerInfra(t)
	args := PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:00Z"}

	t.Run("a serving instance that cannot be asked", func(t *testing.T) {
		sim, _ := liveEnv(t)
		sim.preErr = errors.New("psql: connection refused")
		plan, err := planPITRWithEstimate(context.Background(), args)
		if err != nil || plan.Status != PITRFeasible || !plan.InstanceServing {
			t.Fatalf("plan = %+v, %v", plan, err)
		}
		if e := plan.DataLossEstimate; e == nil || e.Basis != lossBasisArchive || !e.UpperBound {
			t.Errorf("estimate = %+v: a failed question must not fail the plan or pretend to be exact", e)
		}
	})

	t.Run("a down instance is never asked", func(t *testing.T) {
		sim, _ := liveEnv(t)
		sim.up = false
		plan, err := planPITRWithEstimate(context.Background(), args)
		if err != nil || plan.Status != PITRFeasible || plan.InstanceServing {
			t.Fatalf("plan = %+v, %v", plan, err)
		}
		if e := plan.DataLossEstimate; e == nil || e.Basis != lossBasisArchive {
			t.Errorf("estimate = %+v", e)
		}
		if sim.did("preflight") {
			t.Error("a down instance was asked where it is")
		}
	})
}

// The restore reports what it discarded, measured when it ran.
func TestRestoreToPIT_ReportsTheLossItCaused(t *testing.T) {
	t.Run("serving: from the instance's position when it was stopped", func(t *testing.T) {
		withPgBackRestDockerInfra(t)
		_, _ = liveEnv(t)
		plan := approvedLiveDigest(t, liveTarget)
		res, err := restoreToPITImpl(context.Background(), liveArgs(plan))
		if err != nil {
			t.Fatal(err)
		}
		e := res.DataLoss
		if e == nil || e.Basis != lossBasisLive || e.UpperBound || e.To != "2026-10-28T10:00:00Z" {
			t.Fatalf("data_loss = %+v", e)
		}
		if want := int64(8*86400 + 10*3600); *e.WindowSeconds != want {
			t.Errorf("window = %d, want %d", *e.WindowSeconds, want)
		}
		if res.Plan.DataLossEstimate != nil {
			t.Error("the restore's plan carries an estimate: it is recomputed around the ordered steps and must stay out of them")
		}
	})

	t.Run("down: from the end of the archive", func(t *testing.T) {
		withPgBackRestDockerInfra(t)
		sim, _ := liveEnv(t)
		sim.up = false
		args := PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:00Z"}
		plan, err := planPITRImpl(context.Background(), args)
		if err != nil || plan.Status != PITRFeasible {
			t.Fatalf("plan: %+v, %v", plan, err)
		}
		res, err := restoreToPITImpl(context.Background(), RestoreToPITArgs{Target: "pgbackrest_db", Stanza: "main",
			RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:00Z", PlanDigest: plan.PlanDigest})
		if err != nil {
			t.Fatal(err)
		}
		if e := res.DataLoss; e == nil || e.Basis != lossBasisArchive || !e.UpperBound || e.WindowSeconds == nil || *e.WindowSeconds <= 0 {
			t.Errorf("data_loss = %+v", e)
		}
	})
}

// The sentence under "will be discarded" must agree with the estimate: a
// serving instance is stopped, so the loss runs to the stop even when the
// target is inside the archive and no flush was needed (SAMPLE027's VM run
// said "up to the end of the archive" for exactly that case).
func TestPlanDataLossSentence_FollowsTheInstanceState(t *testing.T) {
	withPgBackRestDockerInfra(t)
	sim, _ := liveEnv(t)
	for _, c := range []struct{ typ, val string }{
		{TargetTime, "2026-10-09T00:00:00Z"}, // inside the archive: no flush needed
		{TargetLSN, "0/4000000"},
	} {
		args := PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: c.typ, RecoveryTargetVal: c.val}

		sim.up = true
		plan, err := planPITRImpl(context.Background(), args)
		if err != nil || plan.Status != PITRFeasible || !plan.InstanceServing || plan.ArchiveFlushRequired {
			t.Fatalf("%s serving: %+v, %v", c.typ, plan, err)
		}
		if !strings.Contains(plan.DataLoss, "up to the moment the instance is stopped") {
			t.Errorf("%s serving, inside the archive: data_loss = %q", c.typ, plan.DataLoss)
		}

		sim.up = false
		plan, err = planPITRImpl(context.Background(), args)
		if err != nil || plan.Status != PITRFeasible || plan.InstanceServing {
			t.Fatalf("%s down: %+v, %v", c.typ, plan, err)
		}
		if !strings.Contains(plan.DataLoss, "up to the end of the archive") {
			t.Errorf("%s down: data_loss = %q", c.typ, plan.DataLoss)
		}
	}
}

func TestEstimateDataLoss_Boundaries(t *testing.T) {
	t.Run("an LSN at the very end of the archive is the last segment's remainder", func(t *testing.T) {
		// Segment 16 starts at 0x10000000; the target is its first byte.
		e := estimateDataLoss(RecoveryTarget{Type: TargetLSN}, "0/10000000", lossWAL(), nil, lossNow)
		if e == nil || *e.Segments != 0 || *e.WALBytes != 16<<20 {
			t.Errorf("estimate = %+v", e)
		}
	})
	t.Run("an LSN past the end of the archive on a down instance loses nothing it can see", func(t *testing.T) {
		e := estimateDataLoss(RecoveryTarget{Type: TargetLSN}, "0/20000000", lossWAL(), nil, lossNow)
		if e == nil || *e.WALBytes != 0 || *e.Segments != 0 {
			t.Errorf("estimate = %+v", e)
		}
	})
	t.Run("an LSN in the high log id", func(t *testing.T) {
		w := lossWAL()
		w.ArchiveEndWAL = "000000010000000200000003" // log 2, seg 3 -> segment 2*256+3
		e := estimateDataLoss(RecoveryTarget{Type: TargetLSN}, "2/01000000", w, nil, lossNow)
		if e == nil || *e.Segments != 2 {
			t.Errorf("estimate = %+v", e)
		}
	})
	t.Run("a target with a zone offset is measured in absolute time", func(t *testing.T) {
		// 03:59 at +02:00 is 01:59 UTC, the same instant as the other tests.
		e := estimateDataLoss(RecoveryTarget{Type: TargetTime, Value: "2026-11-03T03:59:00+02:00"}, "x", lossWAL(), nil, lossNow)
		if e == nil || *e.WindowSeconds != 21*60 {
			t.Errorf("estimate = %+v", e)
		}
	})
	t.Run("an unknown target type", func(t *testing.T) {
		if e := estimateDataLoss(RecoveryTarget{Type: "xid"}, "1", lossWAL(), nil, lossNow); e != nil {
			t.Errorf("got %+v", e)
		}
	})
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
