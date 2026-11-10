package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"helpdesk/internal/client"
)

func goodOpts() pitrOpts {
	return pitrOpts{
		RunID: "plr_1", TargetType: "time", Target: "2026-10-28T14:05:00Z",
		PlanDigest: "3c7ae8cf0d79ffd9", AckDataLoss: true, AllowStop: true,
		ResolvedBy: "ops@example.com", ApprovalMode: "auto",
	}
}

func TestPITRApprovalBody_RequiresEveryField(t *testing.T) {
	cases := map[string]func(*pitrOpts){
		"run id":         func(o *pitrOpts) { o.RunID = "" },
		"type":           func(o *pitrOpts) { o.TargetType = "xid" },
		"target":         func(o *pitrOpts) { o.Target = "  " },
		"digest missing": func(o *pitrOpts) { o.PlanDigest = "" },
		"digest short":   func(o *pitrOpts) { o.PlanDigest = "3c7ae8" },
		"digest not hex": func(o *pitrOpts) { o.PlanDigest = "zzzzzzzzzzzzzzzz" },
		"data loss ack":  func(o *pitrOpts) { o.AckDataLoss = false },
	}
	for name, mut := range cases {
		o := goodOpts()
		mut(&o)
		if _, err := o.approvalBody(""); err == nil {
			t.Errorf("%s: approval must be refused before anything is sent", name)
		}
	}
	if _, err := goodOpts().approvalBody(""); err != nil {
		t.Fatalf("complete approval refused: %v", err)
	}
}

func TestRunPITRApprove_SendsWhatTheGatewayReads(t *testing.T) {
	var path, auth, user string
	var got map[string]any
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth, user = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-User")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(`{"status":"resolved"}`))
	}))
	defer gw.Close()
	cfg := client.Config{GatewayURL: gw.URL, APIKey: "k", UserID: "ops@example.com", Purpose: "remediation"}
	if err := runPITRApprove(context.Background(), cfg, goodOpts()); err != nil {
		t.Fatal(err)
	}
	if path != "/api/v1/fleet/playbook-runs/plr_1/proceed-escalation" || auth != "Bearer k" || user != "ops@example.com" {
		t.Errorf("request: path=%q auth=%q user=%q", path, auth, user)
	}
	rt, _ := got["recovery_target"].(map[string]any)
	if got["resolution"] != "approved" || rt["type"] != "time" || rt["value"] != "2026-10-28T14:05:00Z" ||
		got["plan_digest"] != "3c7ae8cf0d79ffd9" || got["acknowledge_data_loss"] != true ||
		got["allow_stop_serving"] != true || got["purpose"] != "remediation" {
		t.Errorf("body = %v", got)
	}
}

func TestRunPITRApprove_GatewayRefusalIsAnError(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"a recovery target cannot be changed once set"}`))
	}))
	defer gw.Close()
	err := runPITRApprove(context.Background(), client.Config{GatewayURL: gw.URL}, goodOpts())
	if err == nil || !strings.Contains(err.Error(), "409") || !strings.Contains(err.Error(), "cannot be changed") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunPITRApprove_InvalidSendsNothing(t *testing.T) {
	called := false
	gw := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer gw.Close()
	o := goodOpts()
	o.AckDataLoss = false
	if err := runPITRApprove(context.Background(), client.Config{GatewayURL: gw.URL}, o); err == nil || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func agentReturning(t *testing.T, plan string, gotBody *map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tool/plan_point_in_time_recovery" || r.Header.Get("Authorization") != "Bearer sk" {
			t.Errorf("path=%q auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, gotBody)
		out, _ := json.Marshal(map[string]string{"output": plan})
		_, _ = w.Write(out)
	}))
}

func TestFormatPITRPlan_FeasibleShowsTheApproveCommand(t *testing.T) {
	p := pitrPlan{Status: "feasible", BackupLabel: "B1", NormalizedTarget: "2026-10-28 14:05:00+00",
		DataLoss: "everything after", InstanceServing: true, ArchiveFlushRequired: true, PlanDigest: "3c7ae8cf0d79ffd9"}
	got := formatPITRPlan(p, goodOpts())
	for _, want := range []string{"B1", "SERVING", "Archive flush", "3c7ae8cf0d79ffd9",
		"--plan-digest 3c7ae8cf0d79ffd9", "--acknowledge-data-loss --allow-stop-serving", "everything after"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatPITRPlan_ShowsTheMeasuredLossAndWhatIsNotCounted(t *testing.T) {
	var p pitrPlan
	raw := `{"status":"feasible","plan_digest":"d1","data_loss_estimate":{"summary":"at most 21m 00s of history discarded","as_of":"2026-11-03T02:30:00Z","unmeasured":["the number of transactions","which writes were wanted"]}}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	got := formatPITRPlan(p, goodOpts())
	for _, want := range []string{"Estimated loss     : at most 21m 00s of history discarded", "Not counted        : the number of transactions; which writes were wanted"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(formatPITRPlan(pitrPlan{Status: "feasible", PlanDigest: "d1"}, goodOpts()), "Estimated loss") {
		t.Error("a plan without an estimate printed one")
	}
}

func TestFormatPITRPlan_ShowsTheRPOCheck_LoudWhenItMayBeExceeded(t *testing.T) {
	plan := func(status, detail string) pitrPlan {
		var p pitrPlan
		raw := `{"status":"feasible","plan_digest":"d1","data_loss_estimate":{"summary":"s","objective":{"status":"` + status + `","detail":"` + detail + `"}}}`
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, status := range []string{"exceeds", "may_exceed"} {
		got := formatPITRPlan(plan(status, "1h 18m 07s discarded against an RPO of 15m 00s"), goodOpts())
		if !strings.Contains(got, "RPO CHECK (!)") || !strings.Contains(got, status+": 1h 18m 07s discarded against an RPO of 15m 00s") {
			t.Errorf("%s is not shown loudly:\n%s", status, got)
		}
	}
	for _, status := range []string{"within", "not_comparable", "no_objective", "objective_invalid"} {
		got := formatPITRPlan(plan(status, "detail text"), goodOpts())
		if strings.Contains(got, "(!)") || !strings.Contains(got, "RPO check") || !strings.Contains(got, status+": detail text") {
			t.Errorf("%s:\n%s", status, got)
		}
	}
	// An agent without the check (an older one) prints no RPO line.
	if got := formatPITRPlan(pitrPlan{Status: "feasible", PlanDigest: "d1"}, goodOpts()); strings.Contains(got, "RPO") {
		t.Errorf("a plan with no comparison printed one:\n%s", got)
	}
}

// The breach acknowledgement is its own flag and its own request field, sent
// only when given.
func TestPITRApprovalBody_BreachAcknowledgementIsSeparateAndOptional(t *testing.T) {
	read := func(o pitrOpts) map[string]any {
		raw, err := o.approvalBody("")
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if _, present := read(goodOpts())["acknowledge_objective_breach"]; present {
		t.Error("an approval with nothing to acknowledge sent acknowledge_objective_breach")
	}
	o := goodOpts()
	o.AckBreach = true
	if got := read(o)["acknowledge_objective_breach"]; got != true {
		t.Errorf("acknowledge_objective_breach = %v", got)
	}
	// It is not implied by, and does not imply, the data-loss acknowledgement.
	o.AckDataLoss = false
	if _, err := o.approvalBody(""); err == nil {
		t.Error("the breach acknowledgement stood in for acknowledge-data-loss")
	}
}

func TestFormatPITRPlan_SaysWhatApprovingWillDemand(t *testing.T) {
	plan := func(status, gate string) pitrPlan {
		var p pitrPlan
		raw := `{"status":"feasible","plan_digest":"3c7ae8cf0d79ffd9","instance_serving":true,"data_loss_estimate":{"summary":"s","objective":{"status":"` +
			status + `","detail":"d","gate":"` + gate + `","enforcement":"acknowledge"}}}`
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	got := formatPITRPlan(plan("exceeds", "acknowledge"), goodOpts())
	for _, want := range []string{"Approving needs    : --acknowledge-objective-breach", "--acknowledge-data-loss --allow-stop-serving --acknowledge-objective-breach"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	// An objective that could not be read asks for the same acknowledgement, and
	// is shown as loudly as a breach: the check could not be made.
	got = formatPITRPlan(plan("objective_invalid", "acknowledge"), goodOpts())
	if !strings.Contains(got, "RPO CHECK (!)") || !strings.Contains(got, "--acknowledge-objective-breach") {
		t.Errorf("an unreadable objective under a strict enforcement:\n%s", got)
	}

	// Nothing demanded: the command is the usual one.
	got = formatPITRPlan(plan("within", ""), goodOpts())
	if strings.Contains(got, "objective-breach") || !strings.Contains(got, "--pitr-approve") {
		t.Errorf("a plan that demands nothing:\n%s", got)
	}

	// Refused: no command to approve it with, and it says why.
	got = formatPITRPlan(plan("exceeds", "refuse"), pitrOpts{TargetType: "time", Target: "2026-10-28T14:05:00Z"})
	if strings.Contains(got, "--pitr-approve") || !strings.Contains(got, "Approving          : REFUSED") || !strings.Contains(got, "Not approvable as it stands") {
		t.Errorf("a plan the restore would refuse still offers a command:\n%s", got)
	}
}

// The budget check is its own line, loud when it matters, and its gate shapes the
// approval command alone or together with the loss verdict's.
func TestFormatPITRPlan_ShowsTheBudgetCheckAndWhatItDemands(t *testing.T) {
	plan := func(lossGate, budgetStatus, budgetGate string, serving bool) pitrPlan {
		var p pitrPlan
		loss := ""
		if lossGate != "" {
			loss = `,"data_loss_estimate":{"summary":"s","objective":{"status":"exceeds","detail":"d","gate":"` + lossGate + `","enforcement":"acknowledge"}}`
		}
		raw := `{"status":"feasible","plan_digest":"3c7ae8cf0d79ffd9","instance_serving":` + map[bool]string{true: "true", false: "false"}[serving] + loss +
			`,"availability_budget":{"status":"` + budgetStatus + `","detail":"only 23m12s left","gate":"` + budgetGate + `","enforcement":"acknowledge","remaining_seconds":1392}}`
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// It may be exceeded and acknowledgement is demanded: loud, and the command carries the flag.
	got := formatPITRPlan(plan("", "may_exceed", "acknowledge", true), goodOpts())
	for _, want := range []string{"BUDGET CHECK (!)   : may_exceed: only 23m12s left",
		"Approving needs    : --acknowledge-objective-breach (this database's availability enforcement is acknowledge)",
		"--acknowledge-data-loss --allow-stop-serving --acknowledge-objective-breach"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "RPO CHECK") {
		t.Errorf("a plan with no loss estimate shows an RPO check:\n%s", got)
	}

	// Room in the budget: said quietly, and the command is the usual one.
	got = formatPITRPlan(plan("", "within", "", true), goodOpts())
	if !strings.Contains(got, "Budget check       : within") || strings.Contains(got, "BUDGET CHECK (!)") || strings.Contains(got, "objective-breach") {
		t.Errorf("room in the budget:\n%s", got)
	}

	// Both breach: one flag, shown once on the command, and both checks are on the page.
	got = formatPITRPlan(plan("acknowledge", "exceeds", "acknowledge", true), goodOpts())
	if strings.Count(got, "--acknowledge-objective-breach") < 1 || !strings.Contains(got, "RPO CHECK (!)") || !strings.Contains(got, "BUDGET CHECK (!)") {
		t.Errorf("both:\n%s", got)
	}
	cmd := got[strings.Index(got, "To approve exactly this plan"):]
	if strings.Count(cmd, "--acknowledge-objective-breach") != 1 {
		t.Errorf("the flag should appear once on the command:\n%s", cmd)
	}

	// Refused for the budget: no command, and it says why, in the budget's words.
	got = formatPITRPlan(plan("", "exceeds", "refuse", true), pitrOpts{TargetType: "time", Target: "2026-10-28T14:05:00Z"})
	if strings.Contains(got, "--pitr-approve") || !strings.Contains(got, "Approving          : REFUSED (this database's availability enforcement is refuse") ||
		!strings.Contains(got, "downtime budget is already spent") {
		t.Errorf("a budget refusal still offers a command:\n%s", got)
	}
	// An already-down instance asks instead, and the note says why.
	var down pitrPlan
	if err := json.Unmarshal([]byte(`{"status":"feasible","plan_digest":"3c7ae8cf0d79ffd9","instance_serving":false,"availability_budget":{"status":"exceeds","detail":"spent","gate":"acknowledge","enforcement":"refuse","gate_note":"the instance is already down"}}`), &down); err != nil {
		t.Fatal(err)
	}
	got = formatPITRPlan(down, goodOpts())
	if !strings.Contains(got, "Note               : the instance is already down") || !strings.Contains(got, "--acknowledge-objective-breach") || strings.Contains(got, "REFUSED") {
		t.Errorf("down instance:\n%s", got)
	}
	// A plan from an agent that reports no budget reads as it always did.
	var old pitrPlan
	_ = json.Unmarshal([]byte(`{"status":"feasible","plan_digest":"3c7ae8cf0d79ffd9"}`), &old)
	if got := formatPITRPlan(old, goodOpts()); strings.Contains(got, "udget") {
		t.Errorf("an older plan mentions a budget:\n%s", got)
	}
}

func TestFormatPITRPlan_InfeasibleOffersNoApproveCommand(t *testing.T) {
	got := formatPITRPlan(pitrPlan{Status: "not_feasible", Detail: "WAL gap", PlanDigest: "3c7ae8cf0d79ffd9"}, goodOpts())
	if strings.Contains(got, "--pitr-approve") || strings.Contains(got, "--plan-digest") || !strings.Contains(got, "WAL gap") {
		t.Errorf("an infeasible plan must not suggest approving it:\n%s", got)
	}
}

func TestRunPITRPlan_AsksTheAgentForThatTarget(t *testing.T) {
	var body map[string]any
	ag := agentReturning(t, `{"status":"feasible","plan_digest":"3c7ae8cf0d79ffd9"}`, &body)
	defer ag.Close()
	o := goodOpts()
	o.SysadminURL, o.SysadminKey, o.Server, o.Stanza = ag.URL, "sk", "pgbackrest-db", ""
	if err := runPITRPlan(context.Background(), client.Config{}, o); err != nil {
		t.Fatal(err)
	}
	args, _ := body["args"].(map[string]any)
	if args["target"] != "pgbackrest-db" || args["stanza"] != "main" ||
		args["recovery_target_type"] != "time" || args["recovery_target_value"] != "2026-10-28T14:05:00Z" ||
		body["purpose"] != "diagnostic" {
		t.Errorf("body = %v", body)
	}
}

func TestRunPITRPlan_NeedsItsInputs(t *testing.T) {
	for name, mut := range map[string]func(*pitrOpts){
		"url":    func(o *pitrOpts) { o.SysadminURL = "" },
		"server": func(o *pitrOpts) { o.Server = "" },
		"target": func(o *pitrOpts) { o.Target = "" },
	} {
		o := goodOpts()
		o.SysadminURL, o.Server = "http://x", "db"
		mut(&o)
		if err := runPITRPlan(context.Background(), client.Config{}, o); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestRunPITRPlan_AgentErrorSurfaces(t *testing.T) {
	ag := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer ag.Close()
	o := goodOpts()
	o.SysadminURL, o.Server = ag.URL, "db"
	if err := runPITRPlan(context.Background(), client.Config{}, o); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

// Every field a flag can set must arrive in the options, each from its own
// flag: distinct values catch a swapped pair, not just a dropped one.
func TestNewPITROpts_MapsEveryFlag(t *testing.T) {
	got := newPITROpts(pitrFlagValues{
		ApproveRunID: "plr_9", TargetType: "lsn", Target: "0/52000000", PlanDigest: "aaaaaaaaaaaaaaaa",
		AckDataLoss: true, AllowStop: true, AckBreach: true, User: "ops@example.com",
		SysadminURL: "http://agent", SysadminKey: "sk", Server: "db1", Stanza: "main2",
	})
	want := pitrOpts{
		RunID: "plr_9", TargetType: "lsn", Target: "0/52000000", PlanDigest: "aaaaaaaaaaaaaaaa",
		AckDataLoss: true, AllowStop: true, AckBreach: true, ResolvedBy: "ops@example.com", ApprovalMode: "auto",
		SysadminURL: "http://agent", SysadminKey: "sk", Server: "db1", Stanza: "main2",
	}
	if got != want {
		t.Errorf("newPITROpts =\n%+v\nwant\n%+v", got, want)
	}
	// The acknowledgements are never implied by anything else.
	z := newPITROpts(pitrFlagValues{ApproveRunID: "plr_9"})
	if z.AckDataLoss || z.AllowStop || z.AckBreach {
		t.Errorf("acknowledgements must default to false: %+v", z)
	}
}
