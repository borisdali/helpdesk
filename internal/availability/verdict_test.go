package availability

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
)

// 99.9% over 30 days: 43m12s of downtime allowed, and a 30m rto.
func rtoBudget() infra.AvailabilityBudget { return budget() }

func noRTOBudget() infra.AvailabilityBudget {
	return infra.ComputeBudget(99.9, 30*24*time.Hour, "environment:prod", 0, false)
}

// ledgerSpending is the ledger after one resolved outage of this length.
func ledgerSpending(b infra.AvailabilityBudget, spent time.Duration, extra ...Incident) *Ledger {
	var incs []Incident
	if spent > 0 {
		incs = append(incs, outage("spent", spent, time.Hour))
	}
	p := params(append(incs, extra...)...)
	p.Budget = b
	l := Compute(p)
	return &l
}

func inputs(b infra.AvailabilityBudget, l *Ledger) BudgetInputs {
	return BudgetInputs{Found: true, Budget: b, Ledger: l, Enforcement: infra.EnforceWarn, Serving: true}
}

func TestCompareToBudget_Statuses(t *testing.T) {
	tests := []struct {
		name   string
		in     BudgetInputs
		status string
		must   []string // fragments of the detail
	}{
		{"room for a restore that takes up to the rto", inputs(rtoBudget(), ledgerSpending(rtoBudget(), 10*time.Minute)), BudgetWithin,
			[]string{"10m of the 43m12s budget over 30d already spent", "33m12s left", "up to the rto of 30m"}},
		{"exactly the rto left still fits", inputs(rtoBudget(), ledgerSpending(rtoBudget(), 13*time.Minute+12*time.Second)), BudgetWithin,
			[]string{"30m left"}},
		{"a second less than the rto may not", inputs(rtoBudget(), ledgerSpending(rtoBudget(), 13*time.Minute+13*time.Second)), BudgetMayExceed,
			[]string{"only 29m59s left", "may take up to the rto of 30m", "a restore can finish sooner than its rto"}},
		{"less than the rto left", inputs(rtoBudget(), ledgerSpending(rtoBudget(), 20*time.Minute)), BudgetMayExceed,
			[]string{"only 23m12s left"}},
		{"the budget is spent", inputs(rtoBudget(), ledgerSpending(rtoBudget(), 50*time.Minute)), BudgetExceeds,
			[]string{"the budget is spent (-6m48s left)", "any further downtime breaches the SLO"}},
		{"exactly spent is spent", inputs(rtoBudget(), ledgerSpending(rtoBudget(), 43*time.Minute+12*time.Second)), BudgetExceeds,
			[]string{"(0s left)"}},
		{"room but no rto declared", inputs(noRTOBudget(), ledgerSpending(noRTOBudget(), 10*time.Minute)), BudgetNotComparable,
			[]string{"no rto is declared", "33m12s left"}},
		{"no rto, but spent: still a breach", inputs(noRTOBudget(), ledgerSpending(noRTOBudget(), 50*time.Minute)), BudgetExceeds, nil},
		{"an SLO that allows no downtime", inputs(infra.ComputeBudget(100, 30*24*time.Hour, "database", 30*time.Minute, true),
			ledgerSpending(infra.ComputeBudget(100, 30*24*time.Hour, "database", 30*time.Minute, true), 0)), BudgetExceeds,
			[]string{"the SLO allows no downtime"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := CompareToBudget(tc.in)
			if v.Status != tc.status {
				t.Fatalf("status = %s (%s), want %s", v.Status, v.Detail, tc.status)
			}
			for _, frag := range tc.must {
				if !strings.Contains(v.Detail, frag) {
					t.Errorf("detail %q lacks %q", v.Detail, frag)
				}
			}
			if v.BudgetSeconds == nil || v.RemainingSeconds == nil {
				t.Errorf("the numbers it rests on are missing: %+v", v)
			}
		})
	}
}

func TestCompareToBudget_NumbersAreCarried(t *testing.T) {
	v := CompareToBudget(inputs(rtoBudget(), ledgerSpending(rtoBudget(), 20*time.Minute)))
	if *v.BudgetSeconds != 2592 || *v.RemainingSeconds != 2592-1200 || *v.RTOSeconds != 1800 || v.WindowSource != "environment:prod" {
		t.Errorf("verdict = %+v", v)
	}
	if v.NeedsAttention() != true {
		t.Error("may_exceed needs attention")
	}
}

// What cannot be checked is said, and is not mistaken for room in the budget.
func TestCompareToBudget_WhatCannotBeChecked(t *testing.T) {
	t.Run("not a configured database", func(t *testing.T) {
		v := CompareToBudget(BudgetInputs{Found: false, Enforcement: infra.EnforceRefuse})
		if v.Status != BudgetNoObjective || !strings.Contains(v.Detail, "not a configured database") || v.Gate != "" {
			t.Errorf("%+v", v)
		}
	})
	t.Run("no SLO is set", func(t *testing.T) {
		v := CompareToBudget(BudgetInputs{Found: true, Budget: infra.AvailabilityBudget{Detail: "no availability_slo is set: no budget"}, Enforcement: infra.EnforceRefuse})
		if v.Status != BudgetNoObjective || !strings.Contains(v.Detail, "no availability SLO is set") || v.Gate != "" {
			t.Errorf("%+v", v)
		}
	})
	t.Run("the SLO or window could not be read", func(t *testing.T) {
		v := CompareToBudget(BudgetInputs{Found: true, Invalid: true, Problems: []string{`db_servers.a.objectives: invalid duration "a month"`}, Enforcement: infra.EnforceRefuse, Serving: true})
		if v.Status != BudgetObjectiveInvalid || !strings.Contains(v.Detail, "a month") || len(v.Problems) != 1 {
			t.Errorf("%+v", v)
		}
		// It asks, and never refuses, whatever the enforcement and whether the instance is serving.
		if v.Gate != GateAcknowledge {
			t.Errorf("gate = %q, want acknowledge", v.Gate)
		}
	})
	t.Run("the spend could not be read", func(t *testing.T) {
		in := inputs(rtoBudget(), nil)
		in.LedgerErr = errors.New("auditd returned 503: starting up")
		in.Enforcement = infra.EnforceRefuse
		v := CompareToBudget(in)
		if v.Status != BudgetLedgerUnavailable || !strings.Contains(v.Detail, "503") || !strings.Contains(v.Detail, "43m12s") || v.Gate != GateAcknowledge {
			t.Errorf("%+v", v)
		}
		if v.RemainingSeconds != nil {
			t.Errorf("a remaining budget appeared from nothing: %v", *v.RemainingSeconds)
		}
		// A ledger that has no remaining (it had no budget to compute) is the same thing.
		in2 := inputs(rtoBudget(), &Ledger{})
		if v := CompareToBudget(in2); v.Status != BudgetLedgerUnavailable {
			t.Errorf("a ledger with no remaining = %s", v.Status)
		}
	})
}

// Outages whose real length is unknown are not counted in the verdict, but are said.
func TestCompareToBudget_SaysWhatItDoesNotCount(t *testing.T) {
	unknown := outage("unknown", 25*time.Minute, 2*time.Hour)
	unknown.Status = audit.IncidentStatusNeedsHuman
	l := ledgerSpending(rtoBudget(), 5*time.Minute, unknown)
	v := CompareToBudget(inputs(rtoBudget(), l))
	// On the confirmed spend alone there is plenty left, so this is not refused or asked for on a guess...
	if v.Status != BudgetWithin {
		t.Errorf("status = %s, want within on the confirmed spend", v.Status)
	}
	// ...but the approver is told what is not in that figure.
	if !strings.Contains(v.Detail, "1 outage(s) of unknown length are not counted") || !strings.Contains(v.Detail, "if they count in full 13m12s would be left") {
		t.Errorf("detail = %q", v.Detail)
	}
	if *v.RemainingWorstCaseSeconds != 2592-300-1500 {
		t.Errorf("worst case = %d", *v.RemainingWorstCaseSeconds)
	}
}

// ── the recovery-time estimate in the verdict ─────────────────────────────────

func estimateOf(minutes ...int) *RecoveryEstimate {
	e := EstimateRecovery(resolved(minutes...), []string{restoreSeries}, now, EstimateWindow, nil)
	return &e
}

// With history, the restore is taken to last as long as its p90, not as long as
// the rto: what has been demonstrated outranks what was declared.
func TestCompareToBudget_HistoryOutranksTheDeclaredRTO(t *testing.T) {
	// 33m12s left, and an rto of 30m: by the rto alone this fits.
	l := ledgerSpending(rtoBudget(), 10*time.Minute)
	if v := CompareToBudget(inputs(rtoBudget(), l)); v.Status != BudgetWithin || v.DurationBasis != DurationFromRTO || *v.DurationSeconds != 1800 {
		t.Fatalf("by the rto: %+v", v)
	}

	// Restores of this database have taken up to 40m at the p90: it may not.
	in := inputs(rtoBudget(), l)
	in.Estimate = estimateOf(20, 25, 30, 35, 40)
	v := CompareToBudget(in)
	if v.Status != BudgetMayExceed || v.DurationBasis != DurationFromHistory || *v.DurationSeconds != 40*60 {
		t.Fatalf("by history: %+v", v)
	}
	for _, frag := range []string{"only 33m12s left", "this database's restores have taken up to 40m at the p90 (5 past restores)", "may use more than is left"} {
		if !strings.Contains(v.Detail, frag) {
			t.Errorf("detail %q lacks %q", v.Detail, frag)
		}
	}

	// Quick restores: it fits, and says what it rests on.
	in.Estimate = estimateOf(10, 12, 14, 16, 18)
	v = CompareToBudget(in)
	if v.Status != BudgetWithin || *v.DurationSeconds != 18*60 || !strings.Contains(v.Detail, "enough for a restore as long as this database's have been (p90 18m over 5 past restores)") {
		t.Errorf("quick: %+v", v)
	}
	// And the estimate rides along on the verdict.
	if v.RecoveryEstimate == nil || v.RecoveryEstimate.Samples != 5 {
		t.Errorf("estimate not attached: %+v", v.RecoveryEstimate)
	}
}

// History makes a restore comparable even when no rto was ever declared.
func TestCompareToBudget_HistoryMakesItComparableWithoutAnRTO(t *testing.T) {
	l := ledgerSpending(noRTOBudget(), 10*time.Minute)
	if v := CompareToBudget(inputs(noRTOBudget(), l)); v.Status != BudgetNotComparable || !strings.Contains(v.Detail, "no rto is declared and there is no history") {
		t.Fatalf("without either: %+v", v)
	}
	in := inputs(noRTOBudget(), l)
	in.Estimate = estimateOf(20, 25, 30)
	v := CompareToBudget(in)
	if v.Status != BudgetWithin || v.DurationBasis != DurationFromHistory || v.RTOSeconds != nil {
		t.Errorf("with history only: %+v", v)
	}
}

// Too little history is not used, and the declared rto stands, as it did.
func TestCompareToBudget_ThinHistoryFallsBackToTheRTO(t *testing.T) {
	l := ledgerSpending(rtoBudget(), 20*time.Minute) // 23m12s left; rto 30m: may exceed
	for name, est := range map[string]*RecoveryEstimate{
		"two restores":              estimateOf(5, 6),
		"no restores":               estimateOf(),
		"a nil estimate":            nil,
		"estimated but with no p90": {Status: EstimateEstimated, Samples: 9},
	} {
		in := inputs(rtoBudget(), l)
		in.Estimate = est
		v := CompareToBudget(in)
		if v.Status != BudgetMayExceed || v.DurationBasis != DurationFromRTO || *v.DurationSeconds != 1800 || !strings.Contains(v.Detail, "may take up to the rto of 30m") {
			t.Errorf("%s: %+v", name, v)
		}
	}
	// A thin estimate is still shown beside the verdict: what there is to say.
	in := inputs(rtoBudget(), l)
	in.Estimate = estimateOf(5, 6)
	if v := CompareToBudget(in); v.RecoveryEstimate == nil || v.RecoveryEstimate.Status != EstimateTooFew {
		t.Errorf("the thin estimate was dropped: %+v", v.RecoveryEstimate)
	}
}

// A spent budget is a breach whatever the history says, and the estimate never
// changes what is refused: a may_exceed from history still only asks.
func TestCompareToBudget_HistoryDoesNotChangeWhatIsRefused(t *testing.T) {
	in := inputs(rtoBudget(), ledgerSpending(rtoBudget(), 50*time.Minute))
	in.Estimate = estimateOf(1, 1, 1) // very quick restores
	if v := CompareToBudget(in); v.Status != BudgetExceeds {
		t.Errorf("a spent budget with quick restores = %s", v.Status)
	}
	in = inputs(rtoBudget(), ledgerSpending(rtoBudget(), 10*time.Minute))
	in.Estimate = estimateOf(120, 130, 140) // restores far longer than what is left
	in.Enforcement = infra.EnforceRefuse
	v := CompareToBudget(in)
	if v.Status != BudgetMayExceed || v.Gate != GateAcknowledge {
		t.Errorf("long restores under refuse = %s, gate %q: a restore that may exceed asks, and is never refused", v.Status, v.Gate)
	}
}

// The estimate is shown whatever the verdict, even for a database with no SLO at
// all, but it only sets a duration when there is a budget to compare it with.
func TestCompareToBudget_TheEstimateRidesAlongWhateverTheVerdict(t *testing.T) {
	est := estimateOf(10, 20, 30)
	for name, in := range map[string]BudgetInputs{
		"no SLO":             {Found: true, Budget: infra.AvailabilityBudget{}, Estimate: est},
		"not a database":     {Found: false, Estimate: est},
		"unreadable SLO":     {Found: true, Invalid: true, Estimate: est},
		"spend not readable": {Found: true, Budget: rtoBudget(), Estimate: est, LedgerErr: errors.New("down")},
	} {
		v := CompareToBudget(in)
		if v.RecoveryEstimate == nil || v.RecoveryEstimate.Samples != 3 {
			t.Errorf("%s: estimate = %+v", name, v.RecoveryEstimate)
		}
		if v.DurationSeconds != nil {
			t.Errorf("%s: a duration was set with no budget to compare it with: %d", name, *v.DurationSeconds)
		}
	}
}

// The gate: what each verdict demands under each enforcement, and where.
func TestCompareToBudget_Gate(t *testing.T) {
	spent := func(d time.Duration) *Ledger { return ledgerSpending(rtoBudget(), d) }
	const refuseNote = "the instance is already down"
	tests := []struct {
		name    string
		in      func() BudgetInputs
		gate    string
		hasNote bool
	}{
		{"within, any enforcement", func() BudgetInputs {
			in := inputs(rtoBudget(), spent(5*time.Minute))
			in.Enforcement = "refuse"
			return in
		}, "", false},
		{"may_exceed, warn: shown only", func() BudgetInputs {
			in := inputs(rtoBudget(), spent(20*time.Minute))
			in.Enforcement = "warn"
			return in
		}, "", false},
		{"may_exceed, acknowledge", func() BudgetInputs {
			in := inputs(rtoBudget(), spent(20*time.Minute))
			in.Enforcement = "acknowledge"
			return in
		}, GateAcknowledge, false},
		{"may_exceed, refuse: asks, never refuses", func() BudgetInputs {
			in := inputs(rtoBudget(), spent(20*time.Minute))
			in.Enforcement = "refuse"
			return in
		}, GateAcknowledge, false},
		{"exceeds, warn: shown only", func() BudgetInputs {
			in := inputs(rtoBudget(), spent(50*time.Minute))
			in.Enforcement = "warn"
			return in
		}, "", false},
		{"exceeds, acknowledge", func() BudgetInputs {
			in := inputs(rtoBudget(), spent(50*time.Minute))
			in.Enforcement = "acknowledge"
			return in
		}, GateAcknowledge, false},
		{"exceeds, refuse, serving: refused", func() BudgetInputs {
			in := inputs(rtoBudget(), spent(50*time.Minute))
			in.Enforcement = "refuse"
			return in
		}, GateRefuse, false},
		{"exceeds, refuse, already down: asks, and says why", func() BudgetInputs {
			in := inputs(rtoBudget(), spent(50*time.Minute))
			in.Enforcement, in.Serving = "refuse", false
			return in
		}, GateAcknowledge, true},
		{"unreadable objective, warn", func() BudgetInputs { return BudgetInputs{Found: true, Invalid: true, Enforcement: "warn"} }, "", false},
		{"unreadable objective, acknowledge", func() BudgetInputs { return BudgetInputs{Found: true, Invalid: true, Enforcement: "acknowledge"} }, GateAcknowledge, false},
		{"unreadable objective, refuse", func() BudgetInputs { return BudgetInputs{Found: true, Invalid: true, Enforcement: "refuse"} }, GateAcknowledge, false},
		{"unreadable spend, warn", func() BudgetInputs { in := inputs(rtoBudget(), nil); in.Enforcement = "warn"; return in }, "", false},
		{"unreadable spend, acknowledge", func() BudgetInputs { in := inputs(rtoBudget(), nil); in.Enforcement = "acknowledge"; return in }, GateAcknowledge, false},
		{"not comparable, refuse: nothing to enforce", func() BudgetInputs {
			in := inputs(noRTOBudget(), ledgerSpending(noRTOBudget(), time.Minute))
			in.Enforcement = "refuse"
			return in
		}, "", false},
		{"no objective, refuse: nothing to enforce", func() BudgetInputs {
			return BudgetInputs{Found: true, Budget: infra.AvailabilityBudget{}, Enforcement: "refuse"}
		}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := CompareToBudget(tc.in())
			if v.Gate != tc.gate {
				t.Errorf("gate = %q, want %q (status %s)", v.Gate, tc.gate, v.Status)
			}
			if got := strings.Contains(v.GateNote, refuseNote); got != tc.hasNote {
				t.Errorf("gate note = %q, want a note about the instance being down: %v", v.GateNote, tc.hasNote)
			}
		})
	}
}

// The one thing that must hold however the pieces change: a restore is refused
// only for a budget that is certainly spent, on an instance that is serving,
// under an enforcement that says refuse. Nothing else, ever.
func TestGateFor_RefusesInExactlyOneCase(t *testing.T) {
	statuses := []string{BudgetWithin, BudgetExceeds, BudgetMayExceed, BudgetNotComparable, BudgetNoObjective, BudgetObjectiveInvalid, BudgetLedgerUnavailable, "something-new", ""}
	for _, status := range statuses {
		for _, enf := range []string{"", infra.EnforceWarn, infra.EnforceAcknowledge, infra.EnforceRefuse, "bogus"} {
			for _, serving := range []bool{true, false} {
				gate, _ := gateFor(status, enf, serving)
				wantRefuse := status == BudgetExceeds && enf == infra.EnforceRefuse && serving
				if (gate == GateRefuse) != wantRefuse {
					t.Errorf("gateFor(%q, %q, serving=%v) = %q: refuse only for a spent budget, refuse enforcement, a serving instance", status, enf, serving, gate)
				}
				// And nothing is ever demanded under warn or an unset enforcement.
				if (enf == "" || enf == infra.EnforceWarn || enf == "bogus") && gate != "" {
					t.Errorf("gateFor(%q, %q, %v) = %q, want nothing", status, enf, serving, gate)
				}
			}
		}
	}
}

func TestBudgetVerdict_NeedsAttentionAndShape(t *testing.T) {
	for status, want := range map[string]bool{
		BudgetExceeds: true, BudgetMayExceed: true, BudgetWithin: false, BudgetNotComparable: false,
		BudgetNoObjective: false, BudgetObjectiveInvalid: false, BudgetLedgerUnavailable: false,
	} {
		if got := (&BudgetVerdict{Status: status}).NeedsAttention(); got != want {
			t.Errorf("NeedsAttention(%s) = %v", status, got)
		}
	}
	if (*BudgetVerdict)(nil).NeedsAttention() {
		t.Error("a nil verdict needs attention")
	}
	// An empty verdict carries only what it has.
	b, _ := json.Marshal(&BudgetVerdict{Status: BudgetNoObjective, Detail: "d"})
	if string(b) != `{"status":"no_objective","detail":"d"}` {
		t.Errorf("json = %s", b)
	}
}
