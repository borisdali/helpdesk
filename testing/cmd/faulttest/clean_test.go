package main

import "testing"

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
		{"all five", EvalResult{EvidenceWarnings: []string{"x"}, ProtocolViolation: true, ObjectiveEvidenceGate: true, TargetDrift: true, Mismatch: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasCleanWarning(tc.er); got != tc.want {
				t.Errorf("hasCleanWarning() = %v, want %v", got, tc.want)
			}
		})
	}
}
