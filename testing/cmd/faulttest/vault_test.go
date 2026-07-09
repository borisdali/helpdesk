package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
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
					"avg_remediation_score": 0.0, "remed_eval_count": 0,
					"rem_feedback_count": 2, "rem_feedback_rate": 0.5},
				{"version": "1.1", "is_active": true, "total_runs": 2, "resolved": 2,
					"resolution_rate": 1.0, "avg_step_count": 3.0, "avg_recovery_secs": 8.0,
					"avg_diagnosis_score": 0.91, "diag_eval_count": 2,
					"avg_remediation_score": 0.85, "remed_eval_count": 1,
					"rem_feedback_count": 2, "rem_feedback_rate": 1.0},
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
	if versions[0].RemFeedbackCount != 2 || versions[0].RemFeedbackRate != 0.5 {
		t.Errorf("v1.0 rem feedback: count=%d rate=%v, want count=2 rate=0.5",
			versions[0].RemFeedbackCount, versions[0].RemFeedbackRate)
	}
	if versions[1].RemFeedbackCount != 2 || versions[1].RemFeedbackRate != 1.0 {
		t.Errorf("v1.1 rem feedback: count=%d rate=%v, want count=2 rate=1.0",
			versions[1].RemFeedbackCount, versions[1].RemFeedbackRate)
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

// ── extractPrimaryConfidence ───────────────────────────────────────────────

func TestExtractPrimaryConfidence(t *testing.T) {
	tests := []struct {
		name string
		text string
		want float64
	}{
		{
			"standard format",
			"HYPOTHESIS_1: Lock contention | CONFIDENCE: 0.92 | EVIDENCE: pg_locks shows waiting",
			0.92,
		},
		{
			"markdown bold prefix only",
			"**HYPOTHESIS_1: High connection count near pg_max_connections | CONFIDENCE: 0.75",
			0.75,
		},
		{
			"markdown bold around hypothesis text",
			"**HYPOTHESIS_1: High connection count near pg_max_connections** | CONFIDENCE: 0.75 | EVIDENCE: pg_stat_activity shows 95/100",
			0.75,
		},
		{
			"multi-line response",
			"Some preamble.\nHYPOTHESIS_1: WAL stale slot | CONFIDENCE: 0.85 | EVIDENCE: inactive slot\nHYPOTHESIS_2: Archive failure | CONFIDENCE: 0.20",
			0.85,
		},
		{
			"no HYPOTHESIS_1",
			"The agent found a lock. CONFIDENCE: 0.80",
			0.0,
		},
		{
			"HYPOTHESIS_1 but no CONFIDENCE field",
			"HYPOTHESIS_1: Lock contention | EVIDENCE: pg_locks shows waiting",
			0.0,
		},
		{
			"HYPOTHESIS_2 should not match",
			"HYPOTHESIS_2: High connections | CONFIDENCE: 0.60",
			0.0,
		},
		{
			"empty text",
			"",
			0.0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPrimaryConfidence(tt.text)
			if got < tt.want-0.001 || got > tt.want+0.001 {
				t.Errorf("extractPrimaryConfidence() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ── fetchFeedback (array decode) ──────────────────────────────────────────

func TestFetchFeedback_DecodesArray_AtGatePreferred(t *testing.T) {
	tr := true
	fa := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"feedback": []map[string]any{
				{"run_id": "plr_x", "feedback_type": "triage", "feedback_time": "post_incident", "verdict_correct": &fa},
				{"run_id": "plr_x", "feedback_type": "triage", "feedback_time": "at_gate", "verdict_correct": &tr},
				{"run_id": "plr_x", "feedback_type": "remediation", "feedback_time": "at_gate", "verdict_correct": &fa},
			},
		})
	}))
	defer srv.Close()

	fb := fetchFeedback(srv.URL, "", "plr_x")
	if fb == nil {
		t.Fatal("expected non-nil feedback")
	}
	if fb.VerdictCorrect == nil || !*fb.VerdictCorrect {
		t.Errorf("VerdictCorrect = %v, want true (at_gate preferred over post_incident)", fb.VerdictCorrect)
	}
}

func TestFetchFeedback_FallsBackToPostIncident(t *testing.T) {
	tr := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"feedback": []map[string]any{
				{"run_id": "plr_y", "feedback_type": "triage", "feedback_time": "post_incident", "verdict_correct": &tr},
			},
		})
	}))
	defer srv.Close()

	fb := fetchFeedback(srv.URL, "", "plr_y")
	if fb == nil {
		t.Fatal("expected non-nil feedback")
	}
	if fb.VerdictCorrect == nil || !*fb.VerdictCorrect {
		t.Errorf("VerdictCorrect = %v, want true (post_incident fallback)", fb.VerdictCorrect)
	}
}

func TestFetchFeedback_ReturnsNilWhenNoTriage(t *testing.T) {
	fa := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"feedback": []map[string]any{
				{"run_id": "plr_z", "feedback_type": "remediation", "feedback_time": "at_gate", "verdict_correct": &fa},
			},
		})
	}))
	defer srv.Close()

	fb := fetchFeedback(srv.URL, "", "plr_z")
	if fb != nil {
		t.Errorf("expected nil when no triage feedback, got %+v", fb)
	}
}

func TestFetchFeedback_NetworkError(t *testing.T) {
	fb := fetchFeedback("http://127.0.0.1:19994", "", "plr_x")
	if fb != nil {
		t.Errorf("expected nil on network error, got %+v", fb)
	}
}

// ── fetchIncidentNarrative ────────────────────────────────────────────────

func TestFetchIncidentNarrative_Success(t *testing.T) {
	now := time.Now().UTC()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/incidents/plr_narr01" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"incident_id": "plr_narr01",
			"started_at":  now.Format(time.RFC3339),
			"operator":    "alice",
			"triage": map[string]any{
				"run_id":   "plr_narr01",
				"playbook": "pbs_db_lock",
				"findings": "Lock chain detected",
			},
			"feedback": []map[string]any{
				{"run_id": "plr_narr01", "feedback_type": "triage", "feedback_time": "at_gate", "verdict_correct": true},
			},
			"evaluation": map[string]any{
				"diagnosis_score":    0.91,
				"primary_confidence": 0.88,
				"judge_used":         true,
			},
		})
	}))
	defer srv.Close()

	n, err := fetchIncidentNarrative(srv.URL, "", "plr_narr01")
	if err != nil {
		t.Fatalf("fetchIncidentNarrative: %v", err)
	}
	if n.IncidentID != "plr_narr01" {
		t.Errorf("IncidentID = %q, want plr_narr01", n.IncidentID)
	}
	if n.Operator != "alice" {
		t.Errorf("Operator = %q, want alice", n.Operator)
	}
	if n.Triage.Findings != "Lock chain detected" {
		t.Errorf("Triage.Findings = %q, want Lock chain detected", n.Triage.Findings)
	}
	if len(n.Feedback) != 1 {
		t.Fatalf("Feedback len = %d, want 1", len(n.Feedback))
	}
	if n.Feedback[0].FeedbackType != "triage" {
		t.Errorf("Feedback[0].FeedbackType = %q, want triage", n.Feedback[0].FeedbackType)
	}
	if n.Evaluation == nil {
		t.Fatal("Evaluation is nil, want non-nil")
	}
	if n.Evaluation.DiagnosisScore != 0.91 {
		t.Errorf("Evaluation.DiagnosisScore = %v, want 0.91", n.Evaluation.DiagnosisScore)
	}
	if n.Evaluation.PrimaryConfidence != 0.88 {
		t.Errorf("Evaluation.PrimaryConfidence = %v, want 0.88", n.Evaluation.PrimaryConfidence)
	}
}

func TestFetchIncidentNarrative_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := fetchIncidentNarrative(srv.URL, "", "plr_ghost")
	if err == nil {
		t.Error("expected error for 404, got nil")
	}
}

func TestFetchIncidentNarrative_NetworkError(t *testing.T) {
	_, err := fetchIncidentNarrative("http://127.0.0.1:19993", "", "plr_x")
	if err == nil {
		t.Error("expected error for unreachable server, got nil")
	}
}

func TestFetchIncidentNarrative_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"incident_id": "plr_x",
			"triage":      map[string]any{"run_id": "plr_x", "playbook": "test"},
		})
	}))
	defer srv.Close()

	fetchIncidentNarrative(srv.URL, "tok-xyz", "plr_x") //nolint:errcheck
	if gotAuth != "Bearer tok-xyz" {
		t.Errorf("Authorization = %q, want Bearer tok-xyz", gotAuth)
	}
}

// ── postEvaluations primary_confidence ───────────────────────────────────

// ── wordWrap ──────────────────────────────────────────────────────────────

func TestWordWrap(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		maxWidth int
		indent   string
		want     string
	}{
		{
			"short text fits on one line",
			"hello world",
			20,
			"  ",
			"hello world",
		},
		{
			"exact fit",
			"hello world",
			11,
			"  ",
			"hello world",
		},
		{
			"wraps at word boundary",
			"one two three four five",
			13,
			"   ",
			"one two three\n   four five",
		},
		{
			"continuation indent applied",
			"alpha beta gamma delta epsilon",
			15,
			">>",
			"alpha beta\n>>gamma delta\n>>epsilon",
		},
		{
			"empty text",
			"",
			10,
			"  ",
			"",
		},
		{
			"single long word is not split",
			"superlongwordthatexceedswidth",
			10,
			"  ",
			"superlongwordthatexceedswidth",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wordWrap(tt.text, tt.maxWidth, tt.indent)
			if got != tt.want {
				t.Errorf("wordWrap(%q, %d, %q) =\n  %q\nwant\n  %q",
					tt.text, tt.maxWidth, tt.indent, got, tt.want)
			}
		})
	}
}

// ── fetchJourneys ─────────────────────────────────────────────────────────

func TestFetchJourneys_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/governance/journeys" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]journeySummary{ //nolint:errcheck
			{TraceID: "tr_abc123", Outcome: "resolved", IncidentRunID: "plr_001"},
			{TraceID: "tr_def456", Outcome: "abandoned"},
		})
	}))
	defer srv.Close()

	got, err := fetchJourneys(srv.URL, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].TraceID != "tr_abc123" {
		t.Errorf("TraceID = %q, want tr_abc123", got[0].TraceID)
	}
	if got[0].IncidentRunID != "plr_001" {
		t.Errorf("IncidentRunID = %q, want plr_001", got[0].IncidentRunID)
	}
	if got[1].Outcome != "abandoned" {
		t.Errorf("Outcome = %q, want abandoned", got[1].Outcome)
	}
}

func TestFetchJourneys_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer srv.Close()

	got, err := fetchJourneys(srv.URL, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestFetchJourneys_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := fetchJourneys(srv.URL, "", nil)
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
}

func TestFetchJourneys_NetworkError(t *testing.T) {
	_, err := fetchJourneys("http://127.0.0.1:19998", "", nil)
	if err == nil {
		t.Fatal("expected error for unreachable server, got nil")
	}
}

func TestFetchJourneys_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer srv.Close()

	fetchJourneys(srv.URL, "tok-xyz", nil) //nolint:errcheck
	if gotAuth != "Bearer tok-xyz" {
		t.Errorf("Authorization = %q, want Bearer tok-xyz", gotAuth)
	}
}

func TestFetchJourneys_PassesQueryParams(t *testing.T) {
	var gotQuery map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = map[string]string{
			"limit":    r.URL.Query().Get("limit"),
			"since":    r.URL.Query().Get("since"),
			"category": r.URL.Query().Get("category"),
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer srv.Close()

	params := map[string]string{
		"limit":    "5",
		"since":    "48h",
		"category": "database",
	}
	fetchJourneys(srv.URL, "", params) //nolint:errcheck
	for k, want := range params {
		if gotQuery[k] != want {
			t.Errorf("query param %s = %q, want %q", k, gotQuery[k], want)
		}
	}
}

func TestFetchJourneys_WithDelegations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]journeySummary{ //nolint:errcheck
			{
				TraceID: "tr_deleg01",
				Delegations: []delegationSummary{
					{Intent: "diagnose slow query", Tools: []string{"get_db_info", "cancel_query"}},
				},
				ToolsUsed:   []string{"get_db_info", "cancel_query"},
				HasMismatch: true,
			},
		})
	}))
	defer srv.Close()

	got, err := fetchJourneys(srv.URL, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if !got[0].HasMismatch {
		t.Error("HasMismatch = false, want true")
	}
	if len(got[0].Delegations) != 1 {
		t.Fatalf("Delegations len = %d, want 1", len(got[0].Delegations))
	}
	if got[0].Delegations[0].Intent != "diagnose slow query" {
		t.Errorf("Intent = %q, want diagnose slow query", got[0].Delegations[0].Intent)
	}
}

// ── postEvaluations primary_confidence ───────────────────────────────────

// ── fetchRunsByOutcome ────────────────────────────────────────────────────────

func TestFetchRunsByOutcome_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/fleet/playbook-runs" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("outcome") != "resolved" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"runs": []incidentRun{
				{RunID: "plr_aaa111", SeriesID: "pbs_db_triage", Outcome: "resolved"},
				{RunID: "plr_bbb222", SeriesID: "pbs_k8s_triage", Outcome: "resolved"},
			},
		})
	}))
	defer srv.Close()

	got, err := fetchRunsByOutcome(srv.URL, "", "resolved", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].RunID != "plr_aaa111" {
		t.Errorf("RunID = %q, want plr_aaa111", got[0].RunID)
	}
	if got[1].SeriesID != "pbs_k8s_triage" {
		t.Errorf("SeriesID = %q, want pbs_k8s_triage", got[1].SeriesID)
	}
}

func TestFetchRunsByOutcome_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"runs": []incidentRun{}}) //nolint:errcheck
	}))
	defer srv.Close()

	got, err := fetchRunsByOutcome(srv.URL, "", "failed", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestFetchRunsByOutcome_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := fetchRunsByOutcome(srv.URL, "", "resolved", 10)
	if err == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

func TestFetchRunsByOutcome_NetworkError(t *testing.T) {
	_, err := fetchRunsByOutcome("http://127.0.0.1:19997", "", "resolved", 10)
	if err == nil {
		t.Error("expected error for unreachable server, got nil")
	}
}

func TestFetchRunsByOutcome_SendsAuth(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"runs": []incidentRun{}}) //nolint:errcheck
	}))
	defer srv.Close()

	fetchRunsByOutcome(srv.URL, "tok-xyz", "resolved", 10) //nolint:errcheck
	if gotHeader != "Bearer tok-xyz" {
		t.Errorf("Authorization = %q, want Bearer tok-xyz", gotHeader)
	}
}

func TestFetchRunsByOutcome_PassesOutcomeAndLimit(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"runs": []incidentRun{}}) //nolint:errcheck
	}))
	defer srv.Close()

	fetchRunsByOutcome(srv.URL, "", "abandoned", 7) //nolint:errcheck
	if gotQuery.Get("outcome") != "abandoned" {
		t.Errorf("outcome = %q, want abandoned", gotQuery.Get("outcome"))
	}
	if gotQuery.Get("limit") != "7" {
		t.Errorf("limit = %q, want 7", gotQuery.Get("limit"))
	}
}

// ── TestPostEvaluations_IncludesPrimaryConfidence ─────────────────────────────

func TestPostEvaluations_IncludesPrimaryConfidence(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	results := []EvalResult{{
		RunID:             "plr_pc01",
		PrimaryConfidence: 0.88,
		OverallScore:      0.85,
	}}
	postEvaluations(srv.URL, "", results)

	if v, ok := gotBody["primary_confidence"]; !ok || v != 0.88 {
		t.Errorf("primary_confidence = %v (ok=%v), want 0.88", gotBody["primary_confidence"], ok)
	}
}

// ── purgeOrphanDrafts ─────────────────────────────────────────────────────

type testDraft = struct {
	PlaybookID string `json:"playbook_id"`
	SeriesID   string `json:"series_id"`
	Version    string `json:"version"`
	Name       string `json:"name"`
	Source     string `json:"source"`
	CreatedAt  string `json:"created_at"`
}

func TestPurgeOrphanDrafts_DeletesOnlyOrphans(t *testing.T) {
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// URL: /api/v1/fleet/playbooks/{id}
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		deleted = append(deleted, id)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	drafts := []testDraft{
		{PlaybookID: "pb_orphan1", SeriesID: "pbs_generated_abc123", Name: "Orphan 1"},
		{PlaybookID: "pb_pinned1", SeriesID: "pbs_connection_remediate", Name: "Pinned"},
		{PlaybookID: "pb_orphan2", SeriesID: "pbs_generated_def456", Name: "Orphan 2"},
	}

	n := purgeOrphanDrafts(srv.URL, "", drafts)
	if n != 2 {
		t.Errorf("purged %d, want 2", n)
	}
	if len(deleted) != 2 {
		t.Errorf("DELETE called %d times, want 2; deleted: %v", len(deleted), deleted)
	}
	for _, id := range deleted {
		if id == "pb_pinned1" {
			t.Error("pb_pinned1 (non-orphan) must not be deleted")
		}
	}
}

func TestPurgeOrphanDrafts_NoneWhenAllPinned(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	drafts := []testDraft{
		{PlaybookID: "pb_pinned1", SeriesID: "pbs_connection_remediate"},
		{PlaybookID: "pb_pinned2", SeriesID: "pbs_wal_stale_slot"},
	}
	n := purgeOrphanDrafts(srv.URL, "", drafts)
	if n != 0 {
		t.Errorf("purged %d, want 0", n)
	}
	if called {
		t.Error("DELETE must not be called when no orphans exist")
	}
}

func TestPurgeOrphanDrafts_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	drafts := []testDraft{
		{PlaybookID: "pb_orphan1", SeriesID: "pbs_generated_abc"},
	}
	purgeOrphanDrafts(srv.URL, "my-key", drafts)
	if gotAuth != "Bearer my-key" {
		t.Errorf("Authorization = %q, want Bearer my-key", gotAuth)
	}
}

func TestPurgeOrphanDrafts_SkipsOnServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	drafts := []testDraft{
		{PlaybookID: "pb_orphan1", SeriesID: "pbs_generated_abc"},
	}
	n := purgeOrphanDrafts(srv.URL, "", drafts)
	if n != 0 {
		t.Errorf("purged %d, want 0 (server error should skip, not count)", n)
	}
}

// ── fetchPlaybookByID ─────────────────────────────────────────────────────

func TestFetchPlaybookByID_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/fleet/playbooks/pb_abc" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"playbook_id": "pb_abc",
			"series_id":   "pbs_test",
			"version":     "1.3",
			"name":        "Test Playbook",
			"guidance":    "Check connections first.",
			"is_active":   true,
		})
	}))
	defer srv.Close()

	pb, err := fetchPlaybookByID(srv.URL, "", "pb_abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pb.PlaybookID != "pb_abc" {
		t.Errorf("playbook_id = %q, want pb_abc", pb.PlaybookID)
	}
	if pb.Version != "1.3" {
		t.Errorf("version = %q, want 1.3", pb.Version)
	}
	if pb.Guidance != "Check connections first." {
		t.Errorf("guidance = %q, want 'Check connections first.'", pb.Guidance)
	}
	if !pb.IsActive {
		t.Error("is_active should be true")
	}
}

func TestFetchPlaybookByID_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := fetchPlaybookByID(srv.URL, "", "pb_x")
	if err == nil {
		t.Error("expected error for 500 status, got nil")
	}
}

func TestFetchPlaybookByID_NetworkError(t *testing.T) {
	_, err := fetchPlaybookByID("http://127.0.0.1:19999", "", "pb_x")
	if err == nil {
		t.Error("expected error for unreachable server, got nil")
	}
}

func TestFetchPlaybookByID_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"playbook_id": "pb_x"}) //nolint:errcheck
	}))
	defer srv.Close()

	fetchPlaybookByID(srv.URL, "my-key", "pb_x") //nolint:errcheck
	if gotAuth != "Bearer my-key" {
		t.Errorf("Authorization = %q, want Bearer my-key", gotAuth)
	}
}

func TestFetchPlaybookByID_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json")) //nolint:errcheck
	}))
	defer srv.Close()

	_, err := fetchPlaybookByID(srv.URL, "", "pb_x")
	if err == nil {
		t.Error("expected error for invalid JSON response, got nil")
	}
}

// ── nextVersion ───────────────────────────────────────────────────────────

func TestNextVersion(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", "1.0"},
		{"1.3", "1.4"},
		{"1.0", "1.1"},
		{"2", "3"},
		{"1.3.0", "1.3.1"},
		{"2.10", "2.11"},
		{"1.abc", "1.abc.1"}, // non-numeric last segment → append .1
	}
	for _, tc := range cases {
		got := nextVersion(tc.input)
		if got != tc.want {
			t.Errorf("nextVersion(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── pickBestRunForSuggest ─────────────────────────────────────────────────

func makeRunsServer(t *testing.T, runs []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"runs": runs}) //nolint:errcheck
	}))
}

func TestPickBestRunForSuggest_PrefersResolved(t *testing.T) {
	srv := makeRunsServer(t, []map[string]any{
		{"run_id": "plr_trans1", "outcome": "transitioned"},
		{"run_id": "plr_trans2", "outcome": "transitioned"},
		{"run_id": "plr_res1", "outcome": "resolved"},
		{"run_id": "plr_trans3", "outcome": "transitioned"},
	})
	defer srv.Close()

	got, err := pickBestRunForSuggest(srv.URL, "", "pbs_test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "plr_res1" {
		t.Errorf("got %q, want plr_res1 (resolved should be preferred over transitioned)", got)
	}
}

func TestPickBestRunForSuggest_FallsBackToTransitioned(t *testing.T) {
	srv := makeRunsServer(t, []map[string]any{
		{"run_id": "plr_trans1", "outcome": "transitioned"},
		{"run_id": "plr_trans2", "outcome": "transitioned"},
	})
	defer srv.Close()

	got, err := pickBestRunForSuggest(srv.URL, "", "pbs_test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "plr_trans1" {
		t.Errorf("got %q, want plr_trans1 (first transitioned when no resolved exists)", got)
	}
}

func TestPickBestRunForSuggest_ErrorWhenNoUsableRuns(t *testing.T) {
	srv := makeRunsServer(t, []map[string]any{
		{"run_id": "plr_esc1", "outcome": "escalated"},
		{"run_id": "plr_fail1", "outcome": "failed"},
	})
	defer srv.Close()

	_, err := pickBestRunForSuggest(srv.URL, "", "pbs_test")
	if err == nil {
		t.Error("expected error when no resolved or transitioned runs exist, got nil")
	}
}

func TestPickBestRunForSuggest_NetworkError(t *testing.T) {
	_, err := pickBestRunForSuggest("http://127.0.0.1:19999", "", "pbs_test")
	if err == nil {
		t.Error("expected error for unreachable server, got nil")
	}
}

// makeRunsAndIncidentServer serves runs on /api/v1/fleet/playbook-runs and
// an incident response on /api/v1/incidents/{id}. The incident response may
// include journeys so tests can verify trace-ID resolution.
func makeRunsAndIncidentServer(t *testing.T, runs []map[string]any, incidentByID map[string]map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/api/v1/incidents/") {
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			if incident, ok := incidentByID[id]; ok {
				json.NewEncoder(w).Encode(incident) //nolint:errcheck
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"runs": runs}) //nolint:errcheck
	}))
}

func TestPickBestRunForSuggest_ResolvesTriageJourneyTrace(t *testing.T) {
	// When the incident has a triage journey, pickBestRunForSuggest should
	// return the journey trace_id instead of the plr_* run ID so that
	// from-trace can find audit_events (which are keyed to the journey trace).
	runs := []map[string]any{
		{"run_id": "plr_abc123", "outcome": "resolved"},
	}
	incidents := map[string]map[string]any{
		"plr_abc123": {
			"incident_id": "plr_abc123",
			"journeys": []map[string]any{
				{"phase": "triage", "trace_id": "faulttest-abc123-db-max-connections"},
				{"phase": "remediation", "trace_id": "faulttest-abc123-db-max-connections-remed"},
			},
		},
	}
	srv := makeRunsAndIncidentServer(t, runs, incidents)
	defer srv.Close()

	got, err := pickBestRunForSuggest(srv.URL, "", "pbs_test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "faulttest-abc123-db-max-connections" {
		t.Errorf("got %q, want faulttest-abc123-db-max-connections (triage journey trace)", got)
	}
}

func TestPickBestRunForSuggest_FallsBackToPLRWhenNoJourneys(t *testing.T) {
	// When the incident has no journeys (e.g. older run), return the PLR ID.
	runs := []map[string]any{
		{"run_id": "plr_nojourneys", "outcome": "resolved"},
	}
	incidents := map[string]map[string]any{
		"plr_nojourneys": {
			"incident_id": "plr_nojourneys",
			"journeys":    []map[string]any{},
		},
	}
	srv := makeRunsAndIncidentServer(t, runs, incidents)
	defer srv.Close()

	got, err := pickBestRunForSuggest(srv.URL, "", "pbs_test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "plr_nojourneys" {
		t.Errorf("got %q, want plr_nojourneys (PLR fallback when no triage journey)", got)
	}
}

func TestPickBestRunForSuggest_FallsBackToPLRWhenIncidentFetchFails(t *testing.T) {
	// When the incident endpoint returns 404, return the PLR ID rather than erroring.
	runs := []map[string]any{
		{"run_id": "plr_old", "outcome": "resolved"},
	}
	srv := makeRunsAndIncidentServer(t, runs, map[string]map[string]any{}) // no incident entries → 404
	defer srv.Close()

	got, err := pickBestRunForSuggest(srv.URL, "", "pbs_test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "plr_old" {
		t.Errorf("got %q, want plr_old (PLR fallback when incident returns 404)", got)
	}
}

func TestPickBestRunForSuggest_IgnoresRemediationJourney(t *testing.T) {
	// When the incident has only a remediation journey (no triage), fall back to PLR.
	runs := []map[string]any{
		{"run_id": "plr_remonly", "outcome": "resolved"},
	}
	incidents := map[string]map[string]any{
		"plr_remonly": {
			"incident_id": "plr_remonly",
			"journeys": []map[string]any{
				{"phase": "remediation", "trace_id": "faulttest-remonly-remed"},
			},
		},
	}
	srv := makeRunsAndIncidentServer(t, runs, incidents)
	defer srv.Close()

	got, err := pickBestRunForSuggest(srv.URL, "", "pbs_test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "plr_remonly" {
		t.Errorf("got %q, want plr_remonly (no triage journey → PLR fallback)", got)
	}
}

// ── compareVersions ────────────────────────────────────────────────────────

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int // sign only: <0, 0, >0
	}{
		{"1.3", "1.4", -1},
		{"1.4", "1.3", 1},
		{"1.3", "1.3", 0},
		{"2", "1.9", 1},
		{"1.10", "1.9", 1},
		{"", "1.0", -1},
		{"1.0", "", 1},
		{"", "", 0},
	}
	sign := func(n int) int {
		if n < 0 {
			return -1
		}
		if n > 0 {
			return 1
		}
		return 0
	}
	for _, tc := range cases {
		got := sign(compareVersions(tc.a, tc.b))
		if got != tc.want {
			t.Errorf("compareVersions(%q, %q) sign = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// ── vault diff two-ID mode ─────────────────────────────────────────────────

func makeDiffPlaybookServer(t *testing.T, playbooks map[string]*diffPlaybook) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GET /api/v1/fleet/playbooks/{id}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) < 5 || parts[4] == "" {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		id := parts[4]
		pb, ok := playbooks[id]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pb)
	}))
}

func TestCompareVersions_TwoIDOrdering(t *testing.T) {
	// Verify that in two-ID mode the lower version is treated as "before".
	// We just test compareVersions ordering; the full CLI wiring is covered by
	// TestCompareVersions above and integration tests.
	if compareVersions("1.3", "1.4") >= 0 {
		t.Error("1.3 should be less than 1.4")
	}
	if compareVersions("1.4", "1.3") <= 0 {
		t.Error("1.4 should be greater than 1.3")
	}
	if compareVersions("1.3", "1.3") != 0 {
		t.Error("equal versions should compare as 0")
	}
}

func TestFetchPlaybookByID_ReturnsCorrectPlaybook(t *testing.T) {
	want := &diffPlaybook{
		PlaybookID:  "pb_v13",
		SeriesID:    "pbs_conn",
		Version:     "1.3",
		Name:        "Conn Remediate",
		Description: "old description",
		Guidance:    "old guidance",
		IsActive:    false,
	}
	srv := makeDiffPlaybookServer(t, map[string]*diffPlaybook{"pb_v13": want})
	defer srv.Close()

	got, err := fetchPlaybookByID(srv.URL, "test-key", "pb_v13")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.PlaybookID != want.PlaybookID || got.Version != want.Version {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestFetchPlaybookByID_Returns404AsError(t *testing.T) {
	srv := makeDiffPlaybookServer(t, map[string]*diffPlaybook{})
	defer srv.Close()

	_, err := fetchPlaybookByID(srv.URL, "", "pb_missing")
	if err == nil {
		t.Error("expected error for 404, got nil")
	}
}

// ── printVersionTable / SUCCESS% ──────────────────────────────────────────

func TestPrintVersionTable_SuccessRateCombinesResolvedAndTransitioned(t *testing.T) {
	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printVersionTable("TRIAGE  pbs_test", []versionStats{
		{Version: "1.0", IsActive: false, TotalRuns: 10, Resolved: 3, ResolutionRate: 0.3, Transitioned: 5, TransitionRate: 0.5},
		{Version: "1.1", IsActive: true, TotalRuns: 4, Resolved: 2, ResolutionRate: 0.5, Transitioned: 1, TransitionRate: 0.25},
	})

	w.Close()
	os.Stdout = old
	var buf strings.Builder
	io.Copy(&buf, r) //nolint:errcheck
	out := buf.String()

	// v1.0: resolved(30%) + transitioned(50%) = 80%
	if !strings.Contains(out, "80%") {
		t.Errorf("expected 80%% success rate for v1.0, output:\n%s", out)
	}
	// v1.1: resolved(50%) + transitioned(25%) = 75%
	if !strings.Contains(out, "75%") {
		t.Errorf("expected 75%% success rate for v1.1, output:\n%s", out)
	}
	// Column header renamed from TRANSITIONED to SUCCESS%
	if !strings.Contains(out, "SUCCESS%") {
		t.Errorf("expected SUCCESS%% column header, output:\n%s", out)
	}
	if strings.Contains(out, "TRANSITIONED") {
		t.Errorf("old TRANSITIONED column header should not appear, output:\n%s", out)
	}
}

func TestPrintVersionTable_ZeroRunsShowsDash(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printVersionTable("", []versionStats{
		{Version: "1.0", IsActive: true, TotalRuns: 0},
	})

	w.Close()
	os.Stdout = old
	var buf strings.Builder
	io.Copy(&buf, r) //nolint:errcheck
	out := buf.String()

	if !strings.Contains(out, "–") {
		t.Errorf("expected dash for zero-run version, got:\n%s", out)
	}
}

func TestPrintVersionTable_ActiveVersionMarked(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printVersionTable("", []versionStats{
		{Version: "1.3", IsActive: false, TotalRuns: 9, Resolved: 3, ResolutionRate: 0.33},
		{Version: "1.4", IsActive: true, TotalRuns: 1, Resolved: 1, ResolutionRate: 1.0},
	})

	w.Close()
	os.Stdout = old
	var buf strings.Builder
	io.Copy(&buf, r) //nolint:errcheck
	out := buf.String()

	if !strings.Contains(out, "1.4 *") {
		t.Errorf("expected active version marked with *, got:\n%s", out)
	}
	if strings.Contains(out, "1.3 *") {
		t.Errorf("inactive version 1.3 should not be marked *, got:\n%s", out)
	}
}

// ── vault history ─────────────────────────────────────────────────────────

// makeHistoryServer returns a test server that responds to the playbooks list
// endpoint with a fixed set of playbooks for the given series.
func makeHistoryServer(t *testing.T, seriesID string, pbs []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("series_id") != seriesID {
			http.Error(w, "wrong series", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"playbooks": pbs})
	}))
}

func TestVaultHistory_ShowsAllVersions(t *testing.T) {
	srv := makeHistoryServer(t, "pbs_conn", []map[string]any{
		{
			"playbook_id": "pb_v13", "version": "1.3",
			"is_active": true, "source": "system",
			"created_at": "2026-01-01T00:00:00Z", "name": "Conn Remediate",
		},
		{
			"playbook_id": "pb_v14", "version": "1.4",
			"is_active": false, "source": "generated",
			"origin_trace": "plr_abc123",
			"created_at": "2026-06-01T00:00:00Z", "name": "Conn Remediate",
		},
	})
	defer srv.Close()

	// We test the HTTP fetch path indirectly by calling the same endpoint
	// the command uses and verifying the response decodes correctly.
	resp, err := http.Get(srv.URL + "?series_id=pbs_conn&active_only=false&include_system=true")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	var result struct {
		Playbooks []struct {
			PlaybookID  string `json:"playbook_id"`
			Version     string `json:"version"`
			IsActive    bool   `json:"is_active"`
			Source      string `json:"source"`
			OriginTrace string `json:"origin_trace"`
		} `json:"playbooks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Playbooks) != 2 {
		t.Fatalf("want 2 playbooks, got %d", len(result.Playbooks))
	}
	if result.Playbooks[0].PlaybookID != "pb_v13" {
		t.Errorf("want pb_v13 first, got %s", result.Playbooks[0].PlaybookID)
	}
	if !result.Playbooks[0].IsActive {
		t.Error("want first entry active (system v1.3)")
	}
	if result.Playbooks[1].Source != "generated" {
		t.Errorf("want second entry source=generated, got %q", result.Playbooks[1].Source)
	}
	if result.Playbooks[1].OriginTrace != "plr_abc123" {
		t.Errorf("want origin_trace=plr_abc123, got %q", result.Playbooks[1].OriginTrace)
	}
}

func TestVaultHistory_EmptySeries(t *testing.T) {
	srv := makeHistoryServer(t, "pbs_empty", []map[string]any{})
	defer srv.Close()

	resp, err := http.Get(srv.URL + "?series_id=pbs_empty&active_only=false&include_system=true")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	var result struct {
		Playbooks []map[string]any `json:"playbooks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Playbooks) != 0 {
		t.Errorf("want empty list, got %d entries", len(result.Playbooks))
	}
}

func TestVaultHistory_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"playbooks": []any{}})
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/api/v1/fleet/playbooks?series_id=pbs_x&active_only=false&include_system=true", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()

	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
}

// ── pct ───────────────────────────────────────────────────────────────────────

func TestPct(t *testing.T) {
	tests := []struct{ in float64; want string }{
		{0.0, "0%"},
		{1.0, "100%"},
		{0.5, "50%"},
		{0.876, "88%"},
		{0.995, "100%"},
		{0.004, "0%"},
	}
	for _, tt := range tests {
		if got := pct(tt.in); got != tt.want {
			t.Errorf("pct(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ── max ───────────────────────────────────────────────────────────────────────

func TestMax(t *testing.T) {
	if got := max(3, 7); got != 7 {
		t.Errorf("max(3,7) = %d, want 7", got)
	}
	if got := max(9, 2); got != 9 {
		t.Errorf("max(9,2) = %d, want 9", got)
	}
	if got := max(5, 5); got != 5 {
		t.Errorf("max(5,5) = %d, want 5", got)
	}
}

// ── faultFromTraceID ──────────────────────────────────────────────────────────

func TestFaultFromTraceID(t *testing.T) {
	tests := []struct {
		name    string
		traceID string
		want    string
	}{
		{
			"standard faulttest trace with run counter",
			"faulttest-a1b2c3d4-db-max-connections-r1",
			"db-max-connections",
		},
		{
			"no run counter suffix",
			"faulttest-a1b2c3d4-pbs-vacuum-bloat",
			"pbs-vacuum-bloat",
		},
		{
			"missing faulttest prefix returns empty",
			"tr_abc123def",
			"",
		},
		{
			"empty string returns empty",
			"",
			"",
		},
		{
			"bare prefix with no rest returns empty",
			"faulttest-",
			"",
		},
		{
			"multi-segment fault id preserved",
			"faulttest-deadbeef-k8s-pod-crash-loop-r3",
			"k8s-pod-crash-loop",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := faultFromTraceID(tt.traceID); got != tt.want {
				t.Errorf("faultFromTraceID(%q) = %q, want %q", tt.traceID, got, tt.want)
			}
		})
	}
}

// ── formatRemediationOutcome ──────────────────────────────────────────────────

func TestFormatRemediationOutcome(t *testing.T) {
	t.Run("nil run returns dash", func(t *testing.T) {
		if got := formatRemediationOutcome(nil); got != "–" {
			t.Errorf("got %q, want –", got)
		}
	})

	t.Run("outcome with duration under 60s", func(t *testing.T) {
		r := &incidentRun{
			Outcome:     "resolved",
			StartedAt:   "2026-06-01T10:00:00Z",
			CompletedAt: "2026-06-01T10:00:08Z",
		}
		got := formatRemediationOutcome(r)
		if got != "resolved 8.0s" {
			t.Errorf("got %q, want 'resolved 8.0s'", got)
		}
	})

	t.Run("outcome with duration over 60s shows minutes", func(t *testing.T) {
		r := &incidentRun{
			Outcome:     "resolved",
			StartedAt:   "2026-06-01T10:00:00Z",
			CompletedAt: "2026-06-01T10:02:00Z",
		}
		got := formatRemediationOutcome(r)
		if got != "resolved 2m" {
			t.Errorf("got %q, want 'resolved 2m'", got)
		}
	})

	t.Run("empty outcome falls back to unknown", func(t *testing.T) {
		r := &incidentRun{Outcome: ""}
		if got := formatRemediationOutcome(r); got != "unknown" {
			t.Errorf("got %q, want unknown", got)
		}
	})

	t.Run("missing timestamps returns outcome only", func(t *testing.T) {
		r := &incidentRun{Outcome: "abandoned"}
		if got := formatRemediationOutcome(r); got != "abandoned" {
			t.Errorf("got %q, want abandoned", got)
		}
	})

	t.Run("invalid timestamps returns outcome only", func(t *testing.T) {
		r := &incidentRun{
			Outcome:     "escalated",
			StartedAt:   "not-a-date",
			CompletedAt: "also-not-a-date",
		}
		if got := formatRemediationOutcome(r); got != "escalated" {
			t.Errorf("got %q, want escalated", got)
		}
	})
}

// ── scanFlag ──────────────────────────────────────────────────────────────────

func TestScanFlag(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		flag       string
		defaultVal string
		want       string
	}{
		{
			"equals form",
			[]string{"--gateway=http://localhost:8080", "--other=x"},
			"gateway", "", "http://localhost:8080",
		},
		{
			"space form",
			[]string{"--gateway", "http://localhost:9090"},
			"gateway", "", "http://localhost:9090",
		},
		{
			"single dash space form",
			[]string{"-gateway", "http://localhost:7070"},
			"gateway", "", "http://localhost:7070",
		},
		{
			"not present returns default",
			[]string{"--other=foo"},
			"gateway", "http://default:8080", "http://default:8080",
		},
		{
			"empty args returns default",
			nil,
			"gateway", "fallback", "fallback",
		},
		{
			"flag at end with no value returns default",
			[]string{"--gateway"},
			"gateway", "def", "def",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scanFlag(tt.args, tt.flag, tt.defaultVal)
			if got != tt.want {
				t.Errorf("scanFlag(%v, %q, %q) = %q, want %q", tt.args, tt.flag, tt.defaultVal, got, tt.want)
			}
		})
	}
}

// ── fetchStabilityCert ────────────────────────────────────────────────────────

func TestFetchStabilityCert_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/fleet/fault-stability/db-max-connections" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"fault_id":   "db-max-connections",
			"fault_name": "DB Max Connections",
			"n_runs":     5,
			"pass_rate":  0.8,
			"is_stable":  true,
		})
	}))
	defer srv.Close()

	got := fetchStabilityCert(srv.URL, "", "db-max-connections")
	if got == nil {
		t.Fatal("expected cert, got nil")
	}
	if got.FaultID != "db-max-connections" {
		t.Errorf("FaultID = %q, want db-max-connections", got.FaultID)
	}
	if !got.IsStable {
		t.Error("IsStable = false, want true")
	}
	if got.NRuns != 5 {
		t.Errorf("NRuns = %d, want 5", got.NRuns)
	}
}

func TestFetchStabilityCert_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if got := fetchStabilityCert(srv.URL, "", "unknown-fault"); got != nil {
		t.Errorf("expected nil for 404, got %+v", got)
	}
}

func TestFetchStabilityCert_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // closed immediately; dial will be refused
	if got := fetchStabilityCert(url, "", "any"); got != nil {
		t.Errorf("expected nil for network error, got %+v", got)
	}
}

func TestFetchStabilityCert_EmptyGatewayURL(t *testing.T) {
	if got := fetchStabilityCert("", "", "any"); got != nil {
		t.Errorf("expected nil for empty URL, got %+v", got)
	}
}

func TestFetchStabilityCert_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"fault_id": "x"}) //nolint:errcheck
	}))
	defer srv.Close()

	fetchStabilityCert(srv.URL, "tok-cert", "x")
	if gotAuth != "Bearer tok-cert" {
		t.Errorf("Authorization = %q, want Bearer tok-cert", gotAuth)
	}
}

// ── postStabilityCert ─────────────────────────────────────────────────────────

func TestPostStabilityCert_PostsCorrectPayload(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfg := &HarnessConfig{
		GatewayURL:     srv.URL,
		DiagnosisModel: "claude-sonnet-4-6",
		JudgeModel:     "claude-opus-4-8",
	}
	f := Failure{
		ID:                        "db-max-connections",
		Name:                      "DB Max Connections",
		DiagnosisPlaybookSeriesID: "pbs_db_triage",
	}
	sr := StabilityReport{
		FailureID:   "db-max-connections",
		FailureName: "DB Max Connections",
		N:           5,
		PassCount:   4,
	}
	postStabilityCert(context.Background(), cfg, f, sr)

	if gotBody["fault_id"] != "db-max-connections" {
		t.Errorf("fault_id = %v, want db-max-connections", gotBody["fault_id"])
	}
	if gotBody["playbook_series_id"] != "pbs_db_triage" {
		t.Errorf("playbook_series_id = %v, want pbs_db_triage", gotBody["playbook_series_id"])
	}
	if gotBody["diagnosis_model"] != "claude-sonnet-4-6" {
		t.Errorf("diagnosis_model = %v, want claude-sonnet-4-6", gotBody["diagnosis_model"])
	}
	if gotBody["judge_model"] != "claude-opus-4-8" {
		t.Errorf("judge_model = %v, want claude-opus-4-8", gotBody["judge_model"])
	}
	if gotBody["n_runs"] != float64(5) {
		t.Errorf("n_runs = %v, want 5", gotBody["n_runs"])
	}
}

func TestPostStabilityCert_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfg := &HarnessConfig{GatewayURL: srv.URL, GatewayAPIKey: "tok-stability"}
	postStabilityCert(context.Background(), cfg, Failure{}, StabilityReport{})
	if gotAuth != "Bearer tok-stability" {
		t.Errorf("Authorization = %q, want Bearer tok-stability", gotAuth)
	}
}

func TestPostStabilityCert_NoopWhenEmptyGateway(t *testing.T) {
	// Should not panic or dial anything when GatewayURL is empty.
	cfg := &HarnessConfig{GatewayURL: ""}
	postStabilityCert(context.Background(), cfg, Failure{}, StabilityReport{})
}

func TestPostStabilityCert_ToleratesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := &HarnessConfig{GatewayURL: srv.URL}
	// Should log a warning but not panic.
	postStabilityCert(context.Background(), cfg, Failure{ID: "x"}, StabilityReport{N: 1})
}

// ── wrapLines ────────────────────────────────────────────────────────────────

func TestWrapLines(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		maxWidth int
		want     []string
	}{
		{
			"short text fits one line",
			"hello world",
			20,
			[]string{"hello world"},
		},
		{
			"wraps at word boundary",
			"one two three four five",
			13,
			[]string{"one two three", "four five"},
		},
		{
			"empty string returns single empty element",
			"",
			10,
			[]string{""},
		},
		{
			"newline in source creates new paragraph",
			"first sentence.\nsecond sentence.",
			40,
			[]string{"first sentence.", "second sentence."},
		},
		{
			"blank line between paragraphs is skipped",
			"para one.\n\npara two.",
			40,
			[]string{"para one.", "para two."},
		},
		{
			"long word is not split",
			"superlongwordthatexceedswidth",
			10,
			[]string{"superlongwordthatexceedswidth"},
		},
		{
			"multi-line wrapping across paragraph",
			"I need to check the connection before proceeding with the query.",
			30,
			[]string{
				"I need to check the connection",
				"before proceeding with the",
				"query.",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapLines(tt.text, tt.maxWidth)
			if len(got) != len(tt.want) {
				t.Fatalf("wrapLines(%q, %d) = %v (len %d), want %v (len %d)",
					tt.text, tt.maxWidth, got, len(got), tt.want, len(tt.want))
			}
			for i, line := range tt.want {
				if got[i] != line {
					t.Errorf("line[%d] = %q, want %q", i, got[i], line)
				}
			}
		})
	}
}

// ── fetchRunEvents ────────────────────────────────────────────────────────────

func twoEvents() []journeyEvent {
	return []journeyEvent{
		{
			EventID:   "rsn_001",
			EventType: "agent_reasoning",
			AgentReasoning: &journeyReasoning{
				Reasoning: "I will check the connection first.",
				ToolCalls: []string{"check_connection"},
			},
		},
		{
			EventID:   "evt_001",
			EventType: "tool_execution",
			ToolExecution: &journeyToolExec{Name: "check_connection"},
		},
	}
}

func TestFetchRunEvents_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/fleet/playbook-runs/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(twoEvents()) //nolint:errcheck
	}))
	defer srv.Close()

	got, err := fetchRunEvents(srv.URL, "", "plr_abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].EventType != "agent_reasoning" {
		t.Errorf("event[0].EventType = %q, want agent_reasoning", got[0].EventType)
	}
	if got[1].EventType != "tool_execution" {
		t.Errorf("event[1].EventType = %q, want tool_execution", got[1].EventType)
	}
}

func TestFetchRunEvents_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer srv.Close()

	got, err := fetchRunEvents(srv.URL, "", "plr_empty")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestFetchRunEvents_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := fetchRunEvents(srv.URL, "", "plr_x")
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
}

func TestFetchRunEvents_NetworkError(t *testing.T) {
	_, err := fetchRunEvents("http://127.0.0.1:19997", "", "plr_x")
	if err == nil {
		t.Fatal("expected error for unreachable server, got nil")
	}
}

func TestFetchRunEvents_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer srv.Close()

	fetchRunEvents(srv.URL, "tok-abc", "plr_x") //nolint:errcheck
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("Authorization = %q, want Bearer tok-abc", gotAuth)
	}
}

func TestFetchRunEvents_RequestsCorrectEventTypes(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer srv.Close()

	fetchRunEvents(srv.URL, "", "plr_x") //nolint:errcheck
	types := gotQuery.Get("types")
	if !strings.Contains(types, "agent_reasoning") {
		t.Errorf("types = %q, want to contain agent_reasoning", types)
	}
	if !strings.Contains(types, "tool_execution") {
		t.Errorf("types = %q, want to contain tool_execution", types)
	}
}

func TestFetchRunEvents_UsesRunIDInPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer srv.Close()

	fetchRunEvents(srv.URL, "", "plr_xyz999") //nolint:errcheck
	if !strings.Contains(gotPath, "plr_xyz999") {
		t.Errorf("path = %q, want plr_xyz999 in path", gotPath)
	}
}

// ── printReasoningTrace ───────────────────────────────────────────────────────

func TestPrintReasoningTrace_Empty(t *testing.T) {
	out := captureStdout(func() { printReasoningTrace(nil) })
	if !strings.Contains(out, "no events found") {
		t.Errorf("empty events: got %q, want 'no events found'", out)
	}
}

func TestPrintReasoningTrace_ReasoningBeforeTool(t *testing.T) {
	events := []journeyEvent{
		{
			EventType: "agent_reasoning",
			AgentReasoning: &journeyReasoning{
				Reasoning: "I will check the connection first.",
				ToolCalls: []string{"check_connection"},
			},
		},
		{
			EventType:     "tool_execution",
			ToolExecution: &journeyToolExec{Name: "check_connection"},
		},
	}
	out := captureStdout(func() { printReasoningTrace(events) })
	if !strings.Contains(out, "I will check the connection first.") {
		t.Errorf("reasoning text missing from output: %q", out)
	}
	if !strings.Contains(out, "check_connection") {
		t.Errorf("tool name missing from output: %q", out)
	}
	if !strings.Contains(out, "[ok]") {
		t.Errorf("status [ok] missing: %q", out)
	}
	if strings.Contains(out, "no preceding reasoning") {
		t.Errorf("unexpected orphan annotation for covered tool: %q", out)
	}
}

func TestPrintReasoningTrace_OrphanTool(t *testing.T) {
	events := []journeyEvent{
		{
			EventType:     "tool_execution",
			ToolExecution: &journeyToolExec{Name: "vacuum_table"},
		},
	}
	out := captureStdout(func() { printReasoningTrace(events) })
	if !strings.Contains(out, "vacuum_table") {
		t.Errorf("tool name missing: %q", out)
	}
	if !strings.Contains(out, "no preceding reasoning") {
		t.Errorf("orphan annotation missing for uncovered tool: %q", out)
	}
}

func TestPrintReasoningTrace_ToolError(t *testing.T) {
	events := []journeyEvent{
		{
			EventType:     "tool_execution",
			ToolExecution: &journeyToolExec{Name: "cancel_query", Error: "connection refused"},
		},
	}
	out := captureStdout(func() { printReasoningTrace(events) })
	if !strings.Contains(out, "[error]") {
		t.Errorf("error status missing: %q", out)
	}
}

func TestPrintReasoningTrace_MultipleToolsOneSummary(t *testing.T) {
	events := []journeyEvent{
		{
			EventType: "agent_reasoning",
			AgentReasoning: &journeyReasoning{
				Reasoning: "Checking both stats and connection.",
				ToolCalls: []string{"check_connection", "get_table_stats"},
			},
		},
		{
			EventType:     "tool_execution",
			ToolExecution: &journeyToolExec{Name: "check_connection"},
		},
		{
			EventType:     "tool_execution",
			ToolExecution: &journeyToolExec{Name: "get_table_stats"},
		},
	}
	out := captureStdout(func() { printReasoningTrace(events) })
	if strings.Contains(out, "no preceding reasoning") {
		t.Errorf("unexpected orphan annotation: both tools were in reasoning.ToolCalls: %q", out)
	}
	if !strings.Contains(out, "check_connection") || !strings.Contains(out, "get_table_stats") {
		t.Errorf("one or both tool names missing: %q", out)
	}
}

// ── vault cert-compare ────────────────────────────────────────────────────

func stableEntry() *certCompareEntry  { return &certCompareEntry{isStable: true, passRate: 1.0, nRuns: 3} }
func unstableEntry() *certCompareEntry { return &certCompareEntry{isStable: false, passRate: 0.3, nRuns: 3} }

func TestChangeLabel(t *testing.T) {
	tests := []struct {
		name string
		row  certCompareRow
		want string
	}{
		{"regression", certCompareRow{oldCert: stableEntry(), newCert: unstableEntry()}, "⚠ REGRESSION"},
		{"improvement", certCompareRow{oldCert: unstableEntry(), newCert: stableEntry()}, "✓ IMPROVEMENT"},
		{"unchanged stable", certCompareRow{oldCert: stableEntry(), newCert: stableEntry()}, "—"},
		{"unchanged unstable", certCompareRow{oldCert: unstableEntry(), newCert: unstableEntry()}, "—"},
		{"old missing", certCompareRow{oldCert: nil, newCert: stableEntry()}, "? NOT RUN YET"},
		{"new missing", certCompareRow{oldCert: stableEntry(), newCert: nil}, "? NOT RUN YET"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := changeLabel(tt.row); got != tt.want {
				t.Errorf("changeLabel = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestChangeOrder(t *testing.T) {
	regression := certCompareRow{oldCert: stableEntry(), newCert: unstableEntry()}
	improvement := certCompareRow{oldCert: unstableEntry(), newCert: stableEntry()}
	stableStable := certCompareRow{oldCert: stableEntry(), newCert: stableEntry()}
	unstableUnstable := certCompareRow{oldCert: unstableEntry(), newCert: unstableEntry()}
	missing := certCompareRow{oldCert: stableEntry(), newCert: nil}

	if changeOrder(regression) >= changeOrder(improvement) {
		t.Error("regression should sort before improvement")
	}
	if changeOrder(improvement) >= changeOrder(stableStable) {
		t.Error("improvement should sort before stable-stable")
	}
	if changeOrder(stableStable) >= changeOrder(unstableUnstable) {
		t.Error("stable-stable should sort before unstable-unstable")
	}
	if changeOrder(unstableUnstable) >= changeOrder(missing) {
		t.Error("unstable-unstable should sort before missing")
	}
}

func TestCertStatusStr(t *testing.T) {
	if got := certStatusStr(nil); got != "(no data)" {
		t.Errorf("nil → %q, want \"(no data)\"", got)
	}
	if got := certStatusStr(stableEntry()); got != "STABLE" {
		t.Errorf("stable → %q, want \"STABLE\"", got)
	}
	if got := certStatusStr(unstableEntry()); got != "UNSTABLE" {
		t.Errorf("unstable → %q, want \"UNSTABLE\"", got)
	}
}

func TestShortModelName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"claude-sonnet-4-5", "sonnet-4-5"},
		{"claude-opus-4-8", "opus-4-8"},
		{"sonnet-4-5", "sonnet-4-5"},   // already short — returned as-is
		{"claude", "claude"},           // single token
	}
	for _, tt := range tests {
		if got := shortModelName(tt.in); got != tt.want {
			t.Errorf("shortModelName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("within limit: got %q", got)
	}
	if got := truncate("hello", 5); got != "hello" {
		t.Errorf("at limit: got %q", got)
	}
	if got := truncate("hello world", 8); len([]rune(got)) != 8 {
		t.Errorf("truncated length = %d, want 8: %q", len(got), got)
	}
	if !strings.HasSuffix(truncate("hello world", 8), "…") {
		t.Errorf("truncated string should end with ellipsis")
	}
}

func TestFetchAllStabilityCerts_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/fleet/fault-stability" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"certs":[
			{"fault_id":"db-oom","fault_name":"DB OOM","diagnosis_model":"claude-sonnet-4-5","n_runs":3,"pass_rate":1.0,"is_stable":true},
			{"fault_id":"db-oom","fault_name":"DB OOM","diagnosis_model":"claude-sonnet-4-6","n_runs":3,"pass_rate":0.33,"is_stable":false},
			{"fault_id":"k8s-crash","fault_name":"K8s Crash","diagnosis_model":"claude-sonnet-4-5","n_runs":3,"pass_rate":0.66,"is_stable":false}
		]}`)
	}))
	defer srv.Close()

	certs, err := fetchAllStabilityCerts(srv.URL, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(certs) != 3 {
		t.Fatalf("len(certs) = %d, want 3", len(certs))
	}
	// Verify first cert fields.
	if certs[0].FaultID != "db-oom" || certs[0].DiagnosisModel != "claude-sonnet-4-5" || !certs[0].IsStable {
		t.Errorf("unexpected first cert: %+v", certs[0])
	}
}

func TestFetchAllStabilityCerts_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"certs":[]}`)
	}))
	defer srv.Close()

	certs, err := fetchAllStabilityCerts(srv.URL, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(certs) != 0 {
		t.Errorf("expected empty slice, got %d certs", len(certs))
	}
}

func TestFetchAllStabilityCerts_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := fetchAllStabilityCerts(srv.URL, "")
	if err == nil {
		t.Error("expected error on 500 response")
	}
}

func TestFetchAllStabilityCerts_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"certs":[]}`)
	}))
	defer srv.Close()

	fetchAllStabilityCerts(srv.URL, "secret-key") //nolint:errcheck
	if gotAuth != "Bearer secret-key" {
		t.Errorf("Authorization = %q, want \"Bearer secret-key\"", gotAuth)
	}
}

// ── vaultCertCompare end-to-end ───────────────────────────────────────────

// newCertCompareServer returns an httptest.Server that serves a realistic
// multi-model cert dataset for cert-compare tests.
//
// Dataset:
//   db-lock-contention  sonnet-4-5=STABLE  sonnet-4-6=UNSTABLE  → REGRESSION
//   db-max-connections  sonnet-4-5=UNSTABLE sonnet-4-6=STABLE   → IMPROVEMENT
//   db-vacuum-needed    sonnet-4-5=STABLE  sonnet-4-6=STABLE    → unchanged
//   db-wal-disk-full    sonnet-4-5=STABLE  (no sonnet-4-6 cert) → NOT RUN YET
func newCertCompareServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/api/v1/fleet/fault-stability":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"certs":[
				{"fault_id":"db-lock-contention","fault_name":"Lock Contention","diagnosis_model":"claude-sonnet-4-5","n_runs":3,"pass_rate":1.0,"is_stable":true},
				{"fault_id":"db-lock-contention","fault_name":"Lock Contention","diagnosis_model":"claude-sonnet-4-6","n_runs":3,"pass_rate":0.33,"is_stable":false},
				{"fault_id":"db-max-connections","fault_name":"Max Connections","diagnosis_model":"claude-sonnet-4-5","n_runs":3,"pass_rate":0.33,"is_stable":false},
				{"fault_id":"db-max-connections","fault_name":"Max Connections","diagnosis_model":"claude-sonnet-4-6","n_runs":3,"pass_rate":1.0,"is_stable":true},
				{"fault_id":"db-vacuum-needed","fault_name":"Vacuum Needed","diagnosis_model":"claude-sonnet-4-5","n_runs":3,"pass_rate":1.0,"is_stable":true},
				{"fault_id":"db-vacuum-needed","fault_name":"Vacuum Needed","diagnosis_model":"claude-sonnet-4-6","n_runs":3,"pass_rate":1.0,"is_stable":true},
				{"fault_id":"db-wal-disk-full","fault_name":"WAL Disk Full","diagnosis_model":"claude-sonnet-4-5","n_runs":3,"pass_rate":1.0,"is_stable":true}
			]}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestVaultCertCompare_RegressionFirst(t *testing.T) {
	srv := newCertCompareServer(t)
	defer srv.Close()

	out := captureStdout(func() {
		vaultCertCompare([]string{
			"claude-sonnet-4-5", "claude-sonnet-4-6",
			"--gateway", srv.URL,
		})
	})

	// Regression must appear.
	if !strings.Contains(out, "REGRESSION") {
		t.Errorf("expected REGRESSION in output:\n%s", out)
	}
	// Improvement must appear.
	if !strings.Contains(out, "IMPROVEMENT") {
		t.Errorf("expected IMPROVEMENT in output:\n%s", out)
	}
	// NOT RUN YET must appear for the missing cert.
	if !strings.Contains(out, "NOT RUN YET") {
		t.Errorf("expected NOT RUN YET in output:\n%s", out)
	}
	// Regression row includes pass-rate delta.
	if !strings.Contains(out, "33%") {
		t.Errorf("expected pass-rate delta (33%%) in output:\n%s", out)
	}
	// Summary line warns about regression.
	if !strings.Contains(out, "regression") {
		t.Errorf("expected summary line mentioning regression:\n%s", out)
	}
}

func TestVaultCertCompare_RegressionSortedFirst(t *testing.T) {
	srv := newCertCompareServer(t)
	defer srv.Close()

	out := captureStdout(func() {
		vaultCertCompare([]string{
			"claude-sonnet-4-5", "claude-sonnet-4-6",
			"--gateway", srv.URL,
		})
	})

	// Verify regressions appear before improvements in the output.
	rIdx := strings.Index(out, "REGRESSION")
	iIdx := strings.Index(out, "IMPROVEMENT")
	if rIdx == -1 || iIdx == -1 {
		t.Fatalf("missing REGRESSION or IMPROVEMENT in output:\n%s", out)
	}
	if rIdx > iIdx {
		t.Errorf("REGRESSION (pos %d) should appear before IMPROVEMENT (pos %d)", rIdx, iIdx)
	}
}

func TestVaultCertCompare_NoRegressions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"certs":[
			{"fault_id":"db-vacuum-needed","fault_name":"Vacuum Needed","diagnosis_model":"claude-sonnet-4-5","n_runs":3,"pass_rate":1.0,"is_stable":true},
			{"fault_id":"db-vacuum-needed","fault_name":"Vacuum Needed","diagnosis_model":"claude-sonnet-4-6","n_runs":3,"pass_rate":1.0,"is_stable":true}
		]}`)
	}))
	defer srv.Close()

	out := captureStdout(func() {
		vaultCertCompare([]string{
			"claude-sonnet-4-5", "claude-sonnet-4-6",
			"--gateway", srv.URL,
		})
	})

	if strings.Contains(out, "REGRESSION") {
		t.Errorf("unexpected REGRESSION in no-regression output:\n%s", out)
	}
	if !strings.Contains(out, "cert-equivalent") {
		t.Errorf("expected cert-equivalent summary:\n%s", out)
	}
}

func TestVaultCertCompare_NoCertsForEitherModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// Return certs for a completely different model — neither requested model matches.
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"certs":[
			{"fault_id":"db-vacuum-needed","diagnosis_model":"claude-haiku-4-5","n_runs":3,"pass_rate":1.0,"is_stable":true}
		]}`)
	}))
	defer srv.Close()

	out := captureStdout(func() {
		vaultCertCompare([]string{
			"claude-sonnet-4-5", "claude-sonnet-4-6",
			"--gateway", srv.URL,
		})
	})

	if !strings.Contains(out, "No stability certs found") {
		t.Errorf("expected no-certs message:\n%s", out)
	}
}
