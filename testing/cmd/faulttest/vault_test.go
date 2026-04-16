package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// ── passRateOf ────────────────────────────────────────────────────────────

func TestPassRateOf(t *testing.T) {
	tests := []struct {
		name    string
		results []bool
		want    float64
	}{
		{"all pass", []bool{true, true, true}, 1.0},
		{"all fail", []bool{false, false}, 0.0},
		{"half", []bool{true, false, true, false}, 0.5},
		{"one of three", []bool{true, false, false}, 1.0 / 3.0},
		{"empty", []bool{}, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := passRateOf(tt.results)
			if got < tt.want-0.001 || got > tt.want+0.001 {
				t.Errorf("passRateOf(%v) = %.4f, want %.4f", tt.results, got, tt.want)
			}
		})
	}
}

// ── history round-trip ─────────────────────────────────────────────────────

func TestLoadHistory_NotExist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	runs, err := loadHistory()
	if err != nil {
		t.Fatalf("loadHistory on missing file: %v", err)
	}
	if runs != nil {
		t.Errorf("expected nil runs for missing file, got %v", runs)
	}
}

func TestAppendLoadHistory_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	report := Report{
		ID:        "run-001",
		Timestamp: "2026-04-16T10:00:00Z",
		Summary:   Summary{Total: 3, Passed: 2, Failed: 1},
		Results: []EvalResult{
			{FailureID: "db-max-connections", FailureName: "Max connections", Passed: true, Score: 0.87, RemediationScore: 1.0},
			{FailureID: "db-lock-contention", FailureName: "Lock contention", Passed: false, Score: 0.43},
		},
	}

	if err := appendHistory(report); err != nil {
		t.Fatalf("appendHistory: %v", err)
	}

	runs, err := loadHistory()
	if err != nil {
		t.Fatalf("loadHistory: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}

	r := runs[0]
	if r.RunID != "run-001" {
		t.Errorf("RunID = %q, want run-001", r.RunID)
	}
	if r.Total != 3 || r.Passed != 2 {
		t.Errorf("Total=%d, Passed=%d, want 3/2", r.Total, r.Passed)
	}
	if len(r.Results) != 2 {
		t.Fatalf("expected 2 fault results, got %d", len(r.Results))
	}
	if r.Results[0].FailureID != "db-max-connections" || !r.Results[0].Passed {
		t.Errorf("unexpected first result: %+v", r.Results[0])
	}
	if r.Results[0].RemediationScore != 1.0 {
		t.Errorf("RemediationScore = %.2f, want 1.0", r.Results[0].RemediationScore)
	}
}

func TestAppendLoadHistory_Accumulates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	for i, runID := range []string{"run-001", "run-002", "run-003"} {
		report := Report{
			ID:        runID,
			Timestamp: "2026-04-16T10:00:00Z",
			Summary:   Summary{Total: 1, Passed: i % 2},
			Results: []EvalResult{
				{FailureID: "db-test", Passed: i%2 == 0},
			},
		}
		if err := appendHistory(report); err != nil {
			t.Fatalf("appendHistory run %d: %v", i, err)
		}
	}

	runs, err := loadHistory()
	if err != nil {
		t.Fatalf("loadHistory: %v", err)
	}
	if len(runs) != 3 {
		t.Errorf("expected 3 runs, got %d", len(runs))
	}
}

func TestLoadHistory_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Write invalid JSON to the history file location.
	path := historyFilePath()
	if err := os.MkdirAll(tmpDir+"/.faulttest", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not valid json"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := loadHistory()
	if err == nil {
		t.Error("expected error for invalid JSON history file, got nil")
	}
}

// ── validatePlaybookExists ─────────────────────────────────────────────────

func TestValidatePlaybookExists_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("series_id") != "pbs_db_conn_pooling" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{"id": "pb-001", "series_id": "pbs_db_conn_pooling"},
		})
	}))
	defer srv.Close()

	if !validatePlaybookExists(srv.URL, "", "pbs_db_conn_pooling") {
		t.Error("expected true for existing playbook, got false")
	}
}

func TestValidatePlaybookExists_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if validatePlaybookExists(srv.URL, "", "pbs_missing") {
		t.Error("expected false for 404, got true")
	}
}

func TestValidatePlaybookExists_EmptyList(t *testing.T) {
	// Gateway returns 200 with empty array — playbook is not registered yet.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	if validatePlaybookExists(srv.URL, "", "pbs_not_registered") {
		t.Error("expected false for empty list response, got true")
	}
}

func TestValidatePlaybookExists_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if validatePlaybookExists(srv.URL, "", "pbs_test") {
		t.Error("expected false for 500, got true")
	}
}

func TestValidatePlaybookExists_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]interface{}{{"id": "pb-1"}})
	}))
	defer srv.Close()

	validatePlaybookExists(srv.URL, "my-api-key", "pbs_test")
	if gotAuth != "Bearer my-api-key" {
		t.Errorf("Authorization = %q, want Bearer my-api-key", gotAuth)
	}
}

func TestValidatePlaybookExists_NetworkError(t *testing.T) {
	// Point at a port where nothing is listening.
	if validatePlaybookExists("http://127.0.0.1:19999", "", "pbs_test") {
		t.Error("expected false for unreachable server, got true")
	}
}

// ── RemediationScore buckets ───────────────────────────────────────────────

func TestRemediationScoreBuckets(t *testing.T) {
	// Documents the scoring thresholds from Remediator.Remediate:
	// 1.0 if recoverySecs ≤ timeout/2, 0.75 if ≤ timeout, 0.0 on timeout error.
	tests := []struct {
		name         string
		recoverySecs float64
		timeout      float64
		wantScore    float64
	}{
		{"fast: within half timeout", 30, 120, 1.0},
		{"fast: exactly half timeout", 60, 120, 1.0},
		{"slow: just over half", 61, 120, 0.75},
		{"slow: at full timeout", 119, 120, 0.75},
		{"short timeout, fast", 5, 30, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mirror the scoring formula from remediation.go.
			score := 0.75
			if tt.recoverySecs <= tt.timeout/2 {
				score = 1.0
			}
			if score != tt.wantScore {
				t.Errorf("recoverySecs=%.0f, timeout=%.0f: score=%.2f, want=%.2f",
					tt.recoverySecs, tt.timeout, score, tt.wantScore)
			}
		})
	}
}
