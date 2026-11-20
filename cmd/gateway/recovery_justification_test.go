package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"helpdesk/internal/audit"
)

// The justification and the ticket an operator gives at a point-in-time gate
// (v0.36): recorded with the approval, bounded and cleaned here, required only
// when the gateway is set to require one for a breach acknowledgement.

func justifiedRequest(reason, ticket string, breach bool) ProceedEscalationRequest {
	return ProceedEscalationRequest{
		RecoveryTarget: &RecoveryTargetSpec{"time", "2026-10-27T14:05:00Z"}, PlanDigest: "0123456789abcdef",
		AcknowledgeDataLoss: true, AcknowledgeObjectiveBreach: breach, Reason: reason, Ticket: ticket,
	}
}

func TestRecoveryApprovalFromGate_JustificationAndTicket(t *testing.T) {
	const why = "customer-facing batch corrupted the ledger; the CTO accepted the loss on the call"
	tests := []struct {
		name     string
		req      ProceedEscalationRequest
		require  bool
		wantCode int
		wantWhy  string
		wantTkt  string
		wantMsg  string
	}{
		{"neither given, nothing required", justifiedRequest("", "", true), false, 0, "", "", ""},
		{"both given are recorded", justifiedRequest(why, "INC-1234", true), false, 0, why, "INC-1234", ""},
		{"surrounding space is trimmed", justifiedRequest("  "+why+"  ", " CHG0012345 ", false), false, 0, why, "CHG0012345", ""},
		{"a repository reference is a ticket", justifiedRequest("", "org/repo#12", false), false, 0, "", "org/repo#12", ""},
		{"required and given", justifiedRequest(why, "", true), true, 0, why, "", ""},
		{"required, a breach acknowledged, no reason", justifiedRequest("", "INC-1", true), true, http.StatusBadRequest, "", "", "needs a justification"},
		{"required, a breach acknowledged, a reason too short to be one", justifiedRequest("ok", "", true), true, http.StatusBadRequest, "", "", "needs a justification"},
		{"required, a breach acknowledged, only spaces", justifiedRequest("           ", "", true), true, http.StatusBadRequest, "", "", "needs a justification"},
		{"required but no breach acknowledged: nothing to justify", justifiedRequest("", "", false), true, 0, "", "", ""},
		{"not required, a breach acknowledged, no reason", justifiedRequest("", "", true), false, 0, "", "", ""},
		{"a ticket with prose in it", justifiedRequest("", "INC-1234 because the CEO said", false), false, http.StatusBadRequest, "", "", "not a ticket reference"},
		{"a ticket with a quote in it", justifiedRequest("", `INC-1"; DROP`, false), false, http.StatusBadRequest, "", "", "not a ticket reference"},
		{"a ticket that starts with punctuation", justifiedRequest("", "-INC", false), false, http.StatusBadRequest, "", "", "not a ticket reference"},
		{"a ticket that is too long", justifiedRequest("", "T"+strings.Repeat("0", 64), false), false, http.StatusBadRequest, "", "", "not a ticket reference"},
		{"a reason that is too long", justifiedRequest(strings.Repeat("é", 1001), "", false), false, http.StatusBadRequest, "", "", "too long"},
		{"a reason at the limit, counted in characters not bytes", justifiedRequest(strings.Repeat("é", 1000), "", false), false, 0, strings.Repeat("é", 1000), "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ap, status, msg := recoveryApprovalFromGate(tc.req, "", "alice", tc.require)
			if status != tc.wantCode {
				t.Fatalf("status %d (%s), want %d", status, msg, tc.wantCode)
			}
			if tc.wantCode != 0 {
				if ap != nil || !strings.Contains(msg, tc.wantMsg) {
					t.Errorf("ap=%v msg=%q, want a refusal saying %q", ap, msg, tc.wantMsg)
				}
				return
			}
			if ap.Justification != tc.wantWhy || ap.Ticket != tc.wantTkt {
				t.Errorf("justification %q ticket %q, want %q and %q", ap.Justification, ap.Ticket, tc.wantWhy, tc.wantTkt)
			}
		})
	}
}

// People paste what is on their screen: a password in the prose never reaches the
// audit trail, whichever form it takes.
func TestRecoveryApprovalFromGate_JustificationIsRedacted(t *testing.T) {
	for _, why := range []string{
		"restored per runbook, conn host=db user=app password=hunter2 dbname=x",
		"restored per runbook, conn postgres://app:hunter2@db:5432/x",
	} {
		ap, status, msg := recoveryApprovalFromGate(justifiedRequest(why, "", true), "", "alice", true)
		if status != 0 {
			t.Fatalf("%d %s", status, msg)
		}
		if strings.Contains(ap.Justification, "hunter2") {
			t.Errorf("a password reached the justification: %q", ap.Justification)
		}
		if !strings.Contains(ap.Justification, "restored per runbook") {
			t.Errorf("the rest of the justification was lost: %q", ap.Justification)
		}
	}
}

// Through the real handler: both are in the gate decision (the hash-covered
// record), the ticket is in the gate_acknowledged lines, and a required
// justification that is missing refuses the approval before any state changes.
func TestHandleProceedEscalation_PITR_RecordsJustificationAndTicket(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	const why = "batch corrupted the ledger; accepted by the on-call lead"
	body := strings.Replace(pitrApprovalBody, `"acknowledge_data_loss":true`,
		`"acknowledge_data_loss":true,"allow_stop_serving":true,"acknowledge_objective_breach":true,`+
			`"reason":"`+why+`","ticket":"INC-1234"`, 1)

	run := gatedPITRRun("time=2026-10-27T14:05:00Z")
	auditd := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{run.RunID: run},
		map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	store, withAuditor := gateDecisionStore(t)
	gw.auditor = withAuditor.auditor
	gw.requireBreachJustification = true
	addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: restored\n")
	if rec := postProceedEscalation(t, gw, run.RunID, body); rec.Code != http.StatusOK {
		t.Fatalf("approve: got %d; %s", rec.Code, rec.Body.String())
	}
	got := gateDecisions(t, store, run.RunID)
	if len(got) != 1 || got[0].Approval == nil {
		t.Fatalf("gate decisions: %+v", got)
	}
	if a := got[0].Approval; a.Justification != why || a.Ticket != "INC-1234" || !a.AcknowledgedBreach {
		t.Errorf("approval = %+v", a)
	}
}

func TestHandleProceedEscalation_PITR_MissingJustificationRefusesBeforeAnythingChanges(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	body := strings.Replace(pitrApprovalBody, `"acknowledge_data_loss":true`, `"acknowledge_data_loss":true,"acknowledge_objective_breach":true`, 1)

	run := gatedPITRRun("time=2026-10-27T14:05:00Z")
	auditd := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{run.RunID: run},
		map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	store, withAuditor := gateDecisionStore(t)
	gw.auditor = withAuditor.auditor
	gw.requireBreachJustification = true
	agent := addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: restored\n")

	rec := postProceedEscalation(t, gw, run.RunID, body)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "needs a justification") {
		t.Fatalf("got %d; %s", rec.Code, rec.Body.String())
	}
	if got := gateDecisions(t, store, run.RunID); len(got) != 0 {
		t.Errorf("a refused approval was recorded as a decision: %+v", got)
	}
	if agent.get() != "" {
		t.Errorf("the agent was called for a refused approval")
	}

	// The same approval with a reason is accepted: the gate was not used up by the refusal.
	ok := strings.Replace(body, `"acknowledge_objective_breach":true`, `"acknowledge_objective_breach":true,"reason":"accepted by the on-call lead"`, 1)
	if rec := postProceedEscalation(t, gw, run.RunID, ok); rec.Code != http.StatusOK {
		t.Fatalf("with a reason: got %d; %s", rec.Code, rec.Body.String())
	}
}

// The decision hub is a second way to approve the same gate, and must carry the
// reason and the ticket the way it carries the acknowledgement.
func TestResolveDecision_Gate_PITR_JustificationAndTicket(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	run := gatedPITRRun("time=2026-10-27T14:05:00Z")
	auditd := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{run.RunID: run},
		map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	store, withAuditor := gateDecisionStore(t)
	gw.auditor = withAuditor.auditor
	addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: restored\n")
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	body := strings.Replace(pitrApprovalBody, `"acknowledge_data_loss":true`,
		`"acknowledge_data_loss":true,"reason":"accepted by the on-call lead","ticket":"INC-77"`, 1)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/decisions/gate:"+run.RunID+"/resolve", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d; %s", rec.Code, rec.Body.String())
	}
	got := gateDecisions(t, store, run.RunID)
	if len(got) != 1 || got[0].Approval == nil || got[0].Approval.Justification != "accepted by the on-call lead" || got[0].Approval.Ticket != "INC-77" {
		t.Errorf("the hub dropped the reason or the ticket: %+v", got)
	}
}

// The text is shown in incident narratives and terminals: a newline must not be
// able to forge a line of the narrative, nor an escape sequence drive a terminal.
func TestCleanOperatorText(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "accepted by the on-call lead", "accepted by the on-call lead"},
		{"trimmed", "  accepted  ", "accepted"},
		{"a forged narrative line", "ok\n05:10:50  execution proceeded  within  nothing was lost", "ok 05:10:50 execution proceeded within nothing was lost"},
		{"carriage returns and tabs", "a\r\nb\tc", "a b c"},
		{"a terminal escape", "red \x1b[31mALERT\x1b[0m done", "red [31mALERT [0m done"},
		{"a unicode space", "a b c", "a b c"},
		{"only control characters", "\n\t\x00\x1b", ""},
		{"accents survive", "restauré après l'incident", "restauré après l'incident"},
	} {
		if got := cleanOperatorText(tc.in); got != tc.want {
			t.Errorf("%s: cleanOperatorText(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestRecoveryApprovalFromGate_JustificationIsOneLineAndLengthIsCountedAfterCleaning(t *testing.T) {
	ap, status, msg := recoveryApprovalFromGate(justifiedRequest("batch corrupted the ledger\n05:10:50  execution  proceeded", "", true), "", "alice", false)
	if status != 0 {
		t.Fatalf("%d %s", status, msg)
	}
	if strings.ContainsAny(ap.Justification, "\n\r\t") || ap.Justification != "batch corrupted the ledger 05:10:50 execution proceeded" {
		t.Errorf("justification = %q", ap.Justification)
	}
	// Twenty newlines around "ok" are not a justification.
	padded := "\n\n\n\n\n\n\n\n\n\nok\n\n\n\n\n\n\n\n\n\n"
	if _, status, _ := recoveryApprovalFromGate(justifiedRequest(padded, "", true), "", "alice", true); status != http.StatusBadRequest {
		t.Errorf("padding counted towards the minimum: status %d", status)
	}
}

// What the gate_acknowledged event records is the cleaned, redacted justification,
// not the raw reason: it is the same words, so it must not be the place a password
// or a forged line survives.
func TestHandleProceedEscalation_PITR_GateAcknowledgedRecordsTheCleanedReason(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	body := strings.Replace(pitrApprovalBody, `"acknowledge_data_loss":true`,
		`"acknowledge_data_loss":true,"reason":"restored per runbook, password=hunter2\n05:10:50 execution proceeded"`, 1)
	run := gatedPITRRun("time=2026-10-27T14:05:00Z")
	auditd := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{run.RunID: run},
		map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	store, withAuditor := gateDecisionStore(t)
	gw.auditor = withAuditor.auditor
	addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: restored\n")
	if rec := postProceedEscalation(t, gw, run.RunID, body); rec.Code != http.StatusOK {
		t.Fatalf("approve: got %d; %s", rec.Code, rec.Body.String())
	}
	events, err := store.Query(context.Background(), audit.QueryOptions{
		TraceIDPrefix: run.RunID, EventTypes: []audit.EventType{audit.EventTypeGateAcknowledged}})
	if err != nil || len(events) != 1 {
		t.Fatalf("gate_acknowledged events: %v %v", events, err)
	}
	ev := events[0]
	recorded := strings.Join(ev.Decision.ReasoningChain, "\n") + "\n" + ev.Output.Response
	if strings.Contains(recorded, "hunter2") {
		t.Errorf("the password reached the gate_acknowledged event:\n%s", recorded)
	}
	if !strings.Contains(recorded, "operator_reason: restored per runbook,") || strings.Contains(ev.Output.Response, "\n") {
		t.Errorf("the reason was not recorded cleaned:\n%s", recorded)
	}
}

// With the requirement on, the hub is held to it like the direct route.
func TestResolveDecision_Gate_PITR_RequiredJustification(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	run := gatedPITRRun("time=2026-10-27T14:05:00Z")
	auditd := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{run.RunID: run},
		map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	gw.requireBreachJustification = true
	addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: restored\n")
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	body := strings.Replace(pitrApprovalBody, `"acknowledge_data_loss":true`, `"acknowledge_data_loss":true,"acknowledge_objective_breach":true`, 1)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/decisions/gate:"+run.RunID+"/resolve", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "needs a justification") {
		t.Fatalf("got %d; %s", rec.Code, rec.Body.String())
	}
}

func TestRecoveryAuditLines_NamesTheTicket(t *testing.T) {
	ap := &audit.RecoveryApproval{Type: "time", Value: "2026-10-27T14:05:00Z", PlanDigest: "0123456789abcdef", Ticket: "INC-1234"}
	if !strings.Contains(strings.Join(recoveryAuditLines(ap), "\n"), "ticket: INC-1234") {
		t.Errorf("lines = %v", recoveryAuditLines(ap))
	}
	ap.Ticket = ""
	if strings.Contains(strings.Join(recoveryAuditLines(ap), "\n"), "ticket") {
		t.Errorf("a ticket is named when there is none: %v", recoveryAuditLines(ap))
	}
}
