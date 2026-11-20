package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"helpdesk/testing/faultlib"
)

// The harness, run for real against a plan that asks for an acknowledgement, sends
// an approval that a gateway requiring a justification accepts: the reason it
// gives is read by the gateway's own decoding and is long enough to count. A test
// of each side alone would pass if the field were renamed on one of them.
func TestFaultlibHarness_SatisfiesAGatewayThatRequiresJustification(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plan, _ := json.Marshal(map[string]any{
			"status": "feasible", "detail": "d", "plan_digest": "0123456789abcdef", "instance_serving": true,
			"availability_budget": map[string]any{"status": "exceeds", "gate": "acknowledge", "enforcement": "acknowledge", "detail": "spent"},
		})
		json.NewEncoder(w).Encode(map[string]any{"output": string(plan)}) //nolint:errcheck
	}))
	defer agent.Close()

	var (
		status int
		msg    string
		reason string
	)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ProceedEscalationRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			status, msg = -1, err.Error()
		} else {
			a, s, m := recoveryApprovalFromGate(req, "", "faulttest", true)
			status, msg = s, m
			if a != nil {
				reason = a.Justification
				if !a.Approved() {
					status, msg = -2, "not approved"
				}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "complete"}) //nolint:errcheck
	}))
	defer gateway.Close()

	r := faultlib.NewRemediator(&faultlib.HarnessConfig{GatewayURL: gateway.URL, SysadminAgentURL: agent.URL})
	fault := faultlib.Failure{ID: "db-pitr-dropped-table", AgentConnOverride: "pgbackrest-db",
		PITR: &faultlib.PITRSpec{TargetType: "time", AllowStopServing: true}}
	if err := r.SetPITR(fault, "PITR_TARGET time=2026-10-28T17:11:10Z lsn=0/3000028\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ProceedEscalation(context.Background(), "plr_g", faultlib.ProceedEscalationRequest{Resolution: "approved"}); err != nil {
		t.Fatal(err)
	}
	if status != 0 {
		t.Fatalf("the gateway refused the harness's approval (%d): %s", status, msg)
	}
	if !strings.Contains(reason, "faulttest harness") || !strings.Contains(reason, "db-pitr-dropped-table") {
		t.Errorf("recorded justification = %q", reason)
	}
}
