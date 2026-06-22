package main

import (
	"testing"
)

func TestBuildStabilityReport_AllPass(t *testing.T) {
	f := Failure{ID: "db-lock-contention", Name: "Lock contention"}
	results := []EvalResult{
		{Passed: true, PrimaryConfidence: 0.95},
		{Passed: true, PrimaryConfidence: 0.92},
		{Passed: true, PrimaryConfidence: 0.94},
	}
	r := buildStabilityReport(f, results)

	if r.PassCount != 3 {
		t.Errorf("PassCount: got %d, want 3", r.PassCount)
	}
	if r.failCount() != 0 {
		t.Errorf("failCount: got %d, want 0", r.failCount())
	}
	if r.passRate() != 1.0 {
		t.Errorf("passRate: got %v, want 1.0", r.passRate())
	}
	if !r.hasConf {
		t.Error("hasConf should be true")
	}
	if r.ConfMin < 0.92-0.001 || r.ConfMin > 0.92+0.001 {
		t.Errorf("ConfMin: got %v, want 0.92", r.ConfMin)
	}
	if r.ConfMax < 0.95-0.001 || r.ConfMax > 0.95+0.001 {
		t.Errorf("ConfMax: got %v, want 0.95", r.ConfMax)
	}
	if !r.isStable() {
		t.Error("should be STABLE: 100% pass rate, 3pp conf range")
	}
}

func TestBuildStabilityReport_LowPassRate(t *testing.T) {
	f := Failure{ID: "db-lock-contention", Name: "Lock contention"}
	results := []EvalResult{
		{Passed: true, PrimaryConfidence: 0.90},
		{Passed: false, PrimaryConfidence: 0.80},
		{Passed: false, PrimaryConfidence: 0.85},
	}
	r := buildStabilityReport(f, results)

	if r.passRate() > stabilityPassThreshold {
		t.Errorf("passRate %v should be below threshold %v", r.passRate(), stabilityPassThreshold)
	}
	if r.isStable() {
		t.Error("should be UNSTABLE: 33% pass rate")
	}
}

func TestBuildStabilityReport_FailedRunsExcludedFromConf(t *testing.T) {
	f := Failure{ID: "db-lock-contention", Name: "Lock contention"}
	results := []EvalResult{
		{Passed: true, PrimaryConfidence: 0.95},  // counted
		{Passed: true, PrimaryConfidence: 0.92},  // counted
		{Passed: false, PrimaryConfidence: 0.10}, // excluded — wrong answer at high conf would corrupt range
	}
	r := buildStabilityReport(f, results)

	if r.PassCount != 2 {
		t.Errorf("PassCount: got %d, want 2", r.PassCount)
	}
	if r.failCount() != 1 {
		t.Errorf("failCount: got %d, want 1", r.failCount())
	}
	// Confidence stats should only reflect the two passing runs (0.92–0.95).
	if r.ConfMin < 0.92-0.001 || r.ConfMin > 0.92+0.001 {
		t.Errorf("ConfMin: got %v, want 0.92 (failed run's 0.10 should be excluded)", r.ConfMin)
	}
	if r.ConfMax < 0.95-0.001 || r.ConfMax > 0.95+0.001 {
		t.Errorf("ConfMax: got %v, want 0.95", r.ConfMax)
	}
}

func TestBuildStabilityReport_NoConfidence(t *testing.T) {
	f := Failure{ID: "db-lock-contention", Name: "Lock contention"}
	results := []EvalResult{
		{Passed: true, PrimaryConfidence: 0}, // agent didn't emit confidence
		{Passed: true, PrimaryConfidence: 0},
	}
	r := buildStabilityReport(f, results)

	if r.hasConf {
		t.Error("hasConf should be false when no run emitted confidence")
	}
	// No conf data → conf range doesn't disqualify stability.
	if !r.isStable() {
		t.Error("should be STABLE: 100% pass rate and no conf data to penalise")
	}
}

func TestBuildStabilityReport_HighConfRange_Unstable(t *testing.T) {
	f := Failure{ID: "db-lock-contention", Name: "Lock contention"}
	results := []EvalResult{
		{Passed: true, PrimaryConfidence: 0.99},
		{Passed: true, PrimaryConfidence: 0.99},
		{Passed: true, PrimaryConfidence: 0.99},
		{Passed: true, PrimaryConfidence: 0.60}, // 39pp range → UNSTABLE
	}
	r := buildStabilityReport(f, results)

	if r.passRate() < stabilityPassThreshold {
		t.Errorf("passRate %v is below threshold — test setup is wrong", r.passRate())
	}
	if r.confRange() <= stabilityConfThreshold {
		t.Errorf("confRange %v should exceed %v", r.confRange(), stabilityConfThreshold)
	}
	if r.isStable() {
		t.Error("should be UNSTABLE: conf range > 30pp")
	}
}

func TestBuildStabilityReport_ProtocolViolations(t *testing.T) {
	f := Failure{ID: "db-lock-contention", Name: "Lock contention"}
	results := []EvalResult{
		{Passed: true, PrimaryConfidence: 0.90, ProtocolViolation: false},
		{Passed: false, PrimaryConfidence: 0.85, ProtocolViolation: true},
		{Passed: false, PrimaryConfidence: 0.80, ProtocolViolation: true},
	}
	r := buildStabilityReport(f, results)

	if r.ProtocolViolations != 2 {
		t.Errorf("ProtocolViolations: got %d, want 2", r.ProtocolViolations)
	}
}

func TestBuildStabilityReport_ConfMean(t *testing.T) {
	f := Failure{ID: "db-lock-contention", Name: "Lock contention"}
	results := []EvalResult{
		{Passed: true, PrimaryConfidence: 0.90},
		{Passed: true, PrimaryConfidence: 1.00},
	}
	r := buildStabilityReport(f, results)

	wantMean := 0.95
	if r.ConfMean < wantMean-0.001 || r.ConfMean > wantMean+0.001 {
		t.Errorf("ConfMean: got %v, want %v", r.ConfMean, wantMean)
	}
}

func TestBuildStabilityReport_EmptyResults(t *testing.T) {
	f := Failure{ID: "db-lock-contention", Name: "Lock contention"}
	r := buildStabilityReport(f, nil)

	if r.N != 0 {
		t.Errorf("N: got %d, want 0", r.N)
	}
	if r.passRate() != 0 {
		t.Errorf("passRate on empty: got %v", r.passRate())
	}
	if r.isStable() {
		t.Error("empty report should not be STABLE")
	}
}
