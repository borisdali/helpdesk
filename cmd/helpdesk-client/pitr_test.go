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
