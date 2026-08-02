package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// mockIncidentAuditd builds a mock auditd that serves the minimum set of
// endpoints needed by handleGetIncident. Each field controls the response for
// one sub-request; nil means "return 404 / empty".
type mockIncidentAuditd struct {
	triageRun    *audit.PlaybookRun
	feedbackRecs []audit.RunFeedback
	evaluation   *audit.RunEvaluation
	// nextRunByPriorID maps a run's RunID to the run that recorded it as
	// PriorRunID — i.e. what GET .../playbook-runs?prior_run_id=<key> should
	// return. Absent key ⇒ empty runs array (no successor for that hop).
	nextRunByPriorID map[string]*audit.PlaybookRun
}

func (m *mockIncidentAuditd) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")

		switch {
		// Triage run lookup: exact path, no trailing segments.
		case strings.HasSuffix(path, "/playbook-runs/"+m.triageRunID()) &&
			r.URL.Query().Get("prior_run_id") == "" &&
			!strings.Contains(path, "/feedback") &&
			!strings.Contains(path, "/evaluation") &&
			!strings.Contains(path, "/steps"):
			if m.triageRun == nil {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(m.triageRun) //nolint:errcheck

		// Next-hop lookup: prior_run_id query param. Walked repeatedly by
		// fetchEscalationHops, once per hop in the chain.
		case strings.Contains(path, "/playbook-runs") && r.URL.Query().Get("prior_run_id") != "":
			priorID := r.URL.Query().Get("prior_run_id")
			runs := []any{}
			if next, ok := m.nextRunByPriorID[priorID]; ok {
				runs = []any{next}
			}
			json.NewEncoder(w).Encode(map[string]any{"runs": runs}) //nolint:errcheck

		// Gate event lookup.
		case strings.Contains(path, "/events"):
			json.NewEncoder(w).Encode([]any{}) //nolint:errcheck

		// Feedback — always return array envelope even when empty.
		case strings.HasSuffix(path, "/feedback"):
			recs := m.feedbackRecs
			if recs == nil {
				recs = []audit.RunFeedback{}
			}
			json.NewEncoder(w).Encode(map[string]any{"feedback": recs}) //nolint:errcheck

		// Evaluation.
		case strings.HasSuffix(path, "/evaluation"):
			if m.evaluation == nil {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(m.evaluation) //nolint:errcheck

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (m *mockIncidentAuditd) triageRunID() string {
	if m.triageRun != nil {
		return m.triageRun.RunID
	}
	return "plr_missing"
}

// getIncident sends GET /api/v1/incidents/{runID} through the gateway mux.
func getIncident(t *testing.T, gw *Gateway, runID string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents/"+runID, nil)
	req.SetPathValue("runID", runID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestHandleGetIncident_BasicNarrative verifies that a simple triage-only run
// populates the top-level and Triage chapter fields correctly.
func TestHandleGetIncident_BasicNarrative(t *testing.T) {
	run := &audit.PlaybookRun{
		RunID:           "plr_narr01",
		SeriesID:        "pbs_db_lock",
		FindingsSummary: "Lock chain detected on pg_locks",
		Outcome:         audit.OutcomeResolved,
		Operator:        "alice",
		StartedAt:       time.Now().UTC(),
	}
	mock := &mockIncidentAuditd{triageRun: run}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	rec := getIncident(t, gw, "plr_narr01")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var n IncidentNarrative
	if err := json.NewDecoder(rec.Body).Decode(&n); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if n.IncidentID != "plr_narr01" {
		t.Errorf("IncidentID = %q, want plr_narr01", n.IncidentID)
	}
	if n.Operator != "alice" {
		t.Errorf("Operator = %q, want alice", n.Operator)
	}
	if n.Triage.RunID != "plr_narr01" {
		t.Errorf("Triage.RunID = %q, want plr_narr01", n.Triage.RunID)
	}
	if n.Triage.Playbook != "pbs_db_lock" {
		t.Errorf("Triage.Playbook = %q, want pbs_db_lock", n.Triage.Playbook)
	}
	if n.Triage.Findings != "Lock chain detected on pg_locks" {
		t.Errorf("Triage.Findings = %q", n.Triage.Findings)
	}
	if n.Gate != nil {
		t.Errorf("Gate should be nil for non-gated run, got %+v", n.Gate)
	}
	if n.Remediation != nil {
		t.Errorf("Remediation should be nil, got %+v", n.Remediation)
	}
}

// TestHandleGetIncident_FeedbackSlice verifies that the handler returns multiple
// feedback records as a slice. This was the v0.18 fix: the old code tried to
// decode the {"feedback":[...]} envelope as a singular object, silently returning nil.
func TestHandleGetIncident_FeedbackSlice(t *testing.T) {
	tr := true
	fa := false
	run := &audit.PlaybookRun{
		RunID:     "plr_narr02",
		SeriesID:  "pbs_db_lock",
		Outcome:   audit.OutcomeResolved,
		StartedAt: time.Now().UTC(),
	}
	mock := &mockIncidentAuditd{
		triageRun: run,
		feedbackRecs: []audit.RunFeedback{
			{RunID: "plr_narr02", FeedbackType: "triage", FeedbackTime: "at_gate", VerdictCorrect: &tr},
			{RunID: "plr_narr02", FeedbackType: "triage", FeedbackTime: "post_incident", VerdictCorrect: &fa},
		},
	}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	rec := getIncident(t, gw, "plr_narr02")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}

	var n IncidentNarrative
	if err := json.NewDecoder(rec.Body).Decode(&n); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(n.Feedback) != 2 {
		t.Fatalf("Feedback len = %d, want 2 (both triage/at_gate and triage/post_incident)", len(n.Feedback))
	}
	if n.Feedback[0].FeedbackTime != "at_gate" {
		t.Errorf("Feedback[0].FeedbackTime = %q, want at_gate", n.Feedback[0].FeedbackTime)
	}
	if n.Feedback[1].FeedbackTime != "post_incident" {
		t.Errorf("Feedback[1].FeedbackTime = %q, want post_incident", n.Feedback[1].FeedbackTime)
	}
}

// TestHandleGetIncident_EvaluationChapter verifies that the eval chapter (added in v0.18)
// is populated when auditd has a run_evaluation record, including primary_confidence.
func TestHandleGetIncident_EvaluationChapter(t *testing.T) {
	run := &audit.PlaybookRun{
		RunID:     "plr_narr03",
		SeriesID:  "pbs_db_lock",
		Outcome:   audit.OutcomeResolved,
		StartedAt: time.Now().UTC(),
	}
	mock := &mockIncidentAuditd{
		triageRun: run,
		evaluation: &audit.RunEvaluation{
			RunID:             "plr_narr03",
			FailureID:         "db-lock-contention",
			DiagnosisScore:    0.91,
			PrimaryConfidence: 0.88,
			JudgeUsed:         true,
		},
	}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	rec := getIncident(t, gw, "plr_narr03")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}

	var n IncidentNarrative
	if err := json.NewDecoder(rec.Body).Decode(&n); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if n.Evaluation == nil {
		t.Fatal("Evaluation is nil, want non-nil evaluation chapter")
	}
	if n.Evaluation.DiagnosisScore != 0.91 {
		t.Errorf("Evaluation.DiagnosisScore = %v, want 0.91", n.Evaluation.DiagnosisScore)
	}
	if n.Evaluation.PrimaryConfidence != 0.88 {
		t.Errorf("Evaluation.PrimaryConfidence = %v, want 0.88", n.Evaluation.PrimaryConfidence)
	}
	if !n.Evaluation.JudgeUsed {
		t.Errorf("Evaluation.JudgeUsed = false, want true")
	}
}

// TestHandleGetIncident_NotFound verifies that a missing triage run returns 404.
func TestHandleGetIncident_NotFound(t *testing.T) {
	mock := &mockIncidentAuditd{triageRun: nil}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	rec := getIncident(t, gw, "plr_ghost")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestHandleGetIncident_EscalationOnly verifies that a successor run reached
// via ESCALATE_TO (not TRANSITION_TO) is classified as an escalation hop, not
// remediation — the exact case that was silently mislabeled before this fix,
// even at just 2 hops.
func TestHandleGetIncident_EscalationOnly(t *testing.T) {
	triage := &audit.PlaybookRun{
		RunID:       "plr_esc01",
		SeriesID:    "pbs_connection_triage",
		Outcome:     audit.OutcomeEscalated,
		EscalatedTo: "pbs_sysadmin_docker_inspect",
		Operator:    "alice",
		TraceID:     "trace-triage",
		StartedAt:   time.Now().UTC(),
	}
	hop := &audit.PlaybookRun{
		RunID:      "plr_esc02",
		SeriesID:   "pbs_sysadmin_docker_inspect",
		Outcome:    audit.OutcomeEscalated,
		PriorRunID: "plr_esc01",
		TraceID:    "trace-hop",
		StartedAt:  time.Now().UTC(),
	}
	mock := &mockIncidentAuditd{
		triageRun:        triage,
		nextRunByPriorID: map[string]*audit.PlaybookRun{"plr_esc01": hop},
	}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	rec := getIncident(t, gw, "plr_esc01")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}

	var n IncidentNarrative
	if err := json.NewDecoder(rec.Body).Decode(&n); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if n.Remediation != nil {
		t.Errorf("Remediation should be nil (successor was ESCALATE_TO, not TRANSITION_TO), got %+v", n.Remediation)
	}
	if len(n.Escalations) != 1 {
		t.Fatalf("Escalations len = %d, want 1", len(n.Escalations))
	}
	if n.Escalations[0].RunID != "plr_esc02" {
		t.Errorf("Escalations[0].RunID = %q, want plr_esc02", n.Escalations[0].RunID)
	}
	if len(n.Journeys) != 2 {
		t.Fatalf("Journeys len = %d, want 2; got %v", len(n.Journeys), n.Journeys)
	}
	if n.Journeys[1].Phase != "escalation:1" {
		t.Errorf("Journeys[1].Phase = %q, want escalation:1", n.Journeys[1].Phase)
	}
}

// TestHandleGetIncident_ThreeHopEscalation verifies a full 3-hop chain —
// triage escalates once, then transitions into remediation — is walked and
// classified completely: the middle hop lands in Escalations, the terminal
// (transitioned-into) hop becomes Remediation, and Journeys reflects all
// three phases in order.
func TestHandleGetIncident_ThreeHopEscalation(t *testing.T) {
	triage := &audit.PlaybookRun{
		RunID:       "plr_t1",
		SeriesID:    "pbs_connection_triage",
		Outcome:     audit.OutcomeEscalated,
		EscalatedTo: "pbs_sysadmin_docker_inspect",
		TraceID:     "trace-t1",
		StartedAt:   time.Now().Add(-3 * time.Minute).UTC(),
	}
	escHop := &audit.PlaybookRun{
		RunID:          "plr_e1",
		SeriesID:       "pbs_sysadmin_docker_inspect",
		Outcome:        audit.OutcomeTransitioned,
		TransitionedTo: "pbs_k8s_pod_crash_remediate",
		PriorRunID:     "plr_t1",
		TraceID:        "trace-e1",
		StartedAt:      time.Now().Add(-2 * time.Minute).UTC(),
	}
	remHop := &audit.PlaybookRun{
		RunID:       "plr_r1",
		SeriesID:    "pbs_k8s_pod_crash_remediate",
		Outcome:     audit.OutcomeResolved,
		PriorRunID:  "plr_e1",
		TraceID:     "trace-r1",
		StartedAt:   time.Now().Add(-1 * time.Minute).UTC(),
		CompletedAt: time.Now().UTC(),
	}
	mock := &mockIncidentAuditd{
		triageRun: triage,
		nextRunByPriorID: map[string]*audit.PlaybookRun{
			"plr_t1": escHop,
			"plr_e1": remHop,
		},
	}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	rec := getIncident(t, gw, "plr_t1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}

	var n IncidentNarrative
	if err := json.NewDecoder(rec.Body).Decode(&n); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(n.Escalations) != 1 {
		t.Fatalf("Escalations len = %d, want 1", len(n.Escalations))
	}
	if n.Escalations[0].RunID != "plr_e1" {
		t.Errorf("Escalations[0].RunID = %q, want plr_e1", n.Escalations[0].RunID)
	}
	if n.Remediation == nil {
		t.Fatal("Remediation should be populated (chain reached a TRANSITION_TO)")
	}
	if n.Remediation.RunID != "plr_r1" {
		t.Errorf("Remediation.RunID = %q, want plr_r1 (the terminal, transitioned-into hop)", n.Remediation.RunID)
	}
	if len(n.Journeys) != 3 {
		t.Fatalf("Journeys len = %d, want 3; got %v", len(n.Journeys), n.Journeys)
	}
	wantPhases := []string{"triage", "escalation:1", "remediation"}
	for i, want := range wantPhases {
		if n.Journeys[i].Phase != want {
			t.Errorf("Journeys[%d].Phase = %q, want %q", i, n.Journeys[i].Phase, want)
		}
	}
	if n.DurationSec <= 0 {
		t.Errorf("DurationSec should be computed from the terminal hop, got %v", n.DurationSec)
	}
}

// TestFetchEscalationHops_CycleGuard verifies that a cyclic prior_run_id
// chain (a data anomaly that should never happen in practice) doesn't hang
// the walk — it stops and returns what was collected before the cycle.
func TestFetchEscalationHops_CycleGuard(t *testing.T) {
	a := &audit.PlaybookRun{RunID: "plr_a", SeriesID: "pbs_a", Outcome: audit.OutcomeEscalated, StartedAt: time.Now().UTC()}
	b := &audit.PlaybookRun{RunID: "plr_b", SeriesID: "pbs_b", Outcome: audit.OutcomeEscalated, PriorRunID: "plr_a", StartedAt: time.Now().UTC()}
	// plr_b's "successor" is plr_a again — a cycle.
	mock := &mockIncidentAuditd{
		triageRun: a,
		nextRunByPriorID: map[string]*audit.PlaybookRun{
			"plr_a": b,
			"plr_b": a,
		},
	}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	done := make(chan []*audit.PlaybookRun, 1)
	go func() {
		done <- gw.fetchEscalationHops(context.Background(), "plr_a")
	}()

	select {
	case hops := <-done:
		if len(hops) != 1 || hops[0].RunID != "plr_b" {
			t.Errorf("hops = %v, want exactly [plr_b] (stopped before the cycle repeats)", hops)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fetchEscalationHops did not return — cycle guard failed to stop the walk")
	}
}

// TestBuildJourneyRefs directly exercises the phase-labeling/merge logic
// without going through HTTP, covering 0/1/N-hop cases and the trace-ID merge.
func TestBuildJourneyRefs(t *testing.T) {
	run := func(runID, traceID, transitionedTo string) *audit.PlaybookRun {
		return &audit.PlaybookRun{RunID: runID, TraceID: traceID, TransitionedTo: transitionedTo}
	}

	tests := []struct {
		name string
		run  *audit.PlaybookRun
		hops []*audit.PlaybookRun
		want []audit.IncidentJourneyRef
	}{
		{
			name: "no hops, no trace",
			run:  run("t", "", ""),
			hops: nil,
			want: nil,
		},
		{
			name: "no hops, with trace",
			run:  run("t", "trace-t", ""),
			hops: nil,
			want: []audit.IncidentJourneyRef{{Phase: "triage", TraceID: "trace-t"}},
		},
		{
			name: "one hop via transition, distinct traces",
			run:  run("t", "trace-t", "pbs_remediate"),
			hops: []*audit.PlaybookRun{run("r", "trace-r", "")},
			want: []audit.IncidentJourneyRef{
				{Phase: "triage", TraceID: "trace-t"},
				{Phase: "remediation", TraceID: "trace-r"},
			},
		},
		{
			name: "one hop via transition, shared trace merges",
			run:  run("t", "trace-shared", "pbs_remediate"),
			hops: []*audit.PlaybookRun{run("r", "trace-shared", "")},
			want: []audit.IncidentJourneyRef{
				{Phase: "triage+remediation", TraceID: "trace-shared"},
			},
		},
		{
			name: "one hop via escalation only (no transition)",
			run:  run("t", "trace-t", ""), // triage escalated, did not transition
			hops: []*audit.PlaybookRun{run("e", "trace-e", "")},
			want: []audit.IncidentJourneyRef{
				{Phase: "triage", TraceID: "trace-t"},
				{Phase: "escalation:1", TraceID: "trace-e"},
			},
		},
		{
			name: "three hops: escalation, then transition",
			run:  run("t", "trace-t", ""),
			hops: []*audit.PlaybookRun{
				run("e", "trace-e", "pbs_remediate"), // this hop transitions -> next hop is remediation
				run("r", "trace-r", ""),
			},
			want: []audit.IncidentJourneyRef{
				{Phase: "triage", TraceID: "trace-t"},
				{Phase: "escalation:1", TraceID: "trace-e"},
				{Phase: "remediation", TraceID: "trace-r"},
			},
		},
		{
			name: "mid-chain merge: two escalation hops share a trace",
			run:  run("t", "trace-t", ""),
			hops: []*audit.PlaybookRun{
				run("e1", "trace-shared", ""),
				run("e2", "trace-shared", ""),
			},
			want: []audit.IncidentJourneyRef{
				{Phase: "triage", TraceID: "trace-t"},
				{Phase: "escalation:1+escalation:2", TraceID: "trace-shared"},
			},
		},
		{
			name: "hop with empty trace_id is skipped",
			run:  run("t", "trace-t", ""),
			hops: []*audit.PlaybookRun{run("e", "", "")},
			want: []audit.IncidentJourneyRef{
				{Phase: "triage", TraceID: "trace-t"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildJourneyRefs(tc.run, tc.hops)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
