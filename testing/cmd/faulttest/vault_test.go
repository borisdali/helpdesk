package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
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

	if err := appendHistory(report, "alloydb-on-vm"); err != nil {
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
	if r.Target != "alloydb-on-vm" {
		t.Errorf("Target = %q, want alloydb-on-vm", r.Target)
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
		if err := appendHistory(report, "staging-db"); err != nil {
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

func TestAppendLoadHistory_TargetPersistedAndDistinct(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	targets := []string{"staging-db", "canary-db", "staging-db"}
	for i, target := range targets {
		report := Report{
			ID:        fmt.Sprintf("run-%03d", i),
			Timestamp: "2026-04-16T10:00:00Z",
			Summary:   Summary{Total: 1, Passed: 1},
			Results:   []EvalResult{{FailureID: "db-test", Passed: true}},
		}
		if err := appendHistory(report, target); err != nil {
			t.Fatalf("appendHistory: %v", err)
		}
	}

	runs, err := loadHistory()
	if err != nil {
		t.Fatalf("loadHistory: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runs))
	}

	stagingCount, canaryCount := 0, 0
	for _, r := range runs {
		switch r.Target {
		case "staging-db":
			stagingCount++
		case "canary-db":
			canaryCount++
		}
	}
	if stagingCount != 2 {
		t.Errorf("staging-db runs = %d, want 2", stagingCount)
	}
	if canaryCount != 1 {
		t.Errorf("canary-db runs = %d, want 1", canaryCount)
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
		json.NewEncoder(w).Encode(map[string]interface{}{
			"playbooks": []map[string]interface{}{
				{"playbook_id": "pb-001", "series_id": "pbs_db_conn_pooling"},
			},
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
	// Gateway returns 200 with empty playbooks array — playbook is not registered yet.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"playbooks":[]}`))
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
		json.NewEncoder(w).Encode(map[string]interface{}{
			"playbooks": []map[string]interface{}{{"playbook_id": "pb-1"}},
		})
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

// ── fetchActivePlaybook ───────────────────────────────────────────────────

func TestFetchActivePlaybook_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("series_id") != "pbs_db_conn_pooling" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
			"playbooks": []map[string]interface{}{
				{
					"playbook_id": "pb_001",
					"name":        "Connection Pooling",
					"description": "Add PgBouncer",
					"guidance":    "Check max_connections first",
				},
			},
		})
	}))
	defer srv.Close()

	pb, err := fetchActivePlaybook(srv.URL, "", "pbs_db_conn_pooling")
	if err != nil {
		t.Fatalf("fetchActivePlaybook: %v", err)
	}
	if pb.PlaybookID != "pb_001" {
		t.Errorf("playbook_id = %q, want pb_001", pb.PlaybookID)
	}
	if pb.Name != "Connection Pooling" {
		t.Errorf("name = %q, want Connection Pooling", pb.Name)
	}
	if pb.Guidance != "Check max_connections first" {
		t.Errorf("guidance = %q, want Check max_connections first", pb.Guidance)
	}
}

func TestFetchActivePlaybook_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"playbooks": []interface{}{}}) //nolint:errcheck
	}))
	defer srv.Close()

	_, err := fetchActivePlaybook(srv.URL, "", "pbs_missing")
	if err == nil {
		t.Error("expected error for empty playbooks list, got nil")
	}
}

func TestFetchActivePlaybook_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := fetchActivePlaybook(srv.URL, "", "pbs_test")
	if err == nil {
		t.Error("expected error for 500 status, got nil")
	}
}

func TestFetchActivePlaybook_NetworkError(t *testing.T) {
	_, err := fetchActivePlaybook("http://127.0.0.1:19999", "", "pbs_test")
	if err == nil {
		t.Error("expected error for unreachable server, got nil")
	}
}

func TestFetchActivePlaybook_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
			"playbooks": []map[string]interface{}{{"playbook_id": "pb_x"}},
		})
	}))
	defer srv.Close()

	fetchActivePlaybook(srv.URL, "my-secret", "pbs_test") //nolint:errcheck
	if gotAuth != "Bearer my-secret" {
		t.Errorf("Authorization = %q, want Bearer my-secret", gotAuth)
	}
}

func TestFetchActivePlaybook_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json")) //nolint:errcheck
	}))
	defer srv.Close()

	_, err := fetchActivePlaybook(srv.URL, "", "pbs_test")
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

// ── fetchPlaybookInfo ─────────────────────────────────────────────────────

func TestFetchPlaybookInfo_DecodesBreakdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"playbooks": []map[string]any{
				{
					"source": "system",
					"stats": map[string]any{
						"total_runs":      10,
						"feedback_count":  6,
						"correct_count":   5,
						"accuracy_rate":   5.0 / 6.0,
						"at_gate_count":               4,
						"at_gate_correct":             4,
						"at_gate_accuracy_rate":       1.0,
						"post_incident_count":         2,
						"post_incident_correct":       1,
						"post_incident_accuracy_rate": 0.5,
						// Remediation fields.
						"remediation_feedback_count":        3,
						"remediation_correct_count":         2,
						"remediation_accuracy_rate":         2.0 / 3.0,
						"remediation_at_gate_count":         1,
						"remediation_at_gate_correct":       1,
						"remediation_post_incident_count":   2,
						"remediation_post_incident_correct": 1,
					},
				},
			},
		})
	}))
	defer srv.Close()

	info := fetchPlaybookInfo(srv.URL, "", "pbs_test")
	if info.check != playbookFound {
		t.Fatalf("check = %v, want playbookFound", info.check)
	}
	if info.feedbackCount != 6 {
		t.Errorf("feedbackCount = %d, want 6", info.feedbackCount)
	}
	if info.atGateCount != 4 || info.atGateCorrect != 4 {
		t.Errorf("atGate = %d/%d, want 4/4", info.atGateCorrect, info.atGateCount)
	}
	if info.atGateAccuracyRate != 1.0 {
		t.Errorf("atGateAccuracyRate = %f, want 1.0", info.atGateAccuracyRate)
	}
	if info.postIncidentCount != 2 || info.postIncidentCorrect != 1 {
		t.Errorf("postIncident = %d/%d, want 1/2", info.postIncidentCorrect, info.postIncidentCount)
	}
	if info.postIncidentAccuracyRate != 0.5 {
		t.Errorf("postIncidentAccuracyRate = %f, want 0.5", info.postIncidentAccuracyRate)
	}
	// Remediation fields.
	if info.remediationFeedbackCount != 3 {
		t.Errorf("remediationFeedbackCount = %d, want 3", info.remediationFeedbackCount)
	}
	if info.remediationCorrectCount != 2 {
		t.Errorf("remediationCorrectCount = %d, want 2", info.remediationCorrectCount)
	}
	if info.remediationAtGateCount != 1 || info.remediationAtGateCorrect != 1 {
		t.Errorf("remediationAtGate = %d/%d, want 1/1", info.remediationAtGateCorrect, info.remediationAtGateCount)
	}
	if info.remediationPostIncidentCount != 2 || info.remediationPostIncidentCorrect != 1 {
		t.Errorf("remediationPostIncident = %d/%d, want 1/2", info.remediationPostIncidentCorrect, info.remediationPostIncidentCount)
	}
}

// ── RemediationScore buckets ───────────────────────────────────────────────

// ── postEvaluations ───────────────────────────────────────────────────────

func TestPostEvaluations_PostsForEachResultWithRunID(t *testing.T) {
	var received []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
		received = append(received, body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	results := []EvalResult{
		{FailureID: "f1", RunID: "plr_a", KeywordScore: 1.0, ToolScore: 0.8, OverallScore: 0.85, Passed: true},
		{FailureID: "f2", RunID: "plr_b", KeywordScore: 0.5, OverallScore: 0.5, Passed: false},
		{FailureID: "f3", RunID: "", OverallScore: 0.9}, // no RunID — should be skipped
	}
	postEvaluations(srv.URL, "", results)

	if len(received) != 2 {
		t.Fatalf("POSTs received = %d, want 2 (result without RunID must be skipped)", len(received))
	}
}

func TestPostEvaluations_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	results := []EvalResult{{FailureID: "f1", RunID: "plr_x", OverallScore: 1.0}}
	postEvaluations(srv.URL, "secret-key", results)

	if gotAuth != "Bearer secret-key" {
		t.Errorf("Authorization = %q, want Bearer secret-key", gotAuth)
	}
}

func TestPostEvaluations_NonFatalOnServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	// Must not panic or call t.Fatal.
	results := []EvalResult{{FailureID: "f1", RunID: "plr_x", OverallScore: 0.5}}
	postEvaluations(srv.URL, "", results)
}

func TestPostEvaluations_NonFatalOnNetworkError(t *testing.T) {
	results := []EvalResult{{FailureID: "f1", RunID: "plr_x", OverallScore: 0.5}}
	postEvaluations("http://127.0.0.1:19997", "", results) // nothing listening
}

func TestPostEvaluations_BodyContainsScores(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	results := []EvalResult{{
		FailureID:      "db-tx-lock",
		FailureName:    "Lock chain",
		RunID:          "plr_scores",
		KeywordScore:   1.0,
		ToolScore:      0.75,
		DiagnosisScore: 0.9,
		OverallScore:   0.85,
		Passed:         true,
	}}
	postEvaluations(srv.URL, "", results)

	if gotBody["failure_id"] != "db-tx-lock" {
		t.Errorf("failure_id = %v", gotBody["failure_id"])
	}
	if gotBody["keyword_score"] != 1.0 {
		t.Errorf("keyword_score = %v, want 1.0", gotBody["keyword_score"])
	}
	if gotBody["overall_score"] != 0.85 {
		t.Errorf("overall_score = %v, want 0.85", gotBody["overall_score"])
	}
	if gotBody["passed"] != true {
		t.Errorf("passed = %v, want true", gotBody["passed"])
	}
}

func TestPostEvaluations_IncludesRemediationJudgeFields(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	results := []EvalResult{{
		RunID:                     "plr_rj01",
		RemediationJudgeScore:     0.67,
		RemediationJudgeReasoning: "correct approach, one extra step",
	}}
	postEvaluations(srv.URL, "", results)

	if gotBody["remediation_judge_score"] != 0.67 {
		t.Errorf("remediation_judge_score = %v, want 0.67", gotBody["remediation_judge_score"])
	}
	if gotBody["remediation_judge_reasoning"] != "correct approach, one extra step" {
		t.Errorf("remediation_judge_reasoning = %v", gotBody["remediation_judge_reasoning"])
	}
}

// ── fetchEvaluation ───────────────────────────────────────────────────────

func TestFetchEvaluation_Found(t *testing.T) {
	payload := map[string]any{
		"run_id":       "plr_ev01",
		"failure_id":   "db-oom",
		"overall_score": 0.9,
		"passed":       true,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(payload) //nolint:errcheck
	}))
	defer srv.Close()

	ev := fetchEvaluation(srv.URL, "", "plr_ev01")
	if ev == nil {
		t.Fatal("expected non-nil evaluation")
	}
	if ev.RunID != "plr_ev01" {
		t.Errorf("RunID = %q", ev.RunID)
	}
	if ev.OverallScore != 0.9 {
		t.Errorf("OverallScore = %v, want 0.9", ev.OverallScore)
	}
}

func TestFetchEvaluation_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	ev := fetchEvaluation(srv.URL, "", "plr_ghost")
	if ev != nil {
		t.Errorf("expected nil for 404, got %+v", ev)
	}
}

func TestFetchEvaluation_NetworkError(t *testing.T) {
	ev := fetchEvaluation("http://127.0.0.1:19996", "", "plr_x")
	if ev != nil {
		t.Errorf("expected nil on network error")
	}
}

func TestFetchEvaluation_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{"run_id": "plr_x"}) //nolint:errcheck
	}))
	defer srv.Close()

	fetchEvaluation(srv.URL, "tok-abc", "plr_x")
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("Authorization = %q, want Bearer tok-abc", gotAuth)
	}
}

// ── fetchVersionStats ─────────────────────────────────────────────────────

func TestFetchVersionStats_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"series_id": "pbs_test",
			"versions": []map[string]any{
				{"version": "1.0", "is_active": false, "total_runs": 3, "resolved": 2,
					"resolution_rate": 0.67, "avg_step_count": 4.0, "avg_recovery_secs": 42.0,
					"avg_diagnosis_score": 0.72, "diag_eval_count": 2,
					"avg_remediation_score": 0.0, "remed_eval_count": 0},
				{"version": "1.1", "is_active": true, "total_runs": 2, "resolved": 2,
					"resolution_rate": 1.0, "avg_step_count": 3.0, "avg_recovery_secs": 8.0,
					"avg_diagnosis_score": 0.91, "diag_eval_count": 2,
					"avg_remediation_score": 0.85, "remed_eval_count": 1},
			},
		})
	}))
	defer srv.Close()

	versions, err := fetchVersionStats(srv.URL, "", "pbs_test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("want 2 versions, got %d", len(versions))
	}
	if versions[0].Version != "1.0" || versions[0].IsActive {
		t.Errorf("v1.0: Version=%q IsActive=%v", versions[0].Version, versions[0].IsActive)
	}
	if versions[1].Version != "1.1" || !versions[1].IsActive {
		t.Errorf("v1.1: Version=%q IsActive=%v", versions[1].Version, versions[1].IsActive)
	}
	if versions[1].AvgDiagnosisScore != 0.91 {
		t.Errorf("v1.1 AvgDiagnosisScore = %v, want 0.91", versions[1].AvgDiagnosisScore)
	}
	if versions[1].RemedEvalCount != 1 || versions[1].AvgRemediationScore != 0.85 {
		t.Errorf("v1.1 remed: count=%d score=%v, want count=1 score=0.85",
			versions[1].RemedEvalCount, versions[1].AvgRemediationScore)
	}
	if versions[0].RemedEvalCount != 0 {
		t.Errorf("v1.0 RemedEvalCount = %d, want 0", versions[0].RemedEvalCount)
	}
}

func TestFetchVersionStats_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"series_id": "pbs_none", "versions": []any{}}) //nolint:errcheck
	}))
	defer srv.Close()

	versions, err := fetchVersionStats(srv.URL, "", "pbs_none")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("want 0 versions, got %d", len(versions))
	}
}

func TestFetchVersionStats_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := fetchVersionStats(srv.URL, "", "pbs_test")
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestFetchVersionStats_NetworkError(t *testing.T) {
	_, err := fetchVersionStats("http://127.0.0.1:19997", "", "pbs_test")
	if err == nil {
		t.Error("expected error on network failure")
	}
}

func TestFetchVersionStats_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{"series_id": "pbs_test", "versions": []any{}}) //nolint:errcheck
	}))
	defer srv.Close()

	fetchVersionStats(srv.URL, "tok-xyz", "pbs_test") //nolint:errcheck
	if gotAuth != "Bearer tok-xyz" {
		t.Errorf("Authorization = %q, want Bearer tok-xyz", gotAuth)
	}
}

// ── formatDuration ────────────────────────────────────────────────────────

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		secs float64
		want string
	}{
		{0, "–"},
		{-5, "–"},
		{42, "42s"},
		{60, "1m0s"},
		{83, "1m23s"},
		{3600, "1h0m"},
		{3661, "1h1m"},
		{7322, "2h2m"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.secs)
		if got != tt.want {
			t.Errorf("formatDuration(%.0f) = %q, want %q", tt.secs, got, tt.want)
		}
	}
}

// ── fetchCalibration ──────────────────────────────────────────────────────

func TestFetchCalibration_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/fleet/calibration" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"series_id":  "",
			"total_runs": 9,
			"bands": []map[string]any{
				{"band": "90-100%", "runs": 5, "correct": 4, "actual_accuracy": 0.80, "calibration": "OVERCONFIDENT"},
				{"band": "70-89%", "runs": 4, "correct": 3, "actual_accuracy": 0.75, "calibration": "WELL_CALIBRATED"},
				{"band": "<70%", "runs": 0, "correct": 0, "actual_accuracy": 0.0, "calibration": "INSUFFICIENT_DATA"},
			},
		})
	}))
	defer srv.Close()

	report, err := fetchCalibration(srv.URL, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.TotalRuns != 9 {
		t.Errorf("TotalRuns = %d, want 9", report.TotalRuns)
	}
	if len(report.Bands) != 3 {
		t.Fatalf("want 3 bands, got %d", len(report.Bands))
	}
	if report.Bands[0].Band != "90-100%" || report.Bands[0].Calibration != "OVERCONFIDENT" {
		t.Errorf("Bands[0]: Band=%q Calibration=%q", report.Bands[0].Band, report.Bands[0].Calibration)
	}
	if report.Bands[1].Calibration != "WELL_CALIBRATED" {
		t.Errorf("Bands[1].Calibration = %q, want WELL_CALIBRATED", report.Bands[1].Calibration)
	}
}

func TestFetchCalibration_WithSeriesID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"series_id": "pbs_test", "total_runs": 0, "bands": []any{},
		})
	}))
	defer srv.Close()

	_, err := fetchCalibration(srv.URL, "", "pbs_test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v1/fleet/calibration?series_id=pbs_test" {
		t.Errorf("path = %q, want /api/v1/fleet/calibration?series_id=pbs_test", gotPath)
	}
}

func TestFetchCalibration_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := fetchCalibration(srv.URL, "", "")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestFetchCalibration_NetworkError(t *testing.T) {
	_, err := fetchCalibration("http://127.0.0.1:0", "", "")
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestFetchCalibration_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"total_runs": 0, "bands": []any{}}) //nolint:errcheck
	}))
	defer srv.Close()

	fetchCalibration(srv.URL, "tok-xyz", "") //nolint:errcheck
	if gotAuth != "Bearer tok-xyz" {
		t.Errorf("Authorization = %q, want Bearer tok-xyz", gotAuth)
	}
}

func TestFetchCalibration_DecodesRemediationBands(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"total_runs":       5,
			"remediation_runs": 3,
			"bands": []map[string]any{
				{"band": "90-100%", "runs": 5, "correct": 4, "actual_accuracy": 0.80, "calibration": "OVERCONFIDENT"},
			},
			"remediation_bands": []map[string]any{
				{"band": "90-100%", "runs": 2, "correct": 2, "actual_accuracy": 1.0, "calibration": "UNDERCONFIDENT"},
				{"band": "70-89%", "runs": 1, "correct": 1, "actual_accuracy": 1.0, "calibration": "UNDERCONFIDENT"},
				{"band": "<70%", "runs": 0, "correct": 0, "actual_accuracy": 0.0, "calibration": "INSUFFICIENT_DATA"},
			},
		})
	}))
	defer srv.Close()

	report, err := fetchCalibration(srv.URL, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.RemediationRuns != 3 {
		t.Errorf("RemediationRuns = %d, want 3", report.RemediationRuns)
	}
	if len(report.RemediationBands) != 3 {
		t.Fatalf("RemediationBands len = %d, want 3", len(report.RemediationBands))
	}
	if report.RemediationBands[0].Band != "90-100%" {
		t.Errorf("RemediationBands[0].Band = %q", report.RemediationBands[0].Band)
	}
	if report.RemediationBands[0].Runs != 2 || report.RemediationBands[0].Correct != 2 {
		t.Errorf("RemediationBands[0]: Runs=%d Correct=%d, want 2/2", report.RemediationBands[0].Runs, report.RemediationBands[0].Correct)
	}
	if report.RemediationBands[0].Calibration != "UNDERCONFIDENT" {
		t.Errorf("RemediationBands[0].Calibration = %q, want UNDERCONFIDENT", report.RemediationBands[0].Calibration)
	}
}

// ── fetchRemediationSteps ─────────────────────────────────────────────────

func TestFetchRemediationSteps_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/fleet/playbook-runs/plr_steps01/steps" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{ //nolint:errcheck
			{"tool": "kill_idle_connections", "args": map[string]any{"idle_threshold": "5m"}, "status": "succeeded", "result": "20 terminated"},
			{"tool": "get_active_connections", "args": map[string]any{}, "status": "succeeded", "result": "count=3"},
		})
	}))
	defer srv.Close()

	steps := fetchRemediationSteps(context.Background(), srv.URL, "", "plr_steps01")
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(steps))
	}
	if steps[0].Tool != "kill_idle_connections" {
		t.Errorf("steps[0].Tool = %q, want kill_idle_connections", steps[0].Tool)
	}
	if steps[0].Status != "succeeded" {
		t.Errorf("steps[0].Status = %q, want succeeded", steps[0].Status)
	}
	if steps[0].Result != "20 terminated" {
		t.Errorf("steps[0].Result = %q", steps[0].Result)
	}
	if steps[1].Tool != "get_active_connections" {
		t.Errorf("steps[1].Tool = %q", steps[1].Tool)
	}
}

func TestFetchRemediationSteps_EmptyRunID(t *testing.T) {
	// runID="" → returns nil without making any HTTP request.
	steps := fetchRemediationSteps(context.Background(), "http://localhost:9999", "", "")
	if steps != nil {
		t.Errorf("expected nil for empty runID, got %v", steps)
	}
}

func TestFetchRemediationSteps_EmptyGatewayURL(t *testing.T) {
	steps := fetchRemediationSteps(context.Background(), "", "", "plr_steps01")
	if steps != nil {
		t.Errorf("expected nil for empty gatewayURL, got %v", steps)
	}
}

func TestFetchRemediationSteps_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	steps := fetchRemediationSteps(context.Background(), srv.URL, "", "plr_steps01")
	if steps != nil {
		t.Errorf("expected nil on server error, got %v", steps)
	}
}

func TestFetchRemediationSteps_NetworkError(t *testing.T) {
	steps := fetchRemediationSteps(context.Background(), "http://127.0.0.1:0", "", "plr_steps01")
	if steps != nil {
		t.Errorf("expected nil on network error, got %v", steps)
	}
}

func TestFetchRemediationSteps_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{}) //nolint:errcheck
	}))
	defer srv.Close()

	fetchRemediationSteps(context.Background(), srv.URL, "sk-auth", "plr_steps01")
	if gotAuth != "Bearer sk-auth" {
		t.Errorf("Authorization = %q, want Bearer sk-auth", gotAuth)
	}
}

func TestFetchRemediationSteps_EmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{}) //nolint:errcheck
	}))
	defer srv.Close()

	steps := fetchRemediationSteps(context.Background(), srv.URL, "", "plr_steps01")
	if len(steps) != 0 {
		t.Errorf("expected 0 steps for empty response, got %d", len(steps))
	}
}

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

// ── fetchFaultRunHistory ───────────────────────────────────────────────────

func TestFetchFaultRunHistory_Success(t *testing.T) {
	now := "2026-01-15T12:00:00Z"
	past := "2026-01-01T12:00:00Z"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("since_days") == "" {
			t.Error("since_days param missing")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"entries": []map[string]any{
				{"run_id": "plr_a", "failure_id": "db-lock-contention", "timestamp": now, "passed": true},
				{"run_id": "plr_b", "failure_id": "db-lock-contention", "timestamp": past, "passed": false},
				{"run_id": "plr_c", "failure_id": "k8s-crashloop", "timestamp": now, "passed": true},
			},
		})
	}))
	defer srv.Close()

	cutoff, _ := time.Parse(time.RFC3339, "2025-12-01T00:00:00Z")
	midPoint, _ := time.Parse(time.RFC3339, "2026-01-08T00:00:00Z")

	result, err := fetchFaultRunHistory(srv.URL, "", 90, "", cutoff, midPoint)
	if err != nil {
		t.Fatalf("fetchFaultRunHistory: %v", err)
	}
	// Both db-lock-contention entries should appear (past is firstHalf, now is secondHalf).
	dbStats, ok := result["db-lock-contention"]
	if !ok {
		t.Fatal("db-lock-contention not in result")
	}
	if len(dbStats.firstHalf) != 1 || len(dbStats.secondHalf) != 1 {
		t.Errorf("db-lock-contention halves: first=%d second=%d, want 1/1",
			len(dbStats.firstHalf), len(dbStats.secondHalf))
	}
	if _, ok := result["k8s-crashloop"]; !ok {
		t.Error("k8s-crashloop not in result")
	}
}

func TestFetchFaultRunHistory_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"entries": []any{}}) //nolint:errcheck
	}))
	defer srv.Close()

	cutoff := time.Now().Add(-90 * 24 * time.Hour)
	mid := time.Now().Add(-45 * 24 * time.Hour)
	fetchFaultRunHistory(srv.URL, "my-key", 90, "", cutoff, mid) //nolint:errcheck
	if gotAuth != "Bearer my-key" {
		t.Errorf("Authorization = %q, want Bearer my-key", gotAuth)
	}
}

func TestFetchFaultRunHistory_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	cutoff := time.Now().Add(-90 * 24 * time.Hour)
	mid := time.Now().Add(-45 * 24 * time.Hour)
	_, err := fetchFaultRunHistory(srv.URL, "", 90, "", cutoff, mid)
	if err == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

func TestFetchFaultRunHistory_NetworkError(t *testing.T) {
	cutoff := time.Now().Add(-90 * 24 * time.Hour)
	mid := time.Now().Add(-45 * 24 * time.Hour)
	_, err := fetchFaultRunHistory("http://127.0.0.1:19998", "", 90, "", cutoff, mid)
	if err == nil {
		t.Error("expected error for unreachable server, got nil")
	}
}
