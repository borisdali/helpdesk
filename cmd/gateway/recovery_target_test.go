package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2aclient"
	"gopkg.in/yaml.v3"

	"helpdesk/internal/audit"
	"helpdesk/playbooks"
)

func TestRecoveryTargetSpec_Validate(t *testing.T) {
	tests := []struct {
		name    string
		spec    *RecoveryTargetSpec
		wantErr string
	}{
		{"utc time", &RecoveryTargetSpec{"time", "2026-10-27T14:05:00Z"}, ""},
		{"offset time", &RecoveryTargetSpec{"time", "2026-10-27T16:05:00+02:00"}, ""},
		{"lsn", &RecoveryTargetSpec{"lsn", "0/3000028"}, ""},
		{"lsn upper", &RecoveryTargetSpec{"lsn", "1A/FF"}, ""},
		{"nil", nil, "required"},
		{"no type", &RecoveryTargetSpec{"", "0/1"}, "type is required"},
		{"xid", &RecoveryTargetSpec{"xid", "5"}, "not supported"},
		{"name", &RecoveryTargetSpec{"name", "before_drop"}, "not supported"},
		{"time without a zone", &RecoveryTargetSpec{"time", "2026-10-27 14:05:00"}, "RFC3339"},
		{"time, free text", &RecoveryTargetSpec{"time", "just before the drop"}, "RFC3339"},
		{"lsn without a slash", &RecoveryTargetSpec{"lsn", "3000028"}, "not an LSN"},
		{"lsn too long", &RecoveryTargetSpec{"lsn", "123456789/1"}, "not an LSN"},
		// Shell syntax never gets as far as the audit trail or the agent.
		{"injection in time", &RecoveryTargetSpec{"time", "2026-10-27T14:05:00Z'; touch /tmp/x; '"}, "RFC3339"},
		{"injection in lsn", &RecoveryTargetSpec{"lsn", "0/1; touch /tmp/x"}, "not an LSN"},
		{"newline in lsn", &RecoveryTargetSpec{"lsn", "0/1\nx"}, "not an LSN"},
		{"long time", &RecoveryTargetSpec{"time", strings.Repeat("2", 60)}, "too long"},
	}
	for _, tc := range tests {
		err := tc.spec.Validate()
		if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.wantErr)
		}
	}
	s := &RecoveryTargetSpec{"time", "  2026-10-27T14:05:00Z "}
	if err := s.Validate(); err != nil || s.Value != "2026-10-27T14:05:00Z" {
		t.Errorf("a padded value should be trimmed: %+v %v", s, err)
	}
}

func TestRecoveryApprovalFromGate(t *testing.T) {
	good := ProceedEscalationRequest{
		RecoveryTarget:      &RecoveryTargetSpec{"time", "2026-10-27T14:05:00Z"},
		PlanDigest:          "0123456789abcdef",
		AcknowledgeDataLoss: true,
	}
	with := func(f func(r *ProceedEscalationRequest)) ProceedEscalationRequest {
		r := good
		spec := *good.RecoveryTarget
		r.RecoveryTarget = &spec
		f(&r)
		return r
	}
	tests := []struct {
		name       string
		req        ProceedEscalationRequest
		stored     string
		wantStatus int
		wantMsg    string
	}{
		{"complete", good, "", 0, ""},
		{"restates the run's own target", good, "time=2026-10-27T14:05:00Z", 0, ""},
		{"no target", with(func(r *ProceedEscalationRequest) { r.RecoveryTarget = nil }), "", 400, "recovery target"},
		{"bad target", with(func(r *ProceedEscalationRequest) { r.RecoveryTarget.Value = "soon" }), "", 400, "RFC3339"},
		{"no digest", with(func(r *ProceedEscalationRequest) { r.PlanDigest = "" }), "", 400, "plan_digest"},
		{"short digest", with(func(r *ProceedEscalationRequest) { r.PlanDigest = "0123" }), "", 400, "plan_digest"},
		{"upper-case digest", with(func(r *ProceedEscalationRequest) { r.PlanDigest = "0123456789ABCDEF" }), "", 400, "plan_digest"},
		{"digest with shell syntax", with(func(r *ProceedEscalationRequest) { r.PlanDigest = "0123456789abc;ls" }), "", 400, "plan_digest"},
		{"no acknowledgement", with(func(r *ProceedEscalationRequest) { r.AcknowledgeDataLoss = false }), "", 400, "acknowledge_data_loss"},
		{"another target than the run's", good, "time=2026-10-27T13:00:00Z", 409, "cannot be changed"},
		{"another type than the run's", good, "lsn=0/3000028", 409, "cannot be changed"},
	}
	for _, tc := range tests {
		ap, status, msg := recoveryApprovalFromGate(tc.req, tc.stored, "alice")
		if status != tc.wantStatus || (tc.wantMsg == "") != (msg == "") || !strings.Contains(msg, tc.wantMsg) {
			t.Errorf("%s: status=%d msg=%q, want %d %q", tc.name, status, msg, tc.wantStatus, tc.wantMsg)
		}
		if tc.wantStatus != 0 {
			if ap != nil {
				t.Errorf("%s: refused, but an approval was returned: %+v", tc.name, ap)
			}
			continue
		}
		if !ap.Approved() || ap.ApprovedBy != "alice" || ap.PlanDigest != "0123456789abcdef" || ap.Value != "2026-10-27T14:05:00Z" {
			t.Errorf("%s: approval = %+v", tc.name, ap)
		}
	}
}

func TestRecoveryApprovalMeta(t *testing.T) {
	if recoveryApprovalMeta(nil) != nil {
		t.Error("nil approval must give no metadata")
	}
	planOnly := recoveryApprovalMeta((&RecoveryTargetSpec{"lsn", "0/3000028"}).planOnly())
	if planOnly["type"] != "lsn" || planOnly["value"] != "0/3000028" || planOnly["plan_digest"] != nil || planOnly["acknowledged"] != nil || planOnly["approved_by"] != nil {
		t.Errorf("a target-only approval must carry nothing that approves: %v", planOnly)
	}
	full := recoveryApprovalMeta(&audit.RecoveryApproval{Type: "time", Value: "v", PlanDigest: "d", ApprovedBy: "alice", Acknowledged: true})
	if full["plan_digest"] != "d" || full["approved_by"] != "alice" || full["acknowledged"] != true {
		t.Errorf("full approval = %v", full)
	}
}

// ── handlers ─────────────────────────────────────────────────────────────────

// recoveryAuditd is an auditd stand-in for the point-in-time tests: playbooks
// by id and series, runs by id, and a record of what the gateway wrote.
type recoveryAuditd struct {
	*httptest.Server
	mu        sync.Mutex
	runBodies []map[string]any // POST .../runs bodies, in order
	patches   int
	// next maps a run to the run that continued from it (prior_run_id), for
	// gates raised deep in a chain.
	next map[string]*audit.PlaybookRun
}

func newRecoveryAuditd(t *testing.T, runs map[string]*audit.PlaybookRun, byID, bySeries map[string]*audit.Playbook) *recoveryAuditd {
	t.Helper()
	m := &recoveryAuditd{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && p == "/v1/fleet/playbooks":
			if pb, ok := bySeries[r.URL.Query().Get("series_id")]; ok {
				json.NewEncoder(w).Encode(map[string]any{"playbooks": []*audit.Playbook{pb}}) //nolint:errcheck
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"playbooks": []*audit.Playbook{}}) //nolint:errcheck
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/v1/fleet/playbooks/") && !strings.HasSuffix(p, "/runs"):
			if pb, ok := byID[strings.TrimPrefix(p, "/v1/fleet/playbooks/")]; ok {
				json.NewEncoder(w).Encode(pb) //nolint:errcheck
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && p == "/v1/fleet/playbook-runs":
			if n, ok := m.next[r.URL.Query().Get("prior_run_id")]; ok {
				json.NewEncoder(w).Encode(map[string]any{"runs": []*audit.PlaybookRun{n}}) //nolint:errcheck
				return
			}
			w.Write([]byte(`{"runs":[]}`)) //nolint:errcheck
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/v1/fleet/playbook-runs/"):
			if run, ok := runs[strings.TrimPrefix(p, "/v1/fleet/playbook-runs/")]; ok {
				json.NewEncoder(w).Encode(run) //nolint:errcheck
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/runs"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
			m.mu.Lock()
			m.runBodies = append(m.runBodies, body)
			n := len(m.runBodies)
			m.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"run_id": "plr_rec" + string(rune('0'+n))}) //nolint:errcheck
		case r.Method == http.MethodPost && strings.Contains(p, "/v1/incidents"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(audit.Incident{IncidentID: "inc_rec01"}) //nolint:errcheck
		case r.Method == http.MethodGet && strings.Contains(p, "/v1/incidents/by-run/"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPatch:
			m.mu.Lock()
			m.patches++
			m.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Write([]byte("[]")) //nolint:errcheck
		}
	}))
	t.Cleanup(m.Close)
	return m
}

func (m *recoveryAuditd) startedRuns() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]map[string]any(nil), m.runBodies...)
}

func (m *recoveryAuditd) patchCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.patches
}

// addCapturingAgent registers a mock A2A agent that answers text and records
// the request it was sent.
func addCapturingAgent(t *testing.T, gw *Gateway, name, text string) *promptCapture {
	t.Helper()
	capture := &promptCapture{}
	_, card := mockA2AServerCapturing(t, name, text, capture)
	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatalf("A2A client for %s: %v", name, err)
	}
	gw.clients[name] = client
	return capture
}

// sentRecoveryApproval returns the recovery_approval metadata of the A2A
// message an agent was sent, or nil.
func sentRecoveryApproval(t *testing.T, c *promptCapture) map[string]any {
	t.Helper()
	var req struct {
		Params struct {
			Message struct {
				Metadata map[string]any `json:"metadata"`
			} `json:"message"`
		} `json:"params"`
	}
	if body := c.get(); body != "" {
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("A2A request body: %v: %s", err, body)
		}
	}
	m, _ := req.Params.Message.Metadata["recovery_approval"].(map[string]any)
	return m
}

var _ = a2a.TransportProtocolJSONRPC // keep the import used if the helpers above change

func pitrPlaybooks() (triage, remediate *audit.Playbook) {
	triage = &audit.Playbook{PlaybookID: "pb_pitr_triage", SeriesID: "pbs_pgbackrest_pitr_triage", Name: "PITR triage",
		PlaybookType: "triage", ExecutionMode: "agent", AgentName: "pitr_sysadmin_agent", ApprovalMode: "manual", IsActive: true}
	remediate = &audit.Playbook{PlaybookID: "pb_pitr_remediate", SeriesID: pitrRemediateSeries, Name: "PITR remediate",
		PlaybookType: "remediation", ExecutionMode: "agent", AgentName: "pitr_sysadmin_agent", ApprovalMode: "manual", IsActive: true}
	return
}

func TestHandlePlaybookRun_RecoveryTarget_Invalid(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	auditd := newRecoveryAuditd(t, nil, map[string]*audit.Playbook{triage.PlaybookID: triage},
		map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	agent := addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: x\n")

	for _, target := range []string{
		`{"type":"time","value":"yesterday"}`,
		`{"type":"xid","value":"5"}`,
		`{"type":"lsn","value":"0/1; ls"}`,
		`{"type":"","value":"0/1"}`,
	} {
		rec := postPlaybookRun(t, gw, triage.PlaybookID, `{"connection_string":"postgres://localhost/test","recovery_target":`+target+`}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400; %s", target, rec.Code, rec.Body.String())
		}
	}
	if n := len(auditd.startedRuns()); n != 0 {
		t.Errorf("%d runs were recorded for invalid targets", n)
	}
	if agent.get() != "" {
		t.Error("the agent was called for an invalid target")
	}
}

// The playbook that restores can never be started directly, with or without
// a target in the body: only a gate approval produces the approval it needs.
func TestHandlePlaybookRun_PITRRemediateCannotBeStartedDirectly(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	auditd := newRecoveryAuditd(t, nil, map[string]*audit.Playbook{triage.PlaybookID: triage, remediate.PlaybookID: remediate},
		map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	agent := addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: x\n")

	for _, body := range []string{
		`{"connection_string":"postgres://localhost/test"}`,
		`{"connection_string":"postgres://localhost/test","approval_mode":"force","recovery_target":{"type":"time","value":"2026-10-27T14:05:00Z"}}`,
		`{"connection_string":"postgres://localhost/test","plan_digest":"0123456789abcdef","acknowledge_data_loss":true}`,
	} {
		rec := postPlaybookRun(t, gw, remediate.PlaybookID, body)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "gate") {
			t.Errorf("%s: got %d, want 409 naming the gate; %s", body, rec.Code, rec.Body.String())
		}
	}
	if len(auditd.startedRuns()) != 0 || agent.get() != "" {
		t.Errorf("the restore playbook started: %d runs, agent called=%v", len(auditd.startedRuns()), agent.get() != "")
	}
}

// A run with a target stores it and hands the agent the target alone: a
// request that merely has a target to plan carries nothing that approves.
func TestHandlePlaybookRun_RecoveryTarget_StoredAndSentWithoutApproval(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	auditd := newRecoveryAuditd(t, nil, map[string]*audit.Playbook{triage.PlaybookID: triage},
		map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	agent := addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: plan feasible\n")

	rec := postPlaybookRun(t, gw, triage.PlaybookID,
		`{"connection_string":"postgres://localhost/test","recovery_target":{"type":"time","value":"2026-10-27T14:05:00Z"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d; %s", rec.Code, rec.Body.String())
	}
	runs := auditd.startedRuns()
	if len(runs) != 1 || runs[0]["recovery_target"] != "time=2026-10-27T14:05:00Z" {
		t.Fatalf("started runs = %v, want one with recovery_target time=2026-10-27T14:05:00Z", runs)
	}
	got := sentRecoveryApproval(t, agent)
	if got["type"] != "time" || got["value"] != "2026-10-27T14:05:00Z" {
		t.Fatalf("recovery_approval sent to the agent = %v", got)
	}
	for _, k := range []string{"plan_digest", "approved_by", "acknowledged"} {
		if _, present := got[k]; present {
			t.Errorf("a target-only request carries %q: it must not look approved", k)
		}
	}

	// And a run without a target sends none at all.
	agent2 := addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: x\n")
	rec = postPlaybookRun(t, gw, triage.PlaybookID, `{"connection_string":"postgres://localhost/test"}`)
	if rec.Code != http.StatusOK || sentRecoveryApproval(t, agent2) != nil {
		t.Errorf("a run with no target: %d, recovery_approval=%v", rec.Code, sentRecoveryApproval(t, agent2))
	}
}

// Force mode (and skipping the trust gate) auto-chains through everything
// else, but not into a restore: the boundary into the PITR remediation
// playbook is always a human's gate.
func TestHandlePlaybookRun_ForceChainStopsAtPITRRemediate(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	other := &audit.Playbook{PlaybookID: "pb_other_rem", SeriesID: "pbs_other_remediate", Name: "Other remediation",
		PlaybookType: "remediation", ExecutionMode: "agent", AgentName: "other_remediate_agent", ApprovalMode: "auto", IsActive: true}
	run := func(t *testing.T, next string) (resp map[string]any, remediateAgent *promptCapture) {
		auditd := newRecoveryAuditd(t, nil, map[string]*audit.Playbook{triage.PlaybookID: triage},
			map[string]*audit.Playbook{remediate.SeriesID: remediate, other.SeriesID: other})
		gw := makePlaybookRunGateway(auditd.URL, nil)
		addCapturingAgent(t, gw, "pitr_sysadmin_agent",
			"HYPOTHESIS_1: data directory lost | CONFIDENCE: 0.95 | EVIDENCE: \"x\"\nROOT_CAUSE: HYPOTHESIS_1\nFINDINGS: plan feasible\nTRANSITION_TO: "+next+"\n")
		remediateAgent = addCapturingAgent(t, gw, "other_remediate_agent", "FINDINGS: done\n")
		rec := postPlaybookRun(t, gw, triage.PlaybookID,
			`{"connection_string":"postgres://localhost/test","approval_mode":"force","skip_trust_gate":true,"recovery_target":{"type":"time","value":"2026-10-27T14:05:00Z"}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d; %s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp, remediateAgent
	}

	resp, _ := run(t, pitrRemediateSeries)
	if resp["status"] != "pending_gate" || resp["gate_reason"] != gateReasonRecoveryTarget {
		t.Fatalf("status=%v gate_reason=%v, want pending_gate / %s; %v", resp["status"], resp["gate_reason"], gateReasonRecoveryTarget, resp)
	}
	if resp["chained_run_id"] != nil {
		t.Errorf("the restore hop ran: %v", resp["chained_run_id"])
	}

	// The control: the same chain into another remediation playbook is not
	// held for a recovery target.
	resp, remediateAgent := run(t, "pbs_other_remediate")
	if resp["gate_reason"] == gateReasonRecoveryTarget || remediateAgent.get() == "" {
		t.Errorf("an unrelated remediation was held for a recovery target: %v", resp)
	}
}

// ── the gate ─────────────────────────────────────────────────────────────────

func gatedPITRRun(target string) *audit.PlaybookRun {
	return &audit.PlaybookRun{
		RunID:            "plr_pitrgate01",
		PlaybookID:       "pb_pitr_triage",
		SeriesID:         "pbs_pgbackrest_pitr_triage",
		Outcome:          audit.OutcomeGatePending,
		TransitionedTo:   pitrRemediateSeries,
		ConnectionString: "postgres://localhost/test",
		FindingsSummary:  "plan feasible",
		RecoveryTarget:   target,
	}
}

const pitrApprovalBody = `{"resolution":"approved","resolved_by":"alice","approval_mode":"auto",` +
	`"recovery_target":{"type":"time","value":"2026-10-27T14:05:00Z"},"plan_digest":"0123456789abcdef","acknowledge_data_loss":true}`

func TestHandleProceedEscalation_PITR_RefusalsChangeNothing(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	run := gatedPITRRun("time=2026-10-27T14:05:00Z")
	auditd := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{run.RunID: run},
		map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	agent := addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: restored\n")

	cases := []struct {
		name string
		body string
		code int
	}{
		{"nothing but the resolution", `{"resolution":"approved","resolved_by":"alice","approval_mode":"auto"}`, 400},
		{"no digest", `{"resolution":"approved","resolved_by":"alice","recovery_target":{"type":"time","value":"2026-10-27T14:05:00Z"},"acknowledge_data_loss":true}`, 400},
		{"no acknowledgement", `{"resolution":"approved","resolved_by":"alice","recovery_target":{"type":"time","value":"2026-10-27T14:05:00Z"},"plan_digest":"0123456789abcdef"}`, 400},
		{"another target than the run's", `{"resolution":"approved","resolved_by":"alice","recovery_target":{"type":"time","value":"2026-10-27T13:00:00Z"},"plan_digest":"0123456789abcdef","acknowledge_data_loss":true}`, 409},
		{"force with nothing else", `{"resolution":"approved","resolved_by":"alice","approval_mode":"force"}`, 400},
	}
	for _, c := range cases {
		rec := postProceedEscalation(t, gw, run.RunID, c.body)
		if rec.Code != c.code {
			t.Errorf("%s: got %d, want %d; %s", c.name, rec.Code, c.code, rec.Body.String())
		}
	}
	if auditd.patchCount() != 0 || len(auditd.startedRuns()) != 0 || agent.get() != "" {
		t.Errorf("a refused approval changed state: %d patches, %d runs started, agent called=%v",
			auditd.patchCount(), len(auditd.startedRuns()), agent.get() != "")
	}

	// Denying needs none of it.
	if rec := postProceedEscalation(t, gw, run.RunID, `{"resolution":"denied","resolved_by":"alice"}`); rec.Code != http.StatusOK {
		t.Errorf("denied: got %d; %s", rec.Code, rec.Body.String())
	}
}

func TestHandleProceedEscalation_PITR_ApprovedReachesTheAgent(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	run := gatedPITRRun("time=2026-10-27T14:05:00Z")
	auditd := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{run.RunID: run},
		map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	agent := addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: restored\n")

	rec := postProceedEscalation(t, gw, run.RunID, pitrApprovalBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d; %s", rec.Code, rec.Body.String())
	}
	got := sentRecoveryApproval(t, agent)
	if got["type"] != "time" || got["value"] != "2026-10-27T14:05:00Z" || got["plan_digest"] != "0123456789abcdef" ||
		got["approved_by"] != "alice" || got["acknowledged"] != true {
		t.Fatalf("recovery_approval sent to the agent = %v", got)
	}
	runs := auditd.startedRuns()
	if len(runs) != 1 || runs[0]["recovery_target"] != "time=2026-10-27T14:05:00Z" || runs[0]["series_id"] != pitrRemediateSeries {
		t.Errorf("remediation run recorded as %v", runs)
	}
}

// The approval may supply the target when the run had none (the triage was
// started without one), and it is then stored on the remediation run.
func TestHandleProceedEscalation_PITR_ApprovalSuppliesTheTarget(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	run := gatedPITRRun("")
	auditd := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{run.RunID: run},
		map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: restored\n")

	if rec := postProceedEscalation(t, gw, run.RunID, pitrApprovalBody); rec.Code != http.StatusOK {
		t.Fatalf("got %d; %s", rec.Code, rec.Body.String())
	}
	if runs := auditd.startedRuns(); len(runs) != 1 || runs[0]["recovery_target"] != "time=2026-10-27T14:05:00Z" {
		t.Errorf("remediation run recorded as %v", runs)
	}
}

// A hop that does not restore keeps the run's target (to plan) and does not
// ask for an approval it does not need.
func TestHandleProceedEscalation_OtherHopCarriesTheTargetOnly(t *testing.T) {
	triage, _ := pitrPlaybooks()
	other := &audit.Playbook{PlaybookID: "pb_other_rem", SeriesID: "pbs_other_remediate", Name: "Other remediation",
		PlaybookType: "remediation", ExecutionMode: "agent", AgentName: "other_remediate_agent", ApprovalMode: "auto", IsActive: true}
	run := gatedPITRRun("lsn=0/3000028")
	run.TransitionedTo = other.SeriesID
	auditd := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{run.RunID: run},
		map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{other.SeriesID: other})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	agent := addCapturingAgent(t, gw, "other_remediate_agent", "FINDINGS: done\n")

	if rec := postProceedEscalation(t, gw, run.RunID, `{"resolution":"approved","resolved_by":"alice","approval_mode":"auto"}`); rec.Code != http.StatusOK {
		t.Fatalf("got %d; %s", rec.Code, rec.Body.String())
	}
	got := sentRecoveryApproval(t, agent)
	if got["type"] != "lsn" || got["value"] != "0/3000028" || got["plan_digest"] != nil || got["acknowledged"] != nil {
		t.Errorf("recovery_approval = %v, want the target alone", got)
	}
	if runs := auditd.startedRuns(); len(runs) != 1 || runs[0]["recovery_target"] != "lsn=0/3000028" {
		t.Errorf("remediation run recorded as %v", runs)
	}
}

// The Decision Hub resolves a gate through the same checks: its fields reach
// proceed-escalation, and without them the gate does not open.
func TestResolveDecision_Gate_PITR(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	run := gatedPITRRun("time=2026-10-27T14:05:00Z")
	auditd := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{run.RunID: run},
		map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	agent := addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: restored\n")

	post := func(body string) *httptest.ResponseRecorder {
		mux := http.NewServeMux()
		gw.RegisterRoutes(mux)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/decisions/gate:"+run.RunID+"/resolve", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := post(`{"resolution":"approved","resolved_by":"alice","approval_mode":"auto"}`); rec.Code != http.StatusBadRequest || agent.get() != "" {
		t.Fatalf("hub approval without the recovery fields: %d; agent called=%v; %s", rec.Code, agent.get() != "", rec.Body.String())
	}
	if rec := post(pitrApprovalBody); rec.Code != http.StatusOK {
		t.Fatalf("hub approval with the recovery fields: %d; %s", rec.Code, rec.Body.String())
	}
	if got := sentRecoveryApproval(t, agent); got["approved_by"] != "alice" || got["plan_digest"] != "0123456789abcdef" {
		t.Errorf("recovery_approval = %v", got)
	}
}

// The gate keys on a series name written in Go; if the shipped playbook were
// renamed or reshaped the gate would silently stop guarding anything.
func TestPITRRemediateSeries_MatchesTheShippedPlaybook(t *testing.T) {
	b, err := playbooks.FS.ReadFile("pgbackrest-pitr-remediate.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var pb struct {
		SeriesID     string `yaml:"series_id"`
		PlaybookType string `yaml:"playbook_type"`
		EntryPoint   bool   `yaml:"entry_point"`
	}
	if err := yaml.Unmarshal(b, &pb); err != nil {
		t.Fatal(err)
	}
	if pb.SeriesID != pitrRemediateSeries || !needsRecoveryApproval(pb.SeriesID) {
		t.Errorf("series_id %q, gate guards %q", pb.SeriesID, pitrRemediateSeries)
	}
	if pb.PlaybookType != "remediation" || pb.EntryPoint {
		t.Errorf("the restore playbook is %q entry_point=%v", pb.PlaybookType, pb.EntryPoint)
	}
	if needsRecoveryApproval("pbs_pgbackrest_restore_remediate") || needsRecoveryApproval("") {
		t.Error("the gate must guard only the point-in-time restore")
	}
}

// A gate raised several hops deep is approved against the hop it was raised
// on (the leaf), not the first run: the leaf's own target is the one that is
// immutable, and the primary's is the fallback.
func TestHandleProceedEscalation_PITR_DeepChain_LeafTargetWins(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	primary := gatedPITRRun("")
	primary.TransitionedTo = "" // the primary's own signal is its first hop's, already done
	primary.EscalatedTo = "pbs_sysadmin_host_triage"
	leaf := &audit.PlaybookRun{RunID: "plr_pitrleaf01", PriorRunID: primary.RunID, SeriesID: "pbs_sysadmin_host_triage",
		Outcome: audit.OutcomeGatePending, TransitionedTo: pitrRemediateSeries, ConnectionString: primary.ConnectionString,
		FindingsSummary: "plan feasible", RecoveryTarget: "time=2026-10-27T13:00:00Z"}

	for _, tc := range []struct {
		name     string
		body     string
		wantCode int
	}{
		{"the leaf's target is restated", strings.Replace(pitrApprovalBody, "14:05:00Z", "13:00:00Z", 1), http.StatusOK},
		{"another target than the leaf's", pitrApprovalBody, http.StatusConflict},
	} {
		auditd := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{primary.RunID: primary, leaf.RunID: leaf},
			map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
		auditd.next = map[string]*audit.PlaybookRun{primary.RunID: leaf}
		gw := makePlaybookRunGateway(auditd.URL, nil)
		addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: restored\n")
		if rec := postProceedEscalation(t, gw, primary.RunID, tc.body); rec.Code != tc.wantCode {
			t.Errorf("%s: got %d, want %d; %s", tc.name, rec.Code, tc.wantCode, rec.Body.String())
		}
	}
}

// Choosing "force" at the gate is allowed: a person is approving, with the
// target, the digest and the acknowledgement. What force can never do is
// reach the gate without them (see ForceChainStopsAtPITRRemediate).
func TestHandleProceedEscalation_PITR_ForceAtTheGate(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	run := gatedPITRRun("time=2026-10-27T14:05:00Z")
	auditd := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{run.RunID: run},
		map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	agent := addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: restored\n")

	body := strings.Replace(pitrApprovalBody, `"approval_mode":"auto"`, `"approval_mode":"force"`, 1)
	if rec := postProceedEscalation(t, gw, run.RunID, body); rec.Code != http.StatusOK {
		t.Fatalf("got %d; %s", rec.Code, rec.Body.String())
	}
	if got := sentRecoveryApproval(t, agent); !(&audit.RecoveryApproval{Type: "time", Value: "x", PlanDigest: "d", ApprovedBy: "a", Acknowledged: got["acknowledged"] == true}).Approved() {
		t.Errorf("recovery_approval = %v", got)
	}
	// ...and force without the fields is still refused (RefusalsChangeNothing).
	if rec := postProceedEscalation(t, gw, run.RunID, `{"resolution":"approved","resolved_by":"alice","approval_mode":"force"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("force without the recovery fields: got %d", rec.Code)
	}
}

// A hop that is auto-chained from a run with a target (anything but the
// restore) carries the target to plan, and records it on its own run.
func TestHandlePlaybookRun_AutoChain_CarriesTheTargetToTheNextHop(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	other := &audit.Playbook{PlaybookID: "pb_other_rem", SeriesID: "pbs_other_remediate", Name: "Other remediation",
		PlaybookType: "remediation", ExecutionMode: "agent", AgentName: "other_remediate_agent", ApprovalMode: "auto", IsActive: true}
	auditd := newRecoveryAuditd(t, nil, map[string]*audit.Playbook{triage.PlaybookID: triage},
		map[string]*audit.Playbook{remediate.SeriesID: remediate, other.SeriesID: other})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	addCapturingAgent(t, gw, "pitr_sysadmin_agent",
		"HYPOTHESIS_1: x | CONFIDENCE: 0.95 | EVIDENCE: \"x\"\nROOT_CAUSE: HYPOTHESIS_1\nFINDINGS: ok\nTRANSITION_TO: pbs_other_remediate\n")
	next := addCapturingAgent(t, gw, "other_remediate_agent", "FINDINGS: done\n")

	rec := postPlaybookRun(t, gw, triage.PlaybookID,
		`{"connection_string":"postgres://localhost/test","approval_mode":"force","skip_trust_gate":true,"recovery_target":{"type":"lsn","value":"0/3000028"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d; %s", rec.Code, rec.Body.String())
	}
	got := sentRecoveryApproval(t, next)
	if got["type"] != "lsn" || got["value"] != "0/3000028" || got["plan_digest"] != nil || got["acknowledged"] != nil {
		t.Errorf("the chained hop was sent %v, want the target alone", got)
	}
	runs := auditd.startedRuns()
	if len(runs) != 2 || runs[0]["recovery_target"] != "lsn=0/3000028" || runs[1]["recovery_target"] != "lsn=0/3000028" {
		t.Errorf("runs recorded as %v, want the target on both", runs)
	}
}

// The gate_acknowledged event is the audit record of who approved what: it
// names the target, the digest and the acknowledgement.
func TestRecordGateAcknowledged_RecordsThePITRApproval(t *testing.T) {
	store, err := audit.NewStore(audit.StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	g := &Gateway{auditor: audit.NewGatewayAuditor(store)}
	run := &audit.PlaybookRun{RunID: "plr_pitraudit01", TransitionedTo: pitrRemediateSeries}

	ap, status, msg := recoveryApprovalFromGate(ProceedEscalationRequest{
		RecoveryTarget: &RecoveryTargetSpec{"time", "2026-10-27T14:05:00Z"}, PlanDigest: "0123456789abcdef", AcknowledgeDataLoss: true,
	}, "", "alice")
	if status != 0 {
		t.Fatalf("%d %s", status, msg)
	}
	extra := []string{"recovery_target: " + audit.FormatRecoveryTarget(ap.Type, ap.Value), "plan_digest: " + ap.PlanDigest, "operator acknowledged that everything committed after the target is discarded"}
	g.recordGateAcknowledged(context.Background(), run, "alice", "approved", "auto", "", "", extra...)

	events, err := store.Query(context.Background(), audit.QueryOptions{TraceIDPrefix: run.RunID, EventTypes: []audit.EventType{audit.EventTypeGateAcknowledged}})
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %d, %v", len(events), err)
	}
	chain := strings.Join(events[0].Decision.ReasoningChain, "\n")
	for _, want := range []string{"operator alice acknowledged", "recovery_target: time=2026-10-27T14:05:00Z", "plan_digest: 0123456789abcdef", "everything committed after the target is discarded"} {
		if !strings.Contains(chain, want) {
			t.Errorf("the audit record lacks %q:\n%s", want, chain)
		}
	}
}

// ── allow_stop_serving ───────────────────────────────────────────────────────

// Stopping a running instance is a separate authorization from approving the
// target: it is only ever the operator's explicit flag.
func TestRecoveryApprovalFromGate_StopServingIsSeparate(t *testing.T) {
	base := ProceedEscalationRequest{
		RecoveryTarget: &RecoveryTargetSpec{"time", "2026-10-27T14:05:00Z"}, PlanDigest: "0123456789abcdef", AcknowledgeDataLoss: true,
	}
	ap, status, msg := recoveryApprovalFromGate(base, "", "alice")
	if status != 0 || ap.StopServing || !ap.Approved() {
		t.Fatalf("approval without the flag: %+v %d %s", ap, status, msg)
	}
	base.AllowStopServing = true
	ap, status, _ = recoveryApprovalFromGate(base, "", "alice")
	if status != 0 || !ap.StopServing {
		t.Fatalf("approval with the flag: %+v", ap)
	}
	// The flag alone approves nothing.
	if _, status, _ := recoveryApprovalFromGate(ProceedEscalationRequest{AllowStopServing: true}, "", "alice"); status != http.StatusBadRequest {
		t.Errorf("allow_stop_serving alone: status %d, want 400", status)
	}

	if m := recoveryApprovalMeta(&audit.RecoveryApproval{Type: "time", Value: "v", PlanDigest: "d", ApprovedBy: "a", Acknowledged: true}); m["stop_serving"] != nil {
		t.Errorf("an approval that does not allow it carries stop_serving: %v", m)
	}
	if m := recoveryApprovalMeta(ap); m["stop_serving"] != true {
		t.Errorf("metadata = %v", m)
	}
	lines := strings.Join(recoveryAuditLines(ap), "\n")
	if !strings.Contains(lines, "authorized stopping the running instance") {
		t.Errorf("audit lines = %s", lines)
	}
	if strings.Contains(strings.Join(recoveryAuditLines(&audit.RecoveryApproval{Type: "time", Value: "v", PlanDigest: "d"}), "\n"), "stopping") {
		t.Error("an approval that does not allow stopping says it does")
	}
}

func TestHandleProceedEscalation_PITR_StopServingReachesTheAgentOnlyWhenAllowed(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"allowed", strings.Replace(pitrApprovalBody, `"acknowledge_data_loss":true`, `"acknowledge_data_loss":true,"allow_stop_serving":true`, 1), true},
		{"not allowed", pitrApprovalBody, false},
	} {
		run := gatedPITRRun("time=2026-10-27T14:05:00Z")
		auditd := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{run.RunID: run},
			map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
		gw := makePlaybookRunGateway(auditd.URL, nil)
		agent := addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: restored\n")
		if rec := postProceedEscalation(t, gw, run.RunID, tc.body); rec.Code != http.StatusOK {
			t.Fatalf("%s: got %d; %s", tc.name, rec.Code, rec.Body.String())
		}
		got := sentRecoveryApproval(t, agent)
		if (got["stop_serving"] == true) != tc.want {
			t.Errorf("%s: recovery_approval = %v", tc.name, got)
		}
	}
}

func TestResolveDecision_Gate_PITR_StopServing(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	run := gatedPITRRun("time=2026-10-27T14:05:00Z")
	auditd := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{run.RunID: run},
		map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	agent := addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: restored\n")
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	body := strings.Replace(pitrApprovalBody, `"acknowledge_data_loss":true`, `"acknowledge_data_loss":true,"allow_stop_serving":true`, 1)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/decisions/gate:"+run.RunID+"/resolve", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d; %s", rec.Code, rec.Body.String())
	}
	if got := sentRecoveryApproval(t, agent); got["stop_serving"] != true {
		t.Errorf("the hub dropped allow_stop_serving: %v", got)
	}
}
