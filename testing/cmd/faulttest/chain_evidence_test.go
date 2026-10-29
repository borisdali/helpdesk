package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"helpdesk/testing/testutil"
)

// The db-pgdata-corrupted-vm fault as the catalog declares it, and the first
// hop's response as the real 2026-10-25 VM run produced it: the entry hop has
// no log tools, so it said nothing about pg_control or the data directory.
func chainTestFault() Failure {
	return Failure{
		ID:       "db-pgdata-corrupted-vm",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedTools:     []string{"get_pgbackrest_status"},
			ExpectedKeywords:  KeywordSpec{AnyOf: []string{"pg_control", "could not find the database system", "data directory", "corrupt", "restore"}},
			ExpectedDiagnosis: DiagnosisSpec{Category: "pgdata_corrupted"},
		},
	}
}

const chainFirstHop = "I'm beginning triage of an unavailable PostgreSQL instance on bare VM. " +
	"Determination: single_instance: true, backup_tool: pgbackrest, pitr_decided: false. ESCALATE_TO: pbs_sysadmin_host_triage"

func chainTestNarrative() *incidentNarrative {
	n := &incidentNarrative{}
	n.Triage.Findings = "single_instance=true; backup_tool=pgbackrest; pitr_decided=false"
	n.Escalations = []narrativeEscalationHop{
		{Playbook: "pbs_sysadmin_host_triage", Findings: `PostgreSQL data directory is missing pg_control; logs show "could not find the database system"`},
		{Playbook: "pbs_pgbackrest_health_triage", Findings: "pgBackRest backup is healthy with continuous WAL chain to archive end"},
	}
	n.Remediation = &struct {
		RunID                        string          `json:"run_id"`
		Playbook                     string          `json:"playbook"`
		Outcome                      string          `json:"outcome"`
		Findings                     string          `json:"findings,omitempty"`
		Transcript                   string          `json:"transcript,omitempty"`
		Steps                        []narrativeStep `json:"steps,omitempty"`
		TraceID                      string          `json:"trace_id,omitempty"`
		HasMismatch                  bool            `json:"has_mismatch,omitempty"`
		HasTargetDrift               bool            `json:"has_target_drift,omitempty"`
		HasProtocolViolation         bool            `json:"has_protocol_violation,omitempty"`
		HasUnverifiedEvidence        bool            `json:"has_unverified_evidence,omitempty"`
		UnverifiedEvidence           []string        `json:"unverified_evidence,omitempty"`
		UnverifiedEvidenceSecondary  []string        `json:"unverified_evidence_secondary,omitempty"`
		SawSignalLine                bool            `json:"saw_signal_line,omitempty"`
		ObjectiveEvidenceConfirmed   []string        `json:"objective_evidence_confirmed,omitempty"`
		ObjectiveEvidenceUnconfirmed []string        `json:"objective_evidence_unconfirmed,omitempty"`
	}{Playbook: "pbs_pgbackrest_restore_remediate", Outcome: "resolved", Findings: "Restore succeeded"}
	return n
}

func firstHopResult(t *testing.T) EvalResult {
	t.Helper()
	r := Evaluate(chainTestFault(), testutil.AgentResponse{Text: chainFirstHop})
	r.ResponseText = chainFirstHop
	return r
}

// The bug: a chain that found pg_control and restored the database failed its
// diagnostic because only the entry hop was scored.
func TestApplyChainEvidence_LaterHopsMeetTheCriteria(t *testing.T) {
	r := firstHopResult(t)
	if r.Passed || r.KeywordPass {
		t.Fatalf("precondition: the first hop alone must fail the keyword check, got %+v", r)
	}
	chain, hops := chainDiagnosticText(chainTestNarrative())
	if hops != 2 {
		t.Fatalf("hops = %d, want 2 diagnostic hops past the first", hops)
	}
	if !ApplyChainEvidence(chainTestFault(), &r, chain, hops, false) {
		t.Fatal("ApplyChainEvidence changed nothing")
	}
	if !r.KeywordPass || r.KeywordScore != 1 {
		t.Errorf("keyword criterion not met after the chain: %+v", r)
	}
	if !r.Passed || r.Score < 0.6 {
		t.Errorf("Passed=%v Score=%.4f kw=%.2f diag=%.2f tool=%.2f, want a pass (keyword 0.5 + tools 0.2 = 0.7 clears 0.6)", r.Passed, r.Score, r.KeywordScore, r.DiagnosisScore, r.ToolScore)
	}
	if r.ChainEvidenceHops != 2 {
		t.Errorf("ChainEvidenceHops = %d, want 2", r.ChainEvidenceHops)
	}
}

// The remediation hop says "restore" because it restored: that is graded by
// the remediation score, so it must not satisfy the diagnostic keywords.
func TestChainDiagnosticText_ExcludesTheRemediationHop(t *testing.T) {
	n := &incidentNarrative{}
	n.Triage.Findings = "single_instance=true"
	n.Escalations = []narrativeEscalationHop{{Findings: "health looks fine"}}
	nn := chainTestNarrative()
	n.Remediation = nn.Remediation
	text, _ := chainDiagnosticText(n)
	if strings.Contains(strings.ToLower(text), "restore") {
		t.Errorf("chain text includes the remediation hop's findings: %q", text)
	}
	r := firstHopResult(t)
	if ApplyChainEvidence(chainTestFault(), &r, text, 1, false) || r.Passed {
		t.Errorf("a chain whose diagnostic hops never mention the fault must not pass: %+v", r)
	}
}

func TestChainDiagnosticText_SingleHopAddsNothing(t *testing.T) {
	n := &incidentNarrative{}
	n.Triage.Findings = "pg_control mentioned here"
	if text, hops := chainDiagnosticText(n); text != "" || hops != 0 {
		t.Errorf("a single-hop run must add nothing, got %q, %d", text, hops)
	}
	if text, hops := chainDiagnosticText(nil); text != "" || hops != 0 {
		t.Errorf("a nil narrative must add nothing, got %q, %d", text, hops)
	}
}

// A chain can only help: an already-passing first hop is left alone.
func TestApplyChainEvidence_NeverLowersAPass(t *testing.T) {
	f := chainTestFault()
	r := Evaluate(f, testutil.AgentResponse{Text: "pg_control is missing, restore from backup. get_pgbackrest_status"})
	r.ResponseText = "pg_control is missing, restore from backup. get_pgbackrest_status"
	before := r
	chain, hops := chainDiagnosticText(chainTestNarrative())
	ApplyChainEvidence(f, &r, chain, hops, false)
	if r.Score < before.Score || (before.Passed && !r.Passed) || r.KeywordScore < before.KeywordScore {
		t.Errorf("a chain lowered a result: before %+v after %+v", before, r)
	}
}

// An evidence gate that failed the result (the expected objective signal never
// fired or was never confirmed) is not something chain text can override.
func TestApplyChainEvidence_EvidenceGatesStillFail(t *testing.T) {
	for name, set := range map[string]func(*EvalResult){
		"coverage gap": func(r *EvalResult) { r.EvidenceCoverageGap = true },
		"unconfirmed":  func(r *EvalResult) { r.EvidenceRequiredButUnconfirmed = true },
	} {
		t.Run(name, func(t *testing.T) {
			r := firstHopResult(t)
			set(&r)
			chain, hops := chainDiagnosticText(chainTestNarrative())
			ApplyChainEvidence(chainTestFault(), &r, chain, hops, false)
			if r.Passed {
				t.Errorf("an evidence-gated result passed after chain evidence: %+v", r)
			}
		})
	}
}

func TestApplyChainEvidence_ProtocolViolationCapStillApplies(t *testing.T) {
	r := firstHopResult(t)
	r.ProtocolViolation = true
	f := chainTestFault()
	chain, hops := chainDiagnosticText(chainTestNarrative())
	ApplyChainEvidence(f, &r, chain, hops, false)
	if r.Score > 0.75 {
		t.Errorf("Score = %.2f, the protocol-violation cap of 0.75 must still apply", r.Score)
	}
}

// With the LLM judge scoring the diagnosis, the chain re-checks keywords only.
func TestApplyChainEvidence_JudgedKeepsTheJudgesDiagnosis(t *testing.T) {
	r := firstHopResult(t)
	r.DiagnosisScore = 0.67
	r.DiagnosisPass = true
	r.ToolScore = 1
	chain, hops := chainDiagnosticText(chainTestNarrative())
	ApplyChainEvidence(chainTestFault(), &r, chain, hops, true)
	if r.DiagnosisScore != 0.67 {
		t.Errorf("the judge's diagnosis score was replaced: %.2f", r.DiagnosisScore)
	}
	want := 1*0.40 + 0.67*0.40 + 1*0.20
	if r.Score < want-0.001 || r.Score > want+0.001 {
		t.Errorf("Score = %.3f, want judge weights %.3f", r.Score, want)
	}
}

func TestApplyChainEvidence_EmptyChainIsANoOp(t *testing.T) {
	r := firstHopResult(t)
	before := r
	if ApplyChainEvidence(chainTestFault(), &r, "", 0, false) || r.Score != before.Score || r.Passed != before.Passed {
		t.Errorf("an empty chain changed the result: %+v", r)
	}
}

// The gateway's wire format for GET /api/v1/incidents/{run_id}, with the hop
// findings of the real 2026-10-25 VM run (plr_cea87922) as `vault incidents
// --summary` printed them. Decoding it proves the JSON tags the chain text
// relies on, which the struct-built narratives above bypass.
const chainNarrativeJSON = `{
  "incident_id": "plr_cea87922",
  "triage": {
    "run_id": "plr_cea87922",
    "trace_id": "tr_h1",
    "playbook": "pbs_db_data_loss_triage",
    "findings": "single_instance=true; backup_tool=pgbackrest; pitr_decided=false"
  },
  "escalations": [
    {"run_id": "plr_29750e97", "trace_id": "tr_h2", "playbook": "pbs_sysadmin_host_triage", "outcome": "transitioned",
     "transitioned_to": "pbs_pgbackrest_health_triage",
     "findings": "The data directory is missing the pg_control file, causing PostgreSQL to fail at startup",
     "diagnostic_report": {"hypotheses": [
        {"rank": 1, "text": "pg_control missing after a storage fault", "confidence": 0.9, "is_primary": true},
        {"rank": 2, "text": "disk full corrupted the WAL", "confidence": 0.1, "is_primary": false, "rejected_reason": "no ENOSPC in the log"}]}},
    {"run_id": "plr_58d7881b", "trace_id": "tr_h3", "playbook": "pbs_pgbackrest_health_triage", "outcome": "escalated",
     "escalated_to": "pbs_pgbackrest_restore_remediate",
     "findings": "Prior findings confirmed pg_control is missing (data loss); the most recent backup is healthy"}
  ],
  "remediation": {"run_id": "plr_ea16622e", "trace_id": "tr_h4", "playbook": "pbs_pgbackrest_restore_remediate", "outcome": "resolved",
                  "findings": "Restore from backup 20261025-125505F succeeded with recovery verified"}
}`

func narrativeServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/incidents/plr_cea87922" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// End to end through the real fetch and JSON decode: the 25 October failure.
func TestApplyChainEvidenceFromGateway_RealRunShape(t *testing.T) {
	srv := narrativeServer(t, http.StatusOK, chainNarrativeJSON)
	r := firstHopResult(t)
	r.RemediationAttempted, r.RemediationPassed, r.RemediationScore = true, true, 0.75
	r.OverallScore = r.Score*0.6 + r.RemediationScore*0.4

	hops, changed := applyChainEvidenceFromGateway(srv.URL, "", chainTestFault(), &r, testutil.AgentResponse{RunID: "plr_cea87922"}, nil, false)
	if !changed || hops != 2 {
		t.Fatalf("changed=%v hops=%d, want true and 2", changed, hops)
	}
	if !r.Passed || !r.KeywordPass || r.ChainEvidenceHops != 2 {
		t.Errorf("diagnostic did not pass on the chain: %+v", r)
	}
	if want := r.Score*0.6 + 0.75*0.4; r.OverallScore < want-1e-9 || r.OverallScore > want+1e-9 {
		t.Errorf("OverallScore = %.4f, want %.4f (recomputed from the raised score)", r.OverallScore, want)
	}
}

// A fetch failure must leave the first-hop result exactly as it was.
func TestApplyChainEvidenceFromGateway_FetchFailureLeavesTheResult(t *testing.T) {
	for name, status := range map[string]int{"not found": http.StatusNotFound, "server error": http.StatusInternalServerError} {
		t.Run(name, func(t *testing.T) {
			srv := narrativeServer(t, status, "nope")
			r := firstHopResult(t)
			before := r
			hops, changed := applyChainEvidenceFromGateway(srv.URL, "", chainTestFault(), &r, testutil.AgentResponse{RunID: "plr_cea87922"}, nil, false)
			if changed || hops != 0 || r.Score != before.Score || r.Passed != before.Passed || r.OverallScore != before.OverallScore {
				t.Errorf("a failed fetch changed the result: hops=%d changed=%v %+v", hops, changed, r)
			}
		})
	}
	t.Run("unparseable body", func(t *testing.T) {
		srv := narrativeServer(t, http.StatusOK, "<html>")
		r := firstHopResult(t)
		if _, changed := applyChainEvidenceFromGateway(srv.URL, "", chainTestFault(), &r, testutil.AgentResponse{RunID: "plr_cea87922"}, nil, false); changed || r.Passed {
			t.Errorf("an unparseable narrative changed the result: %+v", r)
		}
	})
}

// A single-hop run (the narrative has no later hops) gets nothing added, even
// if the triage findings happen to contain a keyword.
func TestApplyChainEvidenceFromGateway_SingleHopRun(t *testing.T) {
	srv := narrativeServer(t, http.StatusOK, `{"incident_id":"plr_cea87922","triage":{"findings":"pg_control is mentioned"}}`)
	r := firstHopResult(t)
	if hops, changed := applyChainEvidenceFromGateway(srv.URL, "", chainTestFault(), &r, testutil.AgentResponse{RunID: "plr_cea87922"}, nil, false); changed || hops != 0 || r.Passed {
		t.Errorf("a single-hop narrative changed the result: hops=%d changed=%v %+v", hops, changed, r)
	}
}

// The wire format decodes into the fields the chain text reads, and only the
// primary hypothesis of a hop is counted: a rejected alternative that
// happens to say "corrupt" must not satisfy a keyword.
func TestChainDiagnosticText_PrimaryHypothesisOnly(t *testing.T) {
	srv := narrativeServer(t, http.StatusOK, chainNarrativeJSON)
	n, err := fetchIncidentNarrative(srv.URL, "", "plr_cea87922")
	if err != nil {
		t.Fatal(err)
	}
	text, hops := chainDiagnosticText(n)
	if hops != 2 {
		t.Errorf("hops = %d, want 2", hops)
	}
	for _, want := range []string{"single_instance=true", "missing the pg_control file", "pg_control missing after a storage fault", "the most recent backup is healthy"} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(want)) {
			t.Errorf("chain text is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "disk full corrupted the WAL") {
		t.Errorf("a non-primary hypothesis leaked into the chain text:\n%s", text)
	}
	if strings.Contains(text, "Restore from backup") {
		t.Errorf("the remediation hop leaked into the chain text:\n%s", text)
	}
}

// A chain that ended at an escalation with no remediation hop still counts.
func TestChainDiagnosticText_EscalationsWithoutRemediation(t *testing.T) {
	n := &incidentNarrative{}
	n.Escalations = []narrativeEscalationHop{{Findings: "pg_control is missing"}}
	text, hops := chainDiagnosticText(n)
	if hops != 1 || !strings.Contains(text, "pg_control") {
		t.Errorf("got %q, %d hops", text, hops)
	}
}

// --- tool evidence across the chain -----------------------------------------

// The fault expects the two tools the later hops run. The entry hop (the DB
// agent) ran neither: in the 2026-10-25 Kubernetes run it called
// check_connection and create_incident_bundle only, and the fault scored
// "tools 0%" on a chain that ran get_host_logs and get_pgbackrest_status.
func chainToolsFault() Failure {
	f := chainTestFault()
	f.Evaluation.ExpectedTools = []string{"get_host_logs", "get_pgbackrest_status"}
	return f
}

// firstHopStructured is the entry hop's response as the gateway returned it:
// structured tool calls naming only what the DB agent ran.
func firstHopStructured() testutil.AgentResponse {
	return testutil.AgentResponse{
		Text:  chainFirstHop,
		RunID: "plr_cea87922",
		ToolCalls: []testutil.ToolCallResult{
			{Name: "check_connection", Success: false},
			{Name: "create_incident_bundle", Success: true},
		},
	}
}

// chainServer serves the incident narrative and the per-trace tool_execution
// events (trace id -> JSON array) the way the gateway's governance endpoint
// does, and counts event requests.
func chainServer(t *testing.T, narrative string, events map[string]string, eventRequests *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/incidents/plr_cea87922":
			_, _ = w.Write([]byte(narrative))
		case "/api/v1/governance/events":
			if eventRequests != nil {
				*eventRequests++
			}
			body, ok := events[r.URL.Query().Get("trace_id")]
			if !ok {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

var chainEvents = map[string]string{
	"tr_h1": `[{"event_type":"tool_execution","tool":{"name":"check_connection","error":"connection refused"}},
	           {"event_type":"tool_execution","tool":{"name":"create_incident_bundle"}}]`,
	"tr_h2": `[{"event_type":"tool_execution","tool":{"name":"check_host"}},
	           {"event_type":"tool_execution","tool":{"name":"get_host_logs"}}]`,
	"tr_h3": `[{"event_type":"tool_execution","tool":{"name":"get_pgbackrest_status"}}]`,
	"tr_h4": `[{"event_type":"tool_execution","tool":{"name":"get_pgbackrest_status"}},
	           {"event_type":"tool_execution","tool":{"name":"restore_from_backup"}},
	           {"event_type":"tool_execution","tool":{"name":"check_host","error":"no pod"}}]`,
}

func TestApplyChainEvidenceFromGateway_ToolsRunByLaterHops(t *testing.T) {
	srv := chainServer(t, chainNarrativeJSON, chainEvents, nil)
	f := chainToolsFault()
	resp := firstHopStructured()
	r := Evaluate(f, resp)
	r.ResponseText = resp.Text
	if r.ToolScore != 0 || r.ToolEvidenceMode != "structured" {
		t.Fatalf("precondition: the entry hop alone must score 0 tools in structured mode, got %.2f (%s)", r.ToolScore, r.ToolEvidenceMode)
	}

	_, changed := applyChainEvidenceFromGateway(srv.URL, "", f, &r, resp, nil, false)
	if !changed {
		t.Fatal("nothing changed")
	}
	if r.ToolScore != 1 || !r.ToolEvidence {
		t.Errorf("ToolScore = %.2f ToolEvidence = %v, want 1 and true: get_host_logs ran in hop 2 and get_pgbackrest_status in hop 3", r.ToolScore, r.ToolEvidence)
	}
	if r.ToolEvidenceMode != "structured+chain" {
		t.Errorf("ToolEvidenceMode = %q, want structured+chain so a report shows where the evidence came from", r.ToolEvidenceMode)
	}
	if !r.Passed || r.Score < 0.6 {
		t.Errorf("Passed=%v Score=%.2f, want a pass", r.Passed, r.Score)
	}
}

// A tool that errored in the later hop is not evidence that it ran: the
// post-restore check_host in hop 4 failed because the pod was gone.
func TestApplyChainToolEvidence_AnErroredToolDoesNotCount(t *testing.T) {
	f := chainTestFault()
	f.Evaluation.ExpectedTools = []string{"check_host"}
	srv := chainServer(t, chainNarrativeJSON, map[string]string{
		"tr_h1": `[]`, "tr_h2": `[]`, "tr_h3": `[]`,
		"tr_h4": `[{"event_type":"tool_execution","tool":{"name":"check_host","error":"no pod"}}]`,
	}, nil)
	resp := firstHopStructured()
	r := Evaluate(f, resp)
	r.ResponseText = resp.Text
	applyChainEvidenceFromGateway(srv.URL, "", f, &r, resp, nil, false)
	if r.ToolScore != 0 {
		t.Errorf("ToolScore = %.2f, want 0: the only check_host call errored", r.ToolScore)
	}
}

// What the first hop itself ran still counts alongside the chain's.
func TestApplyChainToolEvidence_UnionOfTheFirstHopAndTheChain(t *testing.T) {
	f := chainTestFault()
	f.Evaluation.ExpectedTools = []string{"check_connection", "get_host_logs"}
	resp := firstHopStructured()
	resp.ToolCalls[0].Success = true // check_connection succeeded in the entry hop
	r := Evaluate(f, resp)
	r.ResponseText = resp.Text
	if r.ToolScore != 0.5 {
		t.Fatalf("precondition: the entry hop found one of two tools, got %.2f", r.ToolScore)
	}
	if !ApplyChainToolEvidence(f, &r, resp, nil, []string{"get_host_logs"}, false) {
		t.Fatal("nothing changed")
	}
	if r.ToolScore != 1 {
		t.Errorf("ToolScore = %.2f, want 1: check_connection from hop 1 plus get_host_logs from the chain", r.ToolScore)
	}
}

// Unreadable event trails can only leave the score where it was.
func TestApplyChainEvidenceFromGateway_ToolEventsUnavailable(t *testing.T) {
	srv := chainServer(t, chainNarrativeJSON, map[string]string{}, nil) // every events request answers 500
	f := chainToolsFault()
	resp := firstHopStructured()
	r := Evaluate(f, resp)
	r.ResponseText = resp.Text
	applyChainEvidenceFromGateway(srv.URL, "", f, &r, resp, nil, false)
	if r.ToolScore != 0 {
		t.Errorf("ToolScore = %.2f after unreadable tool events, want it unchanged at 0", r.ToolScore)
	}
}

// A run that never left its first hop has no later hops to ask about.
func TestApplyChainEvidenceFromGateway_SingleHopRunFetchesNoToolEvents(t *testing.T) {
	requests := 0
	srv := chainServer(t, `{"incident_id":"plr_cea87922","triage":{"trace_id":"tr_h1","findings":"x"}}`, chainEvents, &requests)
	f := chainToolsFault()
	resp := firstHopStructured()
	r := Evaluate(f, resp)
	r.ResponseText = resp.Text
	applyChainEvidenceFromGateway(srv.URL, "", f, &r, resp, nil, false)
	if requests != 0 {
		t.Errorf("a single-hop run made %d event requests, want 0", requests)
	}
}

func TestApplyChainToolEvidence_NeverLowersAndHonoursTheGates(t *testing.T) {
	f := chainToolsFault()
	resp := firstHopStructured()
	r := Evaluate(f, resp)
	r.ResponseText = resp.Text
	r.EvidenceRequiredButUnconfirmed = true // an evidence gate already failed this result
	r.Passed = false
	ApplyChainToolEvidence(f, &r, resp, nil, []string{"get_host_logs", "get_pgbackrest_status"}, false)
	if r.Passed {
		t.Error("chain tool evidence passed a result an evidence gate had failed")
	}
	before := r
	if ApplyChainToolEvidence(f, &r, resp, nil, nil, false) || r.Score != before.Score {
		t.Error("an empty tool list changed the result")
	}
	if r.ToolScore < before.ToolScore {
		t.Error("a score was lowered")
	}
}

// The request the harness makes for a hop's tool calls: the gateway's
// governance events endpoint, filtered to the hop's trace and to tool
// executions, with the harness's API key.
func TestFetchChainToolNames_RequestShape(t *testing.T) {
	var gotAuth, gotType, gotTrace, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		gotType, gotTrace = r.URL.Query().Get("event_type"), r.URL.Query().Get("trace_id")
		_, _ = w.Write([]byte(`[{"event_type":"tool_execution","tool":{"name":"get_host_logs"}}]`))
	}))
	defer srv.Close()

	names := fetchChainToolNames(srv.URL, "k3y", []string{"tr a&b"})
	if len(names) != 1 || names[0] != "get_host_logs" {
		t.Errorf("names = %v, want [get_host_logs]", names)
	}
	if gotPath != "/api/v1/governance/events" || gotType != "tool_execution" || gotTrace != "tr a&b" || gotAuth != "Bearer k3y" {
		t.Errorf("request = %s event_type=%q trace_id=%q auth=%q, want the governance events path, tool_execution, the (escaped) trace id and the bearer key",
			gotPath, gotType, gotTrace, gotAuth)
	}
}

// Tools are de-duplicated across hops, in first-seen order.
func TestFetchChainToolNames_DeduplicatesAcrossHops(t *testing.T) {
	srv := chainServer(t, chainNarrativeJSON, chainEvents, nil)
	names := fetchChainToolNames(srv.URL, "", []string{"tr_h1", "tr_h3", "tr_h4"})
	want := []string{"create_incident_bundle", "get_pgbackrest_status", "restore_from_backup"} // check_connection and check_host errored
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("names = %v, want %v", names, want)
	}
}

// A gateway response with only text (no structured tool calls) still gets the
// union: what the first hop's text shows plus what the chain ran.
func TestApplyChainToolEvidence_TextModeUnion(t *testing.T) {
	f := chainTestFault()
	f.Evaluation.ExpectedTools = []string{"get_host_logs", "get_pgbackrest_status"}
	resp := testutil.AgentResponse{Text: "I called get_pgbackrest_status: stanza main, status ok, backup_stale=false."}
	r := Evaluate(f, resp)
	r.ResponseText = resp.Text
	if r.ToolScore != 0.5 {
		t.Fatalf("precondition: the entry hop's text shows one of two tools, got %.2f (%s)", r.ToolScore, r.ToolEvidenceMode)
	}
	if !ApplyChainToolEvidence(f, &r, resp, nil, []string{"get_host_logs"}, false) {
		t.Fatal("nothing changed")
	}
	if r.ToolScore != 1 {
		t.Errorf("ToolScore = %.2f, want 1: the text showed get_pgbackrest_status, the chain ran get_host_logs", r.ToolScore)
	}
}
