package main

import (
	"strings"
	"testing"
)

func TestBuildCleanReport_NoWarnings(t *testing.T) {
	f := Failure{ID: "db-lock-contention", Name: "Lock contention"}
	results := []EvalResult{
		{Passed: true},
		{Passed: true},
		{Passed: true},
	}
	r := buildCleanReport(f, results)

	if r.N != 3 {
		t.Errorf("N: got %d, want 3", r.N)
	}
	if r.WarningCount != 0 {
		t.Errorf("WarningCount: got %d, want 0", r.WarningCount)
	}
	if !r.isClean() {
		t.Error("should be clean: zero warnings across all runs")
	}
}

func TestBuildCleanReport_SomeWarnings(t *testing.T) {
	f := Failure{ID: "k8s-oomkilled", Name: "OOMKilled"}
	results := []EvalResult{
		{Passed: true},
		{Passed: true, ProtocolViolation: true},
		{Passed: true, EvidenceWarnings: []string{"hop x recorded evidence but did not escalate"}},
		{Passed: true, ObjectiveEvidenceGate: true},
		{Passed: true, TargetDrift: true},
		{Passed: true, Mismatch: true},
		{Passed: true},
	}
	r := buildCleanReport(f, results)

	if r.N != 7 {
		t.Errorf("N: got %d, want 7", r.N)
	}
	if r.WarningCount != 5 {
		t.Errorf("WarningCount: got %d, want 5 (protocol violation, evidence warning, objective evidence gate, target drift, mismatch)", r.WarningCount)
	}
	if r.isClean() {
		t.Error("should not be clean: 5/7 runs tripped a warning signal")
	}
	// EvidenceWarnings and ObjectiveEvidenceGate are two manifestations of the
	// same underlying signal — both counted under "objective_evidence".
	if r.WarningDistribution["objective_evidence"] != 2 {
		t.Errorf("WarningDistribution[objective_evidence]: got %d, want 2", r.WarningDistribution["objective_evidence"])
	}
	if r.WarningDistribution["protocol_violation"] != 1 {
		t.Errorf("WarningDistribution[protocol_violation]: got %d, want 1", r.WarningDistribution["protocol_violation"])
	}
	if r.WarningDistribution["target_drift"] != 1 {
		t.Errorf("WarningDistribution[target_drift]: got %d, want 1", r.WarningDistribution["target_drift"])
	}
	if r.WarningDistribution["mismatch"] != 1 {
		t.Errorf("WarningDistribution[mismatch]: got %d, want 1", r.WarningDistribution["mismatch"])
	}
}

// TestCleanReport_Print_ShowsPredictableVsVariesAnnotation exercises the
// actual printed stdout of faulttest run --repeat N's inline stability
// report — the third of three render paths for warningDistributionString
// (the other two: the function directly via TestWarningDistributionString,
// and vault accuracy's persisted-cert display via
// TestPrintFaultStabilityCert_ShowsWarningTypesLine). Only the underlying
// WarningDistribution map was checked by TestBuildCleanReport_SomeWarnings;
// this confirms the annotation actually reaches the operator's terminal on
// this path too, not just the other two.
func TestCleanReport_Print_ShowsPredictableVsVariesAnnotation(t *testing.T) {
	f := Failure{ID: "custom-k8s-oomkill-signal", Name: "OOMKilled"}
	results := []EvalResult{
		{Passed: true, ObjectiveEvidenceGate: true, ObjectiveEvidenceSignals: []string{"oom_killed"}},
		{Passed: true, ObjectiveEvidenceGate: true, ObjectiveEvidenceSignals: []string{"oom_killed"}},
		{Passed: true, ObjectiveEvidenceGate: true, ObjectiveEvidenceSignals: []string{"oom_killed"}, Mismatch: true},
		{Passed: true, ObjectiveEvidenceGate: true, ObjectiveEvidenceSignals: []string{"oom_killed"}},
		{Passed: true, ObjectiveEvidenceGate: true, ObjectiveEvidenceSignals: []string{"oom_killed"}},
	}
	r := buildCleanReport(f, results)

	out := captureStdout(func() { r.Print() })

	if !strings.Contains(out, "objective_evidence:oom_killed=5(predictable)") {
		t.Errorf("expected predictable annotation on a signal that fired every run:\n%s", out)
	}
	if !strings.Contains(out, "mismatch=1(varies)") {
		t.Errorf("expected varies annotation on a signal that fired only some runs:\n%s", out)
	}
}

func TestBuildCleanReport_ZeroTolerance(t *testing.T) {
	// Even a single warning in a large batch must fail isClean — no percentage
	// threshold, unlike stabilityPassThreshold/stabilityConfThreshold.
	f := Failure{ID: "db-lock-contention"}
	results := make([]EvalResult, 20)
	for i := range results {
		results[i] = EvalResult{Passed: true}
	}
	results[19].ProtocolViolation = true

	r := buildCleanReport(f, results)
	if r.WarningCount != 1 {
		t.Errorf("WarningCount: got %d, want 1", r.WarningCount)
	}
	if r.isClean() {
		t.Error("should not be clean: even 1/20 warnings must fail zero-tolerance isClean")
	}
}

func TestBuildCleanReport_EmptyResults(t *testing.T) {
	f := Failure{ID: "db-lock-contention"}
	r := buildCleanReport(f, nil)
	if r.N != 0 {
		t.Errorf("N: got %d, want 0", r.N)
	}
	if !r.isClean() {
		t.Error("zero runs should be vacuously clean (WarningCount == 0)")
	}
}

func TestWarningTypesFor(t *testing.T) {
	cases := []struct {
		name string
		er   EvalResult
		want []string
	}{
		{"no signals", EvalResult{Passed: true}, nil},
		{"evidence warnings present", EvalResult{EvidenceWarnings: []string{"x"}}, []string{"objective_evidence"}},
		{"objective evidence gate", EvalResult{ObjectiveEvidenceGate: true}, []string{"objective_evidence"}},
		{"both evidence manifestations — still one type", EvalResult{EvidenceWarnings: []string{"x"}, ObjectiveEvidenceGate: true}, []string{"objective_evidence"}},
		{"protocol violation", EvalResult{ProtocolViolation: true}, []string{"protocol_violation"}},
		{"both types on one run", EvalResult{EvidenceWarnings: []string{"x"}, ProtocolViolation: true}, []string{"objective_evidence", "protocol_violation"}},
		{"target drift", EvalResult{TargetDrift: true}, []string{"target_drift"}},
		{"all three types on one run", EvalResult{EvidenceWarnings: []string{"x"}, ProtocolViolation: true, TargetDrift: true}, []string{"objective_evidence", "protocol_violation", "target_drift"}},
		{"signals present — keyed by signal, not flat bucket", EvalResult{EvidenceWarnings: []string{"x"}, ObjectiveEvidenceSignals: []string{"pod_restarted"}}, []string{"objective_evidence:pod_restarted"}},
		{"multiple signals on one run", EvalResult{ObjectiveEvidenceGate: true, ObjectiveEvidenceSignals: []string{"pod_restarted", "oom_killed"}}, []string{"objective_evidence:pod_restarted", "objective_evidence:oom_killed"}},
		{"gate fired but signals empty — falls back to flat bucket", EvalResult{ObjectiveEvidenceGate: true}, []string{"objective_evidence"}},
		{"mismatch", EvalResult{Mismatch: true}, []string{"mismatch"}},
		{"all five types on one run", EvalResult{EvidenceWarnings: []string{"x"}, ProtocolViolation: true, TargetDrift: true, Mismatch: true}, []string{"objective_evidence", "protocol_violation", "target_drift", "mismatch"}},
		{"catalog evidence coverage gap", EvalResult{EvidenceCoverageGap: true}, []string{"evidence_coverage_gap"}},
		{"catalog evidence unconfirmed", EvalResult{EvidenceRequiredButUnconfirmed: true}, []string{"evidence_unconfirmed"}},
		{"unverified evidence (content-provenance)", EvalResult{UnverifiedEvidence: true}, []string{"unverified_evidence"}},
		{"unverified evidence secondary (non-primary hypothesis)", EvalResult{UnverifiedEvidenceSecondary: true}, []string{"unverified_evidence_secondary"}},
		{
			"primary and secondary are distinct buckets, not the same one",
			EvalResult{UnverifiedEvidence: true, UnverifiedEvidenceSecondary: true},
			[]string{"unverified_evidence", "unverified_evidence_secondary"},
		},
		{
			"coverage gap and unconfirmed are distinct buckets, not the same one",
			EvalResult{EvidenceCoverageGap: true, EvidenceRequiredButUnconfirmed: true},
			[]string{"evidence_coverage_gap", "evidence_unconfirmed"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := warningTypesFor(tc.er)
			if len(got) != len(tc.want) {
				t.Fatalf("warningTypesFor() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("warningTypesFor()[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestConfirmedTypesFor(t *testing.T) {
	cases := []struct {
		name string
		er   EvalResult
		want []string
	}{
		{"no confirmed signals", EvalResult{Passed: true}, nil},
		{"one confirmed signal", EvalResult{ObjectiveEvidenceConfirmed: []string{"oom_killed"}}, []string{"objective_evidence:oom_killed"}},
		{
			"multiple confirmed signals on one run",
			EvalResult{ObjectiveEvidenceConfirmed: []string{"oom_killed", "replica_disconnected"}},
			[]string{"objective_evidence:oom_killed", "objective_evidence:replica_disconnected"},
		},
		{
			"unconfirmed-only signals don't appear here — that's warningTypesFor's job",
			EvalResult{ObjectiveEvidenceSignals: []string{"pod_restarted"}, ObjectiveEvidenceGate: true},
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := confirmedTypesFor(tc.er)
			if len(got) != len(tc.want) {
				t.Fatalf("confirmedTypesFor() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("confirmedTypesFor()[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestBuildCleanReport_ConfirmedAndUnconfirmed_Independent verifies a run
// with BOTH a confirmed and an unconfirmed objective-evidence signal
// populates both distributions correctly and independently — WarningCount
// (zero-tolerance) must only count the unconfirmed one, not double-count or
// get confused by the confirmed one sitting alongside it.
func TestBuildCleanReport_ConfirmedAndUnconfirmed_Independent(t *testing.T) {
	f := Failure{ID: "db-replica-disconnected", Name: "Replica disconnected"}
	results := []EvalResult{
		{Passed: true, ObjectiveEvidenceConfirmed: []string{"replica_disconnected"}},
		{Passed: true, ObjectiveEvidenceGate: true, ObjectiveEvidenceSignals: []string{"idle_in_transaction_stuck"}},
		{Passed: true, ObjectiveEvidenceGate: true, ObjectiveEvidenceSignals: []string{"idle_in_transaction_stuck"}, ObjectiveEvidenceConfirmed: []string{"replica_disconnected"}},
	}
	r := buildCleanReport(f, results)

	if r.WarningCount != 2 {
		t.Errorf("WarningCount: got %d, want 2 (runs 2 and 3 carry an unconfirmed signal)", r.WarningCount)
	}
	if r.WarningDistribution["objective_evidence:idle_in_transaction_stuck"] != 2 {
		t.Errorf("WarningDistribution: got %v, want idle_in_transaction_stuck=2", r.WarningDistribution)
	}
	if r.ConfirmedDistribution["objective_evidence:replica_disconnected"] != 2 {
		t.Errorf("ConfirmedDistribution: got %v, want replica_disconnected=2 (runs 1 and 3)", r.ConfirmedDistribution)
	}
	if _, unconfirmedHasConfirmedSignal := r.WarningDistribution["objective_evidence:replica_disconnected"]; unconfirmedHasConfirmedSignal {
		t.Error("WarningDistribution should not contain replica_disconnected — it was only ever confirmed, never unconfirmed")
	}
}

// TestBuildCleanReport_CoverageGapVsUnconfirmed_SideBySide verifies the two
// evidence-veto failure buckets James asked to see separated — "did the
// signal never fire at all" vs. "it fired but was never confirmed" — show up
// as distinct, independently-counted lines in the printed --repeat N report,
// not collapsed into one flat "evidence" bucket a reader would have to go
// dig through raw JSON to tell apart.
func TestBuildCleanReport_CoverageGapVsUnconfirmed_SideBySide(t *testing.T) {
	f := Failure{ID: "db-replica-disconnected", Name: "Replica disconnected"}
	results := []EvalResult{
		{Passed: false, EvidenceCoverageGap: true},
		{Passed: false, EvidenceCoverageGap: true},
		{Passed: false, EvidenceRequiredButUnconfirmed: true},
		{Passed: true},
		{Passed: true},
	}
	r := buildCleanReport(f, results)

	if r.WarningCount != 3 {
		t.Errorf("WarningCount: got %d, want 3 (2 coverage gaps + 1 unconfirmed)", r.WarningCount)
	}
	if r.WarningDistribution["evidence_coverage_gap"] != 2 {
		t.Errorf("WarningDistribution[evidence_coverage_gap]: got %d, want 2", r.WarningDistribution["evidence_coverage_gap"])
	}
	if r.WarningDistribution["evidence_unconfirmed"] != 1 {
		t.Errorf("WarningDistribution[evidence_unconfirmed]: got %d, want 1", r.WarningDistribution["evidence_unconfirmed"])
	}

	out := captureStdout(func() { r.Print() })
	if !strings.Contains(out, "evidence_coverage_gap=2(varies)") {
		t.Errorf("expected the coverage-gap count as its own line:\n%s", out)
	}
	if !strings.Contains(out, "evidence_unconfirmed=1(varies)") {
		t.Errorf("expected the unconfirmed count as its own, separate line:\n%s", out)
	}
}

// TestBuildCleanReport_UnverifiedEvidenceSecondary_DoesNotBlockClean
// reproduces the exact live scenario from 2026-09-07: a STABLE, fully-passing,
// correctly-attributed diagnosis where every run's only warning is a
// fabricated quote on a REJECTED hypothesis (db-replica-container-stopped's
// "due to timeout" vs the real "due to administrator command"). Before the
// primary/secondary split, this made the fault permanently DIRTY despite the
// acted-on conclusion being entirely sound. Confirms: secondary is still
// tracked (WarningDistribution/Signal types) but Clean is "yes".
func TestBuildCleanReport_UnverifiedEvidenceSecondary_DoesNotBlockClean(t *testing.T) {
	f := Failure{ID: "db-replica-container-stopped", Name: "Replica container stopped"}
	results := []EvalResult{
		{Passed: true, UnverifiedEvidenceSecondary: true},
		{Passed: true, UnverifiedEvidenceSecondary: true},
		{Passed: true, UnverifiedEvidenceSecondary: true},
	}
	r := buildCleanReport(f, results)

	if r.WarningCount != 0 {
		t.Errorf("WarningCount: got %d, want 0 — secondary-only warnings must not count toward the zero-tolerance CLEAN gate", r.WarningCount)
	}
	if !r.isClean() {
		t.Error("isClean() = false, want true — a correctly-attributed, fully-passing diagnosis should earn CLEAN even when a rejected hypothesis cited an invented detail")
	}
	if r.WarningDistribution["unverified_evidence_secondary"] != 3 {
		t.Errorf("WarningDistribution[unverified_evidence_secondary]: got %d, want 3 — still tracked for visibility", r.WarningDistribution["unverified_evidence_secondary"])
	}

	out := captureStdout(func() { r.Print() })
	if !strings.Contains(out, "unverified_evidence_secondary=3") {
		t.Errorf("expected the secondary count surfaced in the printed report:\n%s", out)
	}
	if !strings.Contains(out, "Clean:        yes") {
		t.Errorf("expected Clean: yes in the printed report:\n%s", out)
	}
}

// TestBuildCleanReport_UnverifiedEvidencePrimary_DoesNotBlockClean reproduces
// the live 2026-09-08 db-replica-disconnected/db-replica-container-stopped
// pattern that drove this decision: after four live rounds each surfacing a
// new, genuine citation-formatting variant (compound quotes, backslash
// escaping, narration, separator punctuation, dropped embedded quotes) — all
// real false positives, none a missed fabrication — plus a structural
// truncation-direction bug unrelated to citation style, unverified_evidence
// (primary) moved to warn-only for this release, matching
// UnverifiedEvidenceSecondary's existing treatment: still tracked
// (WarningDistribution), no longer CLEAN-blocking on its own. See
// hasCleanWarning's doc comment for the full rationale.
func TestBuildCleanReport_UnverifiedEvidencePrimary_DoesNotBlockClean(t *testing.T) {
	f := Failure{ID: "db-replica-disconnected", Name: "Replica disconnected (walreceiver dropped)"}
	results := []EvalResult{
		{Passed: true, UnverifiedEvidence: true},
		{Passed: true, UnverifiedEvidence: true},
		{Passed: true, UnverifiedEvidence: true},
	}
	r := buildCleanReport(f, results)

	if r.WarningCount != 0 {
		t.Errorf("WarningCount: got %d, want 0 — unverified_evidence is warn-only as of 2026-09-08, must not count toward CLEAN", r.WarningCount)
	}
	if !r.isClean() {
		t.Error("isClean() = false, want true — a fully-passing, correctly-attributed diagnosis should earn CLEAN even with unverified_evidence firing on every run")
	}
	if r.WarningDistribution["unverified_evidence"] != 3 {
		t.Errorf("WarningDistribution[unverified_evidence]: got %d, want 3 — still tracked for visibility", r.WarningDistribution["unverified_evidence"])
	}

	out := captureStdout(func() { r.Print() })
	if !strings.Contains(out, "unverified_evidence=3") {
		t.Errorf("expected the unverified_evidence count surfaced in the printed report:\n%s", out)
	}
	if !strings.Contains(out, "Clean:        yes") {
		t.Errorf("expected Clean: yes in the printed report:\n%s", out)
	}
}

func TestWarningDistributionString(t *testing.T) {
	cases := []struct {
		name string
		dist map[string]int
		n    int
		want string
	}{
		{"empty", nil, 5, ""},
		{"n<=0 skips annotation entirely", map[string]int{"protocol_violation": 1}, 0, "protocol_violation=1"},
		{"fires every run — predictable", map[string]int{"protocol_violation": 5}, 5, "protocol_violation=5(predictable)"},
		{"fires some but not all runs — varies", map[string]int{"protocol_violation": 2}, 5, "protocol_violation=2(varies)"},
		{
			"sorted regardless of map iteration order, mixed predictable/varies",
			map[string]int{"protocol_violation": 5, "objective_evidence": 2},
			5,
			"objective_evidence=2(varies), protocol_violation=5(predictable)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := warningDistributionString(tc.dist, tc.n); got != tc.want {
				t.Errorf("warningDistributionString() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHasCleanWarning(t *testing.T) {
	cases := []struct {
		name string
		er   EvalResult
		want bool
	}{
		{"no signals", EvalResult{Passed: true}, false},
		{"evidence warnings present", EvalResult{EvidenceWarnings: []string{"x"}}, true},
		{"protocol violation", EvalResult{ProtocolViolation: true}, true},
		{"objective evidence gate", EvalResult{ObjectiveEvidenceGate: true}, true},
		{"target drift", EvalResult{TargetDrift: true}, true},
		{"mismatch", EvalResult{Mismatch: true}, true},
		{"evidence coverage gap", EvalResult{EvidenceCoverageGap: true}, true},
		{"evidence required but unconfirmed", EvalResult{EvidenceRequiredButUnconfirmed: true}, true},
		{
			"unverified evidence (content-provenance) alone does NOT block Clean — warn-only as of 2026-09-08, see hasCleanWarning's doc comment",
			EvalResult{UnverifiedEvidence: true}, false,
		},
		{
			"unverified evidence secondary alone does NOT block Clean — backs a hypothesis the model itself rejected, not the acted-on conclusion",
			EvalResult{UnverifiedEvidenceSecondary: true}, false,
		},
		{
			"all seven blocking signals", EvalResult{
				EvidenceWarnings: []string{"x"}, ProtocolViolation: true, ObjectiveEvidenceGate: true, TargetDrift: true,
				Mismatch: true, EvidenceCoverageGap: true, EvidenceRequiredButUnconfirmed: true,
			}, true,
		},
		{
			"unverified evidence alongside real blocking signals still blocks (via the other signals, not itself)",
			EvalResult{EvidenceCoverageGap: true, UnverifiedEvidence: true}, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasCleanWarning(tc.er); got != tc.want {
				t.Errorf("hasCleanWarning() = %v, want %v", got, tc.want)
			}
		})
	}
}
