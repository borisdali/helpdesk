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
		{Passed: true},
	}
	r := buildCleanReport(f, results)

	if r.N != 5 {
		t.Errorf("N: got %d, want 5", r.N)
	}
	if r.WarningCount != 3 {
		t.Errorf("WarningCount: got %d, want 3 (protocol violation, evidence warning, objective evidence gate)", r.WarningCount)
	}
	if r.isClean() {
		t.Error("should not be clean: 3/5 runs tripped a warning signal")
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
		{"all three", EvalResult{EvidenceWarnings: []string{"x"}, ProtocolViolation: true, ObjectiveEvidenceGate: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasCleanWarning(tc.er); got != tc.want {
				t.Errorf("hasCleanWarning() = %v, want %v", got, tc.want)
			}
		})
	}
}
