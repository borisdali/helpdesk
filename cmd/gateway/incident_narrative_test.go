package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	// eventsByTraceID maps a trace_id to the delegation_verification events GET
	// /v1/events?event_type=delegation_verification&trace_id=<key> should
	// return. Absent key ⇒ empty array (no events found for that trace — the
	// fail-open case).
	eventsByTraceID map[string][]audit.Event
	// eventsRequestCount counts GET /v1/events?event_type=delegation_verification
	// requests per trace_id — used to assert the dedup cache in
	// handleGetIncident actually works.
	eventsRequestCount map[string]int
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

		// Delegation-verification events: GET /v1/events?event_type=
		// delegation_verification&trace_id=X — bare array response, matching
		// cmd/auditd's real handler (no envelope).
		case strings.Contains(path, "/events") && r.URL.Query().Get("event_type") == "delegation_verification":
			traceID := r.URL.Query().Get("trace_id")
			if m.eventsRequestCount == nil {
				m.eventsRequestCount = make(map[string]int)
			}
			m.eventsRequestCount[traceID]++
			json.NewEncoder(w).Encode(m.eventsByTraceID[traceID]) //nolint:errcheck

		// Gate event lookup (and any other event_type).
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

// TestHandleGetIncident_VerificationFlags_SurfaceOnChapter verifies that a
// flagged Journey's has_mismatch/has_target_drift surface inline on the
// Triage chapter, closing the gap where a confident narrative previously gave
// no indication that its underlying tool calls weren't fully verified.
func TestHandleGetIncident_VerificationFlags_SurfaceOnChapter(t *testing.T) {
	run := &audit.PlaybookRun{
		RunID:           "plr_flag01",
		SeriesID:        "pbs_db_restart_triage",
		FindingsSummary: "Confident diagnosis",
		Outcome:         audit.OutcomeResolved,
		StartedAt:       time.Now().UTC(),
		TraceID:         "tr_flag01",
	}
	mock := &mockIncidentAuditd{
		triageRun: run,
		eventsByTraceID: map[string][]audit.Event{
			"tr_flag01": {{
				Timestamp: run.StartedAt.Add(time.Second),
				DelegationVerification: &audit.DelegationVerification{
					Mismatch:          true,
					ProtocolViolation: true,
				},
			}},
		},
	}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	rec := getIncident(t, gw, "plr_flag01")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var n IncidentNarrative
	if err := json.NewDecoder(rec.Body).Decode(&n); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if n.Triage.TraceID != "tr_flag01" {
		t.Errorf("Triage.TraceID = %q, want tr_flag01", n.Triage.TraceID)
	}
	if !n.Triage.HasMismatch {
		t.Error("Triage.HasMismatch = false, want true — should surface inline without a separate Journey lookup")
	}
	if n.Triage.HasTargetDrift {
		t.Error("Triage.HasTargetDrift = true, want false")
	}
	if !n.Triage.HasProtocolViolation {
		t.Error("Triage.HasProtocolViolation = false, want true — should surface inline without a separate Journey lookup")
	}
}

// TestHandleGetIncident_VerificationFlags_NoEventData_FailsOpen verifies
// that a chapter whose trace has no discoverable delegation_verification
// events fails open to false rather than erroring — absence of a flag must
// never be confused with "verified clean" vs "no data", but at the boolean
// level both surface identically as false by design.
func TestHandleGetIncident_VerificationFlags_NoEventData_FailsOpen(t *testing.T) {
	run := &audit.PlaybookRun{
		RunID:     "plr_flag02",
		SeriesID:  "pbs_db_restart_triage",
		Outcome:   audit.OutcomeResolved,
		StartedAt: time.Now().UTC(),
		TraceID:   "tr_flag02",
	}
	// eventsByTraceID deliberately has no entry for tr_flag02.
	mock := &mockIncidentAuditd{triageRun: run}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	rec := getIncident(t, gw, "plr_flag02")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var n IncidentNarrative
	if err := json.NewDecoder(rec.Body).Decode(&n); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if n.Triage.HasMismatch || n.Triage.HasTargetDrift || n.Triage.HasProtocolViolation {
		t.Errorf("expected all three flags false when no Journey data exists, got HasMismatch=%v HasTargetDrift=%v HasProtocolViolation=%v",
			n.Triage.HasMismatch, n.Triage.HasTargetDrift, n.Triage.HasProtocolViolation)
	}
}

// TestHandleGetIncident_VerificationFlags_SharedTraceDedup verifies the
// per-request events cache: when triage and remediation share one trace_id,
// handleGetIncident must fetch that trace's delegation_verification events
// exactly once, not once per chapter that references it — regardless of how
// many chapters/events end up attributed to it. Per-hop windows are disjoint
// by construction (each hop's window ends where the next hop's begins), so a
// shared trace_id no longer means "both chapters show the same flag" (that
// was the pre-fix, leaking behavior) — this test uses two distinct events,
// one per hop's own window, to prove both correct disjoint attribution AND
// the dedup cache in the same scenario that originally exposed the leak.
func TestHandleGetIncident_VerificationFlags_SharedTraceDedup(t *testing.T) {
	const sharedTrace = "tr_shared01"
	triageStart := time.Now().Add(-2 * time.Minute).UTC()
	remediationStart := time.Now().Add(-1 * time.Minute).UTC()
	run := &audit.PlaybookRun{
		RunID:          "plr_shared01",
		SeriesID:       "pbs_db_restart_triage",
		Outcome:        audit.OutcomeTransitioned,
		TransitionedTo: "pbs_db_restart_action",
		StartedAt:      triageStart,
		TraceID:        sharedTrace,
	}
	remediationHop := &audit.PlaybookRun{
		RunID:      "plr_shared02",
		SeriesID:   "pbs_db_restart_action",
		Outcome:    audit.OutcomeResolved,
		PriorRunID: "plr_shared01",
		TraceID:    sharedTrace,
		StartedAt:  remediationStart,
	}
	mock := &mockIncidentAuditd{
		triageRun:        run,
		nextRunByPriorID: map[string]*audit.PlaybookRun{"plr_shared01": remediationHop},
		eventsByTraceID: map[string][]audit.Event{
			sharedTrace: {
				{
					Timestamp:              triageStart.Add(time.Second),
					DelegationVerification: &audit.DelegationVerification{Mismatch: true},
				},
				{
					Timestamp:              remediationStart.Add(time.Second),
					DelegationVerification: &audit.DelegationVerification{TargetDrift: []string{"host=other-db"}},
				},
			},
		},
	}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	rec := getIncident(t, gw, "plr_shared01")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var n IncidentNarrative
	if err := json.NewDecoder(rec.Body).Decode(&n); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !n.Triage.HasMismatch || n.Triage.HasTargetDrift {
		t.Errorf("Triage flags = (mismatch=%v, drift=%v), want (true, false) — only its own hop's event", n.Triage.HasMismatch, n.Triage.HasTargetDrift)
	}
	if n.Remediation == nil {
		t.Fatal("narrative missing remediation chapter")
	}
	if n.Remediation.HasMismatch || !n.Remediation.HasTargetDrift {
		t.Errorf("Remediation flags = (mismatch=%v, drift=%v), want (false, true) — only its own hop's event, not triage's mismatch", n.Remediation.HasMismatch, n.Remediation.HasTargetDrift)
	}
	if got := mock.eventsRequestCount[sharedTrace]; got != 1 {
		t.Errorf("events requests for shared trace = %d, want exactly 1 (dedup cache should prevent a second fetch)", got)
	}
}

// TestHandleGetIncident_VerificationFlags_AllThreeChaptersIndependent verifies
// that Triage, an Escalation hop, AND Remediation each surface their OWN
// flag value from their OWN distinct trace — closing a gap the shared-trace
// dedup test doesn't: that test gives Triage and Remediation the *same*
// trace/flag data (by design, to test the cache), so it can't prove
// Remediation is wired to its own independent journey lookup rather than
// accidentally inheriting Triage's.
func TestHandleGetIncident_VerificationFlags_AllThreeChaptersIndependent(t *testing.T) {
	run := &audit.PlaybookRun{
		RunID:       "plr_indep01",
		SeriesID:    "pbs_db_restart_triage",
		Outcome:     audit.OutcomeEscalated,
		EscalatedTo: "pbs_sysadmin_docker_inspect",
		StartedAt:   time.Now().Add(-3 * time.Minute).UTC(),
		TraceID:     "tr_indep_triage",
	}
	escalation := &audit.PlaybookRun{
		RunID:          "plr_indep02",
		SeriesID:       "pbs_sysadmin_docker_inspect",
		Outcome:        audit.OutcomeTransitioned,
		TransitionedTo: "pbs_db_restart_action",
		PriorRunID:     "plr_indep01",
		TraceID:        "tr_indep_escalation",
		StartedAt:      time.Now().Add(-2 * time.Minute).UTC(),
	}
	remediation := &audit.PlaybookRun{
		RunID:      "plr_indep03",
		SeriesID:   "pbs_db_restart_action",
		Outcome:    audit.OutcomeResolved,
		PriorRunID: "plr_indep02",
		TraceID:    "tr_indep_remediation",
		StartedAt:  time.Now().Add(-1 * time.Minute).UTC(),
	}
	mock := &mockIncidentAuditd{
		triageRun: run,
		nextRunByPriorID: map[string]*audit.PlaybookRun{
			"plr_indep01": escalation,
			"plr_indep02": remediation,
		},
		eventsByTraceID: map[string][]audit.Event{
			"tr_indep_triage": {{
				Timestamp:              run.StartedAt.Add(time.Second),
				DelegationVerification: &audit.DelegationVerification{Mismatch: true},
			}},
			"tr_indep_escalation": {{
				Timestamp:              escalation.StartedAt.Add(time.Second),
				DelegationVerification: &audit.DelegationVerification{ProtocolViolation: true},
			}},
			"tr_indep_remediation": {{
				Timestamp:              remediation.StartedAt.Add(time.Second),
				DelegationVerification: &audit.DelegationVerification{TargetDrift: []string{"host=other-db"}},
			}},
		},
	}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	rec := getIncident(t, gw, "plr_indep01")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var n IncidentNarrative
	if err := json.NewDecoder(rec.Body).Decode(&n); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !n.Triage.HasMismatch || n.Triage.HasTargetDrift || n.Triage.HasProtocolViolation {
		t.Errorf("Triage flags = (mismatch=%v, drift=%v, violation=%v), want (true, false, false)",
			n.Triage.HasMismatch, n.Triage.HasTargetDrift, n.Triage.HasProtocolViolation)
	}
	if len(n.Escalations) != 1 {
		t.Fatalf("escalations count = %d, want 1", len(n.Escalations))
	}
	if n.Escalations[0].HasMismatch || n.Escalations[0].HasTargetDrift || !n.Escalations[0].HasProtocolViolation {
		t.Errorf("Escalation flags = (mismatch=%v, drift=%v, violation=%v), want (false, false, true) — must come from its own trace",
			n.Escalations[0].HasMismatch, n.Escalations[0].HasTargetDrift, n.Escalations[0].HasProtocolViolation)
	}
	if n.Remediation == nil {
		t.Fatal("narrative missing remediation chapter")
	}
	if n.Remediation.HasMismatch || !n.Remediation.HasTargetDrift || n.Remediation.HasProtocolViolation {
		t.Errorf("Remediation flags = (mismatch=%v, drift=%v, violation=%v), want (false, true, false) — must come from its own trace, not Triage's",
			n.Remediation.HasMismatch, n.Remediation.HasTargetDrift, n.Remediation.HasProtocolViolation)
	}
}

// TestHandleGetIncident_VerificationFlags_SharedTrace_DoesNotLeakAcrossHops is a
// regression test for a real bug found via live 3-hop escalation-chain
// verification: force-mode auto-chaining (chainEscalation, cmd/gateway/
// playbooks.go) reuses the same *http.Request across every chained hop, so
// when the caller supplies its own X-Trace-ID (as faulttest's real client
// does), every hop in one auto-chain ends up sharing that single trace_id.
// The old whole-trace Journey lookup couldn't distinguish between hops
// sharing a trace_id, so a later hop's genuine mismatch leaked backward onto
// an earlier, actually-clean hop's reported HasMismatch — confirmed live: the
// pbs_sysadmin_docker_inspect hop's own delegation_verification event said
// mismatch:false, but its posted hop-cert still showed DIRTY. This models
// that exact shape: an escalation hop and a remediation hop sharing one
// trace_id, where only the remediation hop's own window contains a mismatch
// event.
func TestHandleGetIncident_VerificationFlags_SharedTrace_DoesNotLeakAcrossHops(t *testing.T) {
	const sharedTrace = "trace-shared-multihop"
	triage := &audit.PlaybookRun{
		RunID:       "plr_leak_t1",
		SeriesID:    "pbs_connection_triage",
		Outcome:     audit.OutcomeEscalated,
		EscalatedTo: "pbs_sysadmin_docker_inspect",
		TraceID:     "trace-leak-triage",
		StartedAt:   time.Now().Add(-3 * time.Minute).UTC(),
	}
	escHop := &audit.PlaybookRun{
		RunID:          "plr_leak_e1",
		SeriesID:       "pbs_sysadmin_docker_inspect",
		Outcome:        audit.OutcomeTransitioned,
		TransitionedTo: "pbs_db_restart_action",
		PriorRunID:     "plr_leak_t1",
		TraceID:        sharedTrace,
		StartedAt:      time.Now().Add(-2 * time.Minute).UTC(),
		CompletedAt:    time.Now().Add(-90 * time.Second).UTC(),
	}
	remHop := &audit.PlaybookRun{
		RunID:      "plr_leak_r1",
		SeriesID:   "pbs_db_restart_action",
		Outcome:    audit.OutcomeResolved,
		PriorRunID: "plr_leak_e1",
		TraceID:    sharedTrace,
		StartedAt:  time.Now().Add(-1 * time.Minute).UTC(),
	}
	mock := &mockIncidentAuditd{
		triageRun: triage,
		nextRunByPriorID: map[string]*audit.PlaybookRun{
			"plr_leak_t1": escHop,
			"plr_leak_e1": remHop,
		},
		eventsByTraceID: map[string][]audit.Event{
			// Only one event on the shared trace, timestamped after escHop's
			// own CompletedAt (i.e. outside its window) but within remHop's
			// still-open window — a policy-denied write on the terminal
			// remediation hop, exactly like the live restart_container case.
			sharedTrace: {{
				Timestamp:              remHop.StartedAt.Add(time.Second),
				DelegationVerification: &audit.DelegationVerification{Mismatch: true},
			}},
		},
	}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	rec := getIncident(t, gw, "plr_leak_t1")
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
	if n.Escalations[0].HasMismatch {
		t.Error("Escalations[0].HasMismatch = true, want false — the mismatch event belongs to the later remediation hop sharing this trace_id, not this one")
	}
	if n.Remediation == nil {
		t.Fatal("narrative missing remediation chapter")
	}
	if !n.Remediation.HasMismatch {
		t.Error("Remediation.HasMismatch = false, want true — its own hop genuinely mismatched")
	}
	if got := mock.eventsRequestCount[sharedTrace]; got != 1 {
		t.Errorf("events requests for shared trace = %d, want exactly 1 (dedup cache should still work even though the events are now hop-filtered)", got)
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

// TestHandleGetIncident_FourHopTwoEscalations verifies the actual motivating
// scenario — a chain that crosses two agent boundaries before remediation
// (e.g. database agent -> sysadmin agent -> k8s agent -> remediation) — is
// walked and classified completely at the handler level, not just by the
// pure buildJourneyRefs function in isolation.
func TestHandleGetIncident_FourHopTwoEscalations(t *testing.T) {
	triage := &audit.PlaybookRun{
		RunID:       "plr_h1",
		SeriesID:    "pbs_connection_triage",
		Outcome:     audit.OutcomeEscalated,
		EscalatedTo: "pbs_sysadmin_docker_inspect",
		TraceID:     "trace-h1",
		StartedAt:   time.Now().Add(-4 * time.Minute).UTC(),
	}
	esc1 := &audit.PlaybookRun{
		RunID:       "plr_h2",
		SeriesID:    "pbs_sysadmin_docker_inspect",
		Outcome:     audit.OutcomeEscalated,
		EscalatedTo: "pbs_k8s_pod_crash_triage",
		PriorRunID:  "plr_h1",
		TraceID:     "trace-h2",
		StartedAt:   time.Now().Add(-3 * time.Minute).UTC(),
	}
	esc2 := &audit.PlaybookRun{
		RunID:          "plr_h3",
		SeriesID:       "pbs_k8s_pod_crash_triage",
		Outcome:        audit.OutcomeTransitioned,
		TransitionedTo: "pbs_k8s_pod_crash_remediate",
		PriorRunID:     "plr_h2",
		TraceID:        "trace-h3",
		StartedAt:      time.Now().Add(-2 * time.Minute).UTC(),
	}
	remHop := &audit.PlaybookRun{
		RunID:       "plr_h4",
		SeriesID:    "pbs_k8s_pod_crash_remediate",
		Outcome:     audit.OutcomeResolved,
		PriorRunID:  "plr_h3",
		TraceID:     "trace-h4",
		StartedAt:   time.Now().Add(-1 * time.Minute).UTC(),
		CompletedAt: time.Now().UTC(),
	}
	mock := &mockIncidentAuditd{
		triageRun: triage,
		nextRunByPriorID: map[string]*audit.PlaybookRun{
			"plr_h1": esc1,
			"plr_h2": esc2,
			"plr_h3": remHop,
		},
	}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	rec := getIncident(t, gw, "plr_h1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
	var n IncidentNarrative
	if err := json.NewDecoder(rec.Body).Decode(&n); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(n.Escalations) != 2 {
		t.Fatalf("Escalations len = %d, want 2; got %+v", len(n.Escalations), n.Escalations)
	}
	if n.Escalations[0].RunID != "plr_h2" {
		t.Errorf("Escalations[0].RunID = %q, want plr_h2", n.Escalations[0].RunID)
	}
	if n.Escalations[1].RunID != "plr_h3" {
		t.Errorf("Escalations[1].RunID = %q, want plr_h3", n.Escalations[1].RunID)
	}
	if n.Remediation == nil || n.Remediation.RunID != "plr_h4" {
		t.Fatalf("Remediation = %+v, want run_id=plr_h4 (the true terminal hop)", n.Remediation)
	}
	if len(n.Journeys) != 4 {
		t.Fatalf("Journeys len = %d, want 4; got %v", len(n.Journeys), n.Journeys)
	}
	wantPhases := []string{"triage", "escalation:1", "escalation:2", "remediation"}
	for i, want := range wantPhases {
		if n.Journeys[i].Phase != want {
			t.Errorf("Journeys[%d].Phase = %q, want %q", i, n.Journeys[i].Phase, want)
		}
	}
}

// TestHandleGetIncident_RemediationSuccessorIgnored locks in the documented
// v1 scope boundary: a remediation run that itself has a successor (e.g. it
// further escalates) is not solved by this fix. fetchEscalationHops walks
// blindly and will fetch that successor over the network, but the
// classification loop stops at the first TRANSITION_TO hop, so the successor
// is silently absent from the narrative rather than misclassified or
// causing an error. If this behavior is intentionally extended later, this
// test should be updated, not just deleted.
func TestHandleGetIncident_RemediationSuccessorIgnored(t *testing.T) {
	triage := &audit.PlaybookRun{
		RunID:       "plr_s1",
		SeriesID:    "pbs_connection_triage",
		Outcome:     audit.OutcomeEscalated,
		EscalatedTo: "pbs_sysadmin_docker_inspect",
		TraceID:     "trace-s1",
		StartedAt:   time.Now().Add(-3 * time.Minute).UTC(),
	}
	esc := &audit.PlaybookRun{
		RunID:          "plr_s2",
		SeriesID:       "pbs_sysadmin_docker_inspect",
		Outcome:        audit.OutcomeTransitioned,
		TransitionedTo: "pbs_k8s_pod_crash_remediate",
		PriorRunID:     "plr_s1",
		TraceID:        "trace-s2",
		StartedAt:      time.Now().Add(-2 * time.Minute).UTC(),
	}
	remHop := &audit.PlaybookRun{
		RunID:       "plr_s3",
		SeriesID:    "pbs_k8s_pod_crash_remediate",
		Outcome:     audit.OutcomeEscalated, // remediation itself escalated further — unusual, out of scope
		EscalatedTo: "pbs_some_other_series",
		PriorRunID:  "plr_s2",
		TraceID:     "trace-s3",
		StartedAt:   time.Now().Add(-1 * time.Minute).UTC(),
	}
	// plr_s3 (the remediation hop) has its own successor — should be fetched
	// by fetchEscalationHops but never classified or surfaced.
	successor := &audit.PlaybookRun{
		RunID:      "plr_s4",
		SeriesID:   "pbs_some_other_series",
		Outcome:    audit.OutcomeResolved,
		PriorRunID: "plr_s3",
		TraceID:    "trace-s4",
		StartedAt:  time.Now().UTC(),
	}
	mock := &mockIncidentAuditd{
		triageRun: triage,
		nextRunByPriorID: map[string]*audit.PlaybookRun{
			"plr_s1": esc,
			"plr_s2": remHop,
			"plr_s3": successor,
		},
	}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	rec := getIncident(t, gw, "plr_s1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
	var n IncidentNarrative
	if err := json.NewDecoder(rec.Body).Decode(&n); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(n.Escalations) != 1 || n.Escalations[0].RunID != "plr_s2" {
		t.Fatalf("Escalations = %+v, want exactly [plr_s2]", n.Escalations)
	}
	if n.Remediation == nil || n.Remediation.RunID != "plr_s3" {
		t.Fatalf("Remediation = %+v, want run_id=plr_s3", n.Remediation)
	}
	// plr_s4 must not appear anywhere — not as an escalation, not as remediation.
	for _, e := range n.Escalations {
		if e.RunID == "plr_s4" {
			t.Errorf("plr_s4 (remediation's successor) should not appear in Escalations")
		}
	}
	if n.Remediation.RunID == "plr_s4" {
		t.Errorf("plr_s4 (remediation's successor) should not have overwritten Remediation")
	}
	for _, j := range n.Journeys {
		if j.TraceID == "trace-s4" {
			t.Errorf("Journeys should not reference plr_s4's trace, got %+v", n.Journeys)
		}
	}
	if len(n.Journeys) != 3 {
		t.Errorf("Journeys len = %d, want 3 (triage, escalation:1, remediation) — plr_s4 excluded", len(n.Journeys))
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

// TestFetchEscalationHops_MaxHopsBound verifies the *other* way the walk can
// stop: a long, strictly acyclic chain (every run ID distinct — the cycle
// guard never fires) that exceeds maxEscalationHops. The walk must stop at
// exactly maxEscalationHops hops, not overrun it and not return one short.
func TestFetchEscalationHops_MaxHopsBound(t *testing.T) {
	const chainLen = maxEscalationHops + 5 // deliberately longer than the bound

	next := map[string]*audit.PlaybookRun{}
	for i := 0; i < chainLen; i++ {
		runID := fmt.Sprintf("plr_long%d", i+1)
		priorID := fmt.Sprintf("plr_long%d", i)
		if i == 0 {
			priorID = "plr_long0" // triage run's own ID
		}
		next[priorID] = &audit.PlaybookRun{
			RunID:      runID,
			SeriesID:   fmt.Sprintf("pbs_long%d", i+1),
			Outcome:    audit.OutcomeEscalated,
			PriorRunID: priorID,
			StartedAt:  time.Now().UTC(),
		}
	}
	mock := &mockIncidentAuditd{
		triageRun:        &audit.PlaybookRun{RunID: "plr_long0", SeriesID: "pbs_long0"},
		nextRunByPriorID: next,
	}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	done := make(chan []*audit.PlaybookRun, 1)
	go func() {
		done <- gw.fetchEscalationHops(context.Background(), "plr_long0")
	}()

	select {
	case hops := <-done:
		if len(hops) != maxEscalationHops {
			t.Fatalf("hops len = %d, want exactly maxEscalationHops (%d)", len(hops), maxEscalationHops)
		}
		if hops[0].RunID != "plr_long1" {
			t.Errorf("hops[0].RunID = %q, want plr_long1", hops[0].RunID)
		}
		if want := fmt.Sprintf("plr_long%d", maxEscalationHops); hops[len(hops)-1].RunID != want {
			t.Errorf("last hop RunID = %q, want %q (stopped exactly at the bound)", hops[len(hops)-1].RunID, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fetchEscalationHops did not return — max-hops bound failed to stop the walk")
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

// TestHopVerificationFlags exercises hopVerificationFlags directly (rather
// than only indirectly through handleGetIncident), covering boundary and
// aggregation cases the end-to-end incident tests don't specifically pin
// down: window-boundary inclusivity/exclusivity, multiple events in one
// window with mixed signals (OR-aggregation across all three flags
// independently), and a nil DelegationVerification event being skipped
// rather than panicking.
func TestHopVerificationFlags(t *testing.T) {
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	start := base
	end := base.Add(10 * time.Second)

	mismatchEvent := func(ts time.Time) audit.Event {
		return audit.Event{Timestamp: ts, DelegationVerification: &audit.DelegationVerification{Mismatch: true}}
	}

	tests := []struct {
		name                                                 string
		events                                               []audit.Event
		start, end                                           time.Time
		wantMismatch, wantTargetDrift, wantProtocolViolation bool
	}{
		{
			name:   "empty events",
			events: nil,
			start:  start, end: end,
		},
		{
			name:   "event exactly at start is included (inclusive lower bound)",
			events: []audit.Event{mismatchEvent(start)},
			start:  start, end: end,
			wantMismatch: true,
		},
		{
			name:   "event exactly at end is excluded (exclusive upper bound)",
			events: []audit.Event{mismatchEvent(end)},
			start:  start, end: end,
			wantMismatch: false,
		},
		{
			name:   "event one nanosecond before end is included",
			events: []audit.Event{mismatchEvent(end.Add(-time.Nanosecond))},
			start:  start, end: end,
			wantMismatch: true,
		},
		{
			name:   "event before start is excluded",
			events: []audit.Event{mismatchEvent(start.Add(-time.Second))},
			start:  start, end: end,
			wantMismatch: false,
		},
		{
			name:   "zero end means unbounded — a far-future event still counts",
			events: []audit.Event{mismatchEvent(start.Add(365 * 24 * time.Hour))},
			start:  start, end: time.Time{},
			wantMismatch: true,
		},
		{
			name: "multiple events in one window, mixed signals OR together independently",
			events: []audit.Event{
				{Timestamp: start.Add(time.Second), DelegationVerification: &audit.DelegationVerification{Mismatch: true}},
				{Timestamp: start.Add(2 * time.Second), DelegationVerification: &audit.DelegationVerification{TargetDrift: []string{"host=x"}}},
				{Timestamp: start.Add(3 * time.Second), DelegationVerification: &audit.DelegationVerification{ProtocolViolation: true}},
			},
			start: start, end: end,
			wantMismatch: true, wantTargetDrift: true, wantProtocolViolation: true,
		},
		{
			name:   "event with nil DelegationVerification is skipped, not a panic",
			events: []audit.Event{{Timestamp: start.Add(time.Second), DelegationVerification: nil}},
			start:  start, end: end,
			wantMismatch: false,
		},
		{
			name:   "TargetDriftDetail-only signal still sets HasTargetDrift via TargetDrift presence",
			events: []audit.Event{{Timestamp: start.Add(time.Second), DelegationVerification: &audit.DelegationVerification{TargetDrift: []string{"host=other"}}}},
			start:  start, end: end,
			wantTargetDrift: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotMismatch, gotTargetDrift, gotProtocolViolation := hopVerificationFlags(tc.events, tc.start, tc.end)
			if gotMismatch != tc.wantMismatch || gotTargetDrift != tc.wantTargetDrift || gotProtocolViolation != tc.wantProtocolViolation {
				t.Errorf("hopVerificationFlags() = (mismatch=%v, drift=%v, violation=%v), want (mismatch=%v, drift=%v, violation=%v)",
					gotMismatch, gotTargetDrift, gotProtocolViolation, tc.wantMismatch, tc.wantTargetDrift, tc.wantProtocolViolation)
			}
		})
	}
}
