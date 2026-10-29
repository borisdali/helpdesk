package faultlib

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"helpdesk/testing/testutil"
)

func TestParsePITRMarker(t *testing.T) {
	good := "noise\nPITR_TARGET time=2026-10-28T17:11:10Z lsn=0/3000028\nmore noise\n"
	tests := []struct {
		name, out, typ string
		want           string
		wantErr        string
	}{
		{"time", good, "time", "2026-10-28T17:11:10Z", ""},
		{"lsn", good, "lsn", "0/3000028", ""},
		{"the newest line wins", "PITR_TARGET time=2026-10-28T10:00:00Z\nPITR_TARGET time=2026-10-28T11:00:00Z\n", "time", "2026-10-28T11:00:00Z", ""},
		{"token order does not matter", "PITR_TARGET lsn=1/0 time=2026-10-28T10:00:00Z", "time", "2026-10-28T10:00:00Z", ""},
		{"indented and CRLF", "  PITR_TARGET time=2026-10-28T10:00:00Z\r\n", "time", "2026-10-28T10:00:00Z", ""},
		{"no marker", "all quiet\n", "time", "", "printed no PITR_TARGET line"},
		{"empty", "", "time", "", "printed no PITR_TARGET line"},
		{"a marker word inside another line is not a marker", "echo PITR_TARGET time=2026-10-28T10:00:00Z\n", "time", "", "printed no PITR_TARGET line"},
		{"the other type is missing", "PITR_TARGET time=2026-10-28T10:00:00Z\n", "lsn", "", "no lsn= value"},
		{"empty value", "PITR_TARGET time= lsn=0/1\n", "time", "", "no time= value"},
		{"unsupported type", good, "xid", "", "not time or lsn"},
	}
	for _, tc := range tests {
		got, err := ParsePITRMarker(tc.out, tc.typ)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got.Value != tc.want || got.Type != tc.typ {
			t.Errorf("%s: got %+v, %v; want %s", tc.name, got, err, tc.want)
		}
	}
}

func TestPITRSpec_Type(t *testing.T) {
	var nilSpec *PITRSpec
	if nilSpec.Type() != "time" || (&PITRSpec{}).Type() != "time" || (&PITRSpec{TargetType: "lsn"}).Type() != "lsn" {
		t.Error("the target type defaults to time")
	}
}

// ── the injector keeps what a script prints ──────────────────────────────────

func TestInjector_KeepsShellExecOutput(t *testing.T) {
	inj := NewInjector(&HarnessConfig{})
	f := Failure{
		Inject:   InjectSpec{Type: "shell_exec", ScriptInline: "echo hello; echo 'PITR_TARGET time=2026-10-28T10:00:00Z lsn=0/1'"},
		Teardown: InjectSpec{Type: "shell_exec", ScriptInline: "echo teardown-output"},
	}
	if err := inj.Inject(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if out := inj.InjectOutput(); !strings.Contains(out, "PITR_TARGET time=2026-10-28T10:00:00Z") || !strings.Contains(out, "hello") {
		t.Errorf("inject output = %q", out)
	}
	// Teardown's output is not the inject's.
	if err := inj.Teardown(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if out := inj.InjectOutput(); strings.Contains(out, "teardown-output") || !strings.Contains(out, "PITR_TARGET") {
		t.Errorf("after teardown: %q", out)
	}
	// A new injection starts from nothing: a failing one leaves no stale target.
	bad := Failure{Inject: InjectSpec{Type: "shell_exec", ScriptInline: "exit 3"}}
	if err := inj.Inject(context.Background(), bad); err == nil {
		t.Fatal("expected the failing script to fail")
	}
	if out := inj.InjectOutput(); out != "" {
		t.Errorf("a failed injection kept the previous output: %q", out)
	}
}

// ── the cycle carries it to the runner ───────────────────────────────────────

type outputInjector struct{ out string }

func (o *outputInjector) Inject(context.Context, Failure) error   { return nil }
func (o *outputInjector) Teardown(context.Context, Failure) error { return nil }
func (o *outputInjector) InjectOutput() string                    { return o.out }

type plainInjector struct{}

func (plainInjector) Inject(context.Context, Failure) error   { return nil }
func (plainInjector) Teardown(context.Context, Failure) error { return nil }

type ctxCapturingRunner struct{ got string }

func (c *ctxCapturingRunner) Run(ctx context.Context, f Failure) (resp testutil.AgentResponse) {
	c.got = InjectOutputFromContext(ctx)
	return
}

func TestRunFaultCycle_CarriesTheInjectOutput(t *testing.T) {
	runner := &ctxCapturingRunner{}
	res := RunFaultCycle(context.Background(), &outputInjector{out: "PITR_TARGET time=2026-10-28T10:00:00Z"}, runner, Failure{ID: "x"}, "trace1", nil)
	if res.InjectOutput != "PITR_TARGET time=2026-10-28T10:00:00Z" || runner.got != res.InjectOutput {
		t.Errorf("result %q, runner saw %q", res.InjectOutput, runner.got)
	}
	// An injector that keeps nothing is not an error and carries nothing.
	runner = &ctxCapturingRunner{}
	res = RunFaultCycle(context.Background(), plainInjector{}, runner, Failure{ID: "x"}, "", nil)
	if res.InjectOutput != "" || runner.got != "" {
		t.Errorf("result %q, runner saw %q", res.InjectOutput, runner.got)
	}
}

// ── the triage run carries the target ────────────────────────────────────────

func TestRunViaPlaybook_SendsTheRecoveryTarget(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{"playbooks": []map[string]any{{"playbook_id": "pb_pitr"}}}) //nolint:errcheck
			return
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)                                         //nolint:errcheck
		json.NewEncoder(w).Encode(map[string]any{"text": "plan feasible"}) //nolint:errcheck
	}))
	defer srv.Close()
	r := newTestRunner(t, srv.URL, true)

	pitr := Failure{ID: "db-pitr-dropped-table", Category: "database", Prompt: "plan it", Timeout: "30s",
		DiagnosisPlaybookSeriesID: "pbs_pgbackrest_pitr_triage", PITR: &PITRSpec{}}
	ctx := WithInjectOutput(context.Background(), "x\nPITR_TARGET time=2026-10-28T17:11:10Z lsn=0/3000028\n")
	if resp := r.runViaPlaybook(ctx, pitr); resp.Error != nil {
		t.Fatalf("run: %v", resp.Error)
	}
	target, _ := body["recovery_target"].(map[string]any)
	if target["type"] != "time" || target["value"] != "2026-10-28T17:11:10Z" {
		t.Errorf("recovery_target = %v, want the inject script's time", body["recovery_target"])
	}

	// An lsn fault sends the lsn.
	body = nil
	pitr.PITR = &PITRSpec{TargetType: "lsn"}
	if resp := r.runViaPlaybook(ctx, pitr); resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if target, _ := body["recovery_target"].(map[string]any); target["type"] != "lsn" || target["value"] != "0/3000028" {
		t.Errorf("recovery_target = %v", body["recovery_target"])
	}

	// A fault with no PITR spec sends none, whatever the context holds.
	body = nil
	plain := Failure{ID: "db-x", Category: "database", Prompt: "p", Timeout: "30s", DiagnosisPlaybookSeriesID: "pbs_pgbackrest_pitr_triage"}
	if resp := r.runViaPlaybook(ctx, plain); resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if _, present := body["recovery_target"]; present {
		t.Errorf("a non-PITR fault sent a recovery_target: %v", body["recovery_target"])
	}

	// A PITR fault whose script printed no target does not run the triage at all.
	body = nil
	if resp := r.runViaPlaybook(context.Background(), pitr); resp.Error == nil || !strings.Contains(resp.Error.Error(), "PITR_TARGET") {
		t.Errorf("a missing target should fail the run: %v", resp.Error)
	}
	if body != nil {
		t.Error("the triage was started without a target")
	}
}

// ── approving the gate ───────────────────────────────────────────────────────

type pitrFakes struct {
	agent, gateway         *httptest.Server
	planCalls, gateCalls   int
	planBody, gateBody     map[string]any
	planAuth, gateAuth     string
	planStatus, planDigest string
}

func newPITRFakes(t *testing.T) *pitrFakes {
	t.Helper()
	f := &pitrFakes{planStatus: "feasible", planDigest: "0123456789abcdef"}
	f.agent = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.planCalls++
		f.planAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/tool/plan_point_in_time_recovery" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &f.planBody) //nolint:errcheck
		plan, _ := json.Marshal(map[string]any{"status": f.planStatus, "detail": "because", "plan_digest": f.planDigest, "instance_serving": true, "archive_flush_required": true})
		json.NewEncoder(w).Encode(map[string]any{"output": string(plan)}) //nolint:errcheck
	}))
	f.gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.gateCalls++
		f.gateAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		f.gateBody = nil
		json.Unmarshal(raw, &f.gateBody)                                  //nolint:errcheck
		json.NewEncoder(w).Encode(ApproveRunResponse{Status: "complete"}) //nolint:errcheck
	}))
	t.Cleanup(func() { f.agent.Close(); f.gateway.Close() })
	return f
}

func (f *pitrFakes) remediator() *Remediator {
	return NewRemediator(&HarnessConfig{GatewayURL: f.gateway.URL, GatewayAPIKey: "key-1", SysadminAgentURL: f.agent.URL})
}

// The agent's endpoint takes its own key when there is one.
func TestRemediator_PITRPlanUsesTheSysadminKey(t *testing.T) {
	fk := newPITRFakes(t)
	r := NewRemediator(&HarnessConfig{GatewayURL: fk.gateway.URL, GatewayAPIKey: "gateway-key", SysadminAPIKey: "agent-key", SysadminAgentURL: fk.agent.URL})
	r.SetPITR(droppedTable, droppedTableOut) //nolint:errcheck
	if _, err := r.ProceedEscalation(context.Background(), "plr_g", ProceedEscalationRequest{Resolution: "approved"}); err != nil {
		t.Fatal(err)
	}
	if fk.planAuth != "Bearer agent-key" || fk.gateAuth != "Bearer gateway-key" {
		t.Errorf("plan call used %q, gateway call used %q: each endpoint takes its own key", fk.planAuth, fk.gateAuth)
	}
}

var droppedTable = Failure{ID: "db-pitr-dropped-table", AgentConnOverride: "pgbackrest-db",
	PITR: &PITRSpec{TargetType: "time", AllowStopServing: true}}

const droppedTableOut = "PITR_TARGET time=2026-10-28T17:11:10Z lsn=0/3000028\n"

func TestRemediator_ApprovesTheReviewedPITRPlan(t *testing.T) {
	fk := newPITRFakes(t)
	r := fk.remediator()
	if err := r.SetPITR(droppedTable, droppedTableOut); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ProceedEscalation(context.Background(), "plr_gate01", ProceedEscalationRequest{
		Resolution: "approved", ResolvedBy: "faulttest", ApprovalMode: "auto", Purpose: "remediation"}); err != nil {
		t.Fatal(err)
	}
	// The plan was read from the agent, for the fault's server and target.
	args, _ := fk.planBody["args"].(map[string]any)
	if fk.planAuth != "Bearer key-1" || args["target"] != "pgbackrest-db" || args["recovery_target_type"] != "time" || args["recovery_target_value"] != "2026-10-28T17:11:10Z" {
		t.Errorf("plan request: auth %q body %v", fk.planAuth, fk.planBody)
	}
	if fk.planBody["trace_id"] == "" || fk.planBody["purpose"] != "diagnostic" {
		t.Errorf("the direct tool call needs a trace_id and a purpose: %v", fk.planBody)
	}
	// ...and the approval carries exactly what the operator reviewed.
	target, _ := fk.gateBody["recovery_target"].(map[string]any)
	if target["type"] != "time" || target["value"] != "2026-10-28T17:11:10Z" || fk.gateBody["plan_digest"] != "0123456789abcdef" ||
		fk.gateBody["acknowledge_data_loss"] != true || fk.gateBody["allow_stop_serving"] != true {
		t.Errorf("approval = %v", fk.gateBody)
	}
	if fk.gateBody["resolved_by"] != "faulttest" || fk.gateBody["approval_mode"] != "auto" {
		t.Errorf("the caller's own fields were lost: %v", fk.gateBody)
	}
}

func TestRemediator_PITRApprovalIsSeparateAndConditional(t *testing.T) {
	t.Run("a fault that leaves the database down does not authorize a stop", func(t *testing.T) {
		fk := newPITRFakes(t)
		r := fk.remediator()
		down := droppedTable
		down.PITR = &PITRSpec{TargetType: "time"}
		r.SetPITR(down, droppedTableOut)                                                                     //nolint:errcheck
		r.ProceedEscalation(context.Background(), "plr_g", ProceedEscalationRequest{Resolution: "approved"}) //nolint:errcheck
		if _, present := fk.gateBody["allow_stop_serving"]; present {
			t.Errorf("allow_stop_serving sent for a fault that did not ask for it: %v", fk.gateBody)
		}
	})
	t.Run("a denial carries nothing and reads no plan", func(t *testing.T) {
		fk := newPITRFakes(t)
		r := fk.remediator()
		r.SetPITR(droppedTable, droppedTableOut)                                                           //nolint:errcheck
		r.ProceedEscalation(context.Background(), "plr_g", ProceedEscalationRequest{Resolution: "denied"}) //nolint:errcheck
		if fk.planCalls != 0 || fk.gateBody["recovery_target"] != nil {
			t.Errorf("plan calls %d, body %v", fk.planCalls, fk.gateBody)
		}
	})
	t.Run("a request that already has a target is left alone", func(t *testing.T) {
		fk := newPITRFakes(t)
		r := fk.remediator()
		r.SetPITR(droppedTable, droppedTableOut) //nolint:errcheck
		mine := &PITRTarget{Type: "lsn", Value: "0/1"}
		r.ProceedEscalation(context.Background(), "plr_g", ProceedEscalationRequest{Resolution: "approved", RecoveryTarget: mine, PlanDigest: "aaaaaaaaaaaaaaaa"}) //nolint:errcheck
		if fk.planCalls != 0 || fk.gateBody["plan_digest"] != "aaaaaaaaaaaaaaaa" {
			t.Errorf("plan calls %d, body %v", fk.planCalls, fk.gateBody)
		}
	})
	t.Run("a fault that is not a PITR one is untouched, and SetPITR clears the previous fault's target", func(t *testing.T) {
		fk := newPITRFakes(t)
		r := fk.remediator()
		r.SetPITR(droppedTable, droppedTableOut)                                                             //nolint:errcheck
		r.SetPITR(Failure{ID: "db-other"}, "")                                                               //nolint:errcheck
		r.ProceedEscalation(context.Background(), "plr_g", ProceedEscalationRequest{Resolution: "approved"}) //nolint:errcheck
		if fk.planCalls != 0 || fk.gateBody["recovery_target"] != nil || fk.gateBody["plan_digest"] != nil || fk.gateBody["allow_stop_serving"] != nil {
			t.Errorf("plan calls %d, body %v", fk.planCalls, fk.gateBody)
		}
	})
}

// The operator does not approve a plan that is not feasible, and neither does
// the harness: no approval is sent.
func TestRemediator_RefusesToApproveAPlanThatIsNotFeasible(t *testing.T) {
	fk := newPITRFakes(t)
	fk.planStatus = "not_feasible"
	r := fk.remediator()
	r.SetPITR(droppedTable, droppedTableOut) //nolint:errcheck
	_, err := r.ProceedEscalation(context.Background(), "plr_g", ProceedEscalationRequest{Resolution: "approved"})
	if err == nil || !strings.Contains(err.Error(), "not feasible") || !strings.Contains(err.Error(), "because") {
		t.Fatalf("err = %v", err)
	}
	if fk.gateCalls != 0 {
		t.Error("the gate was approved on a plan that is not feasible")
	}

	// No digest in the answer is just as unusable.
	fk2 := newPITRFakes(t)
	fk2.planDigest = ""
	r2 := fk2.remediator()
	r2.SetPITR(droppedTable, droppedTableOut) //nolint:errcheck
	if _, err := r2.ProceedEscalation(context.Background(), "plr_g", ProceedEscalationRequest{Resolution: "approved"}); err == nil || fk2.gateCalls != 0 {
		t.Errorf("err = %v, gate calls %d", err, fk2.gateCalls)
	}
}

func TestRemediator_PITRSetupErrors(t *testing.T) {
	fk := newPITRFakes(t)
	r := fk.remediator()
	if err := r.SetPITR(droppedTable, "the script printed nothing useful\n"); err == nil || !strings.Contains(err.Error(), "db-pitr-dropped-table") || !strings.Contains(err.Error(), "PITR_TARGET") {
		t.Errorf("a missing target: %v", err)
	}
	// Without the sysadmin agent's URL the plan cannot be read, and saying so
	// beats approving blind.
	noAgent := NewRemediator(&HarnessConfig{GatewayURL: fk.gateway.URL})
	noAgent.SetPITR(droppedTable, droppedTableOut) //nolint:errcheck
	if _, err := noAgent.ProceedEscalation(context.Background(), "plr_g", ProceedEscalationRequest{Resolution: "approved"}); err == nil || !strings.Contains(err.Error(), "FAULTTEST_SYSADMIN_AGENT_URL") {
		t.Errorf("err = %v", err)
	}
	if fk.gateCalls != 0 {
		t.Error("a gate was approved without a plan")
	}
	// The agent rejecting the call (a missing key) is an error, not an approval.
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{"error": "authentication required"}) //nolint:errcheck
	}))
	defer rejecting.Close()
	r3 := NewRemediator(&HarnessConfig{GatewayURL: fk.gateway.URL, SysadminAgentURL: rejecting.URL})
	r3.SetPITR(droppedTable, droppedTableOut) //nolint:errcheck
	if _, err := r3.ProceedEscalation(context.Background(), "plr_g", ProceedEscalationRequest{Resolution: "approved"}); err == nil || !strings.Contains(err.Error(), "authentication required") {
		t.Errorf("err = %v", err)
	}
	_ = time.Second
}

// ── the catalog's fault ──────────────────────────────────────────────────────

func TestCatalog_DroppedTableFaults_AreWellFormed(t *testing.T) {
	cat, err := LoadBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"db-pitr-dropped-table", "db-pitr-dropped-table-k8s"} {
		var f Failure
		for _, c := range cat.Failures {
			if c.ID == id {
				f = c
			}
		}
		if f.ID == "" {
			t.Fatalf("%s is missing from the catalog", id)
		}
		t.Run(id, func(t *testing.T) { checkDroppedTableFault(t, f) })
	}
}

func checkDroppedTableFault(t *testing.T, f Failure) {
	t.Helper()
	if f.PITR == nil || f.PITR.Type() != "time" || !f.PITR.AllowStopServing {
		t.Errorf("pitr spec = %+v: the dropped table leaves the database serving, so the approval must allow stopping it", f.PITR)
	}
	wantConn := map[string]string{"db-pitr-dropped-table": "pgbackrest-db", "db-pitr-dropped-table-k8s": "pgbackrest-k8s-destruct"}[f.ID]
	if f.DiagnosisPlaybookSeriesID != "pbs_pgbackrest_pitr_triage" || f.AgentConnOverride != wantConn || f.DirectAgent != "sysadmin" {
		t.Errorf("routing = %q %q %q, want conn %q", f.DiagnosisPlaybookSeriesID, f.AgentConnOverride, f.DirectAgent, wantConn)
	}
	if !strings.Contains(f.Inject.ScriptInline, "echo \"PITR_TARGET time=$T lsn=$LSN\"") {
		t.Error("the inject script does not print its PITR_TARGET line")
	}
	// The target is printed AFTER the damage is made would be wrong: it must be
	// noted before it, and the damage must come after.
	script := f.Inject.ScriptInline
	if strings.Index(script, "T=$(Q") >= strings.Index(script, "DROP TABLE pitr_orders") || strings.Index(script, "DROP TABLE pitr_orders") >= strings.Index(script, "echo \"PITR_TARGET") {
		t.Error("the target must be read before the table is dropped")
	}
	if strings.Contains(f.Prompt, "2026") || strings.Contains(strings.ToLower(f.Prompt), "lsn=") {
		t.Errorf("the prompt names a target: the harness supplies it, the model must not be handed one: %q", f.Prompt)
	}
	// The check of the rows themselves: verify_sql where the harness can reach
	// the database, verify_exec (kubectl exec) where it cannot.
	check := f.Remediation.VerifySQL + f.Remediation.VerifyExec
	if f.Remediation.VerifySQL == "" && f.Remediation.VerifyExec == "" {
		t.Error("no verify_sql or verify_exec: the rows are never checked")
	}
	for _, want := range []string{"pitr_after_drop", "pitr_orders", "= 5"} {
		if !strings.Contains(check, want) {
			t.Errorf("the recovery check lacks %q: %s", want, check)
		}
	}
	if f.TimeoutDuration() < 300*time.Second {
		t.Errorf("timeout %s is too short for a flush, a stop, a restore and a restart", f.TimeoutDuration())
	}
}

func TestPITRSkipReason(t *testing.T) {
	pitr := Failure{ID: "p", PITR: &PITRSpec{}}
	plain := Failure{ID: "x"}
	full := HarnessConfig{ViaGateway: true, RemediateEnabled: true, GateEscalation: true, SysadminAgentURL: "http://a"}

	if got := PITRSkipReason(plain, HarnessConfig{}); got != "" {
		t.Errorf("a fault that is not a PITR one is never skipped, got %q", got)
	}
	if got := PITRSkipReason(pitr, full); got != "" {
		t.Errorf("a fully configured run must not skip, got %q", got)
	}
	for name, mut := range map[string]func(*HarnessConfig){
		"via-gateway":     func(c *HarnessConfig) { c.ViaGateway = false },
		"remediate":       func(c *HarnessConfig) { c.RemediateEnabled = false },
		"gate-escalation": func(c *HarnessConfig) { c.GateEscalation = false },
		"sysadmin agent":  func(c *HarnessConfig) { c.SysadminAgentURL = "" },
	} {
		c := full
		mut(&c)
		got := PITRSkipReason(pitr, c)
		if got == "" {
			t.Errorf("without %s the fault cannot run and must be skipped", name)
		}
	}
}
