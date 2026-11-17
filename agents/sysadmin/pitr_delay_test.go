package main

import (
	"strings"
	"testing"
)

func liveEstimate(window int64, rpo *int64, status string, commits *int, atLeast bool) *DataLossEstimate {
	e := &DataLossEstimate{Basis: "live_instance", WindowSeconds: &window}
	if rpo != nil {
		e.Objective = &RPOComparison{Status: status, RPOSeconds: rpo}
	}
	if commits != nil {
		e.Transactions = &TransactionLoss{Status: "measured", Commits: commits, AtLeast: atLeast}
	}
	return e
}

func ptrInt(n int) *int { return &n }

// A serving instance keeps taking writes: the loss grows, and the rpo is the limit
// it runs towards.
func TestDataDelay_ServingInstanceGrowsTowardsTheRPO(t *testing.T) {
	d := dataDelayFor(liveEstimate(18*60, i64(30*60), RPOWithin, ptrInt(720), false), true)
	if d == nil || d.State != DataDelayGrowing {
		t.Fatalf("delay = %+v", d)
	}
	if d.UntilRPOSeconds == nil || *d.UntilRPOSeconds != 12*60 {
		t.Errorf("until rpo = %v", d.UntilRPOSeconds)
	}
	if d.CommitsPerMinute == nil || *d.CommitsPerMinute != 40 {
		t.Errorf("commits per minute = %v", d.CommitsPerMinute)
	}
	for _, want := range []string{
		"the recovery target is fixed and the instance keeps taking writes",
		"grows by a minute every minute: 18m 00s now",
		"against an rpo of 30m 00s; estimated, under current assumptions, in 12m 00s it would exceed it",
		"about 40 commits a minute",
	} {
		if !strings.Contains(d.Detail, want) {
			t.Errorf("missing %q in %q", want, d.Detail)
		}
	}
}

func TestDataDelay_AlreadyOverTheRPO(t *testing.T) {
	d := dataDelayFor(liveEstimate(65*60, i64(15*60), RPOExceeds, nil, false), true)
	if d == nil || d.UntilRPOSeconds != nil || !strings.Contains(d.Detail, "1h 05m 00s now, which already exceeds the rpo of 15m 00s by 50m 00s") {
		t.Errorf("delay = %+v", d)
	}
}

// An instance that is down writes nothing, so waiting costs downtime and not data.
func TestDataDelay_DownInstanceDoesNotGrow(t *testing.T) {
	d := dataDelayFor(&DataLossEstimate{Basis: "archive_end", UpperBound: true}, false)
	if d == nil || d.State != DataDelayFixed || !strings.Contains(d.Detail, "nothing is being written") || !strings.Contains(d.Detail, "costs is downtime") {
		t.Errorf("delay = %+v", d)
	}
}

func TestDataDelay_SaysNothingItCannotSay(t *testing.T) {
	if dataDelayFor(nil, true) != nil || dataDelayFor(nil, false) != nil {
		t.Error("no estimate, yet a delay")
	}
	// A serving instance that could not be asked where its history ends: the figure is
	// the archive's, which is not growing in step with the clock.
	if d := dataDelayFor(&DataLossEstimate{Basis: "archive_end", UpperBound: true}, true); d != nil {
		t.Errorf("archive basis on a serving instance: %+v", d)
	}
	// An LSN target has no time span to say it grows by.
	if d := dataDelayFor(&DataLossEstimate{Basis: "live_instance"}, true); d != nil {
		t.Errorf("no window: %+v", d)
	}
}

// Without an objective the growth is still said, with no limit to run towards; a
// count that is a lower bound, or zero, gives no rate.
func TestDataDelay_NoObjectiveAndNoRate(t *testing.T) {
	d := dataDelayFor(liveEstimate(300, nil, "", nil, false), true)
	if d == nil || d.UntilRPOSeconds != nil || strings.Contains(d.Detail, "rpo") || strings.Contains(d.Detail, "commits") {
		t.Errorf("no objective: %+v", d)
	}
	if d := dataDelayFor(liveEstimate(300, i64(3600), RPOWithin, ptrInt(50), true), true); d.CommitsPerMinute != nil {
		t.Errorf("a lower-bound count gave a rate: %+v", d)
	}
	if d := dataDelayFor(liveEstimate(300, i64(3600), RPOWithin, ptrInt(0), false), true); d.CommitsPerMinute != nil || strings.Contains(d.Detail, "commits") {
		t.Errorf("zero commits gave a rate: %+v", d)
	}
	// A small rate keeps a decimal; a large one does not.
	if d := dataDelayFor(liveEstimate(600, nil, "", ptrInt(15), false), true); !strings.Contains(d.Detail, "about 1.5 commits a minute") {
		t.Errorf("small rate: %q", d.Detail)
	}
}

// What waiting does to the data is part of the plan, not of its digest.
func TestPlan_DataDelayIsNotPartOfTheDigest(t *testing.T) {
	a := pitrPlanDigest("main", "20261111-121609F", "2026-11-12 00:45:31+00", 3)
	p := PITRPlan{DataDelay: &DataDelay{State: DataDelayGrowing, Detail: "x"}, PlanDigest: a}
	if p.PlanDigest != a || a != pitrPlanDigest("main", "20261111-121609F", "2026-11-12 00:45:31+00", 3) {
		t.Error("the digest is not a function of the plan's identity alone")
	}
}
