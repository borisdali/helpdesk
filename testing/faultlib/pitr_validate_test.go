package faultlib

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const validPITRFault = `
version: "1"
failures:
  - id: my-pitr
    name: n
    category: database
    severity: high
    pitr: {target_type: time}
    diagnosis_playbook_series_id: pbs_pgbackrest_pitr_triage
    inject:
      type: shell_exec
      script_inline: |
        echo "PITR_TARGET time=$T"
    prompt: p
`

func TestLoadCatalog_ValidatesPITRFaults(t *testing.T) {
	if _, err := LoadCatalogFromBytes([]byte(validPITRFault), "custom"); err != nil {
		t.Fatalf("a well-formed pitr fault was rejected: %v", err)
	}
	for _, tc := range []struct {
		name, from, to, want string
	}{
		{"a target type the gateway does not take", "pitr: {target_type: time}", "pitr: {target_type: xid}", "not time or lsn"},
		{"an inject that keeps no output", "type: shell_exec", "type: docker_exec", "must be shell_exec"},
		{"an inject that never prints the target", `echo "PITR_TARGET time=$T"`, `echo hello`, "never prints a PITR_TARGET line"},
		{"no diagnosis playbook, so no triage request to carry the target", "    diagnosis_playbook_series_id: pbs_pgbackrest_pitr_triage\n", "", "needs diagnosis_playbook_series_id"},
	} {
		y := strings.Replace(validPITRFault, tc.from, tc.to, 1)
		if y == validPITRFault {
			t.Fatalf("%s: the substitution changed nothing", tc.name)
		}
		_, err := LoadCatalogFromBytes([]byte(y), "custom")
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "my-pitr") {
			t.Errorf("%s: err = %v, want naming my-pitr and containing %q", tc.name, err, tc.want)
		}
	}
	// An external_inject that is not a script is as bad as the inject.
	y := strings.Replace(validPITRFault, "    prompt: p\n", "    external_inject: {type: sql}\n    prompt: p\n", 1)
	if _, err := LoadCatalogFromBytes([]byte(y), "custom"); err == nil || !strings.Contains(err.Error(), "external_inject") {
		t.Errorf("external_inject: err = %v", err)
	}
	// A fault with no pitr block is not held to any of this.
	plain := strings.Replace(strings.Replace(validPITRFault, "    pitr: {target_type: time}\n", "", 1), "type: shell_exec", "type: docker_exec", 1)
	if _, err := LoadCatalogFromBytes([]byte(plain), "custom"); err != nil {
		t.Errorf("a non-PITR fault was rejected: %v", err)
	}
}

// Every PITR fault the project ships is held to the same rules by the loader
// itself (LoadBuiltinCatalog fails otherwise); this names them, so a new one
// cannot ship without the harness knowing what it is.
func TestBuiltinCatalog_PITRFaults(t *testing.T) {
	cat, err := LoadBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, f := range cat.Failures {
		if f.PITR != nil {
			ids = append(ids, f.ID)
		}
	}
	if len(ids) < 2 {
		t.Errorf("pitr faults in the catalog: %v, want db-pitr-dropped-table and its Kubernetes variant", ids)
	}
}

// ── the small branches ───────────────────────────────────────────────────────

func TestSetPITR_ServerFallsBackToTheAgentConn(t *testing.T) {
	fk := newPITRFakes(t)
	r := NewRemediator(&HarnessConfig{GatewayURL: fk.gateway.URL, SysadminAgentURL: fk.agent.URL, AgentConnStr: "my-alias"})
	f := Failure{ID: "x", PITR: &PITRSpec{}} // no agent_conn_override
	if err := r.SetPITR(f, "PITR_TARGET time=2026-10-28T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	r.ProceedEscalation(t.Context(), "plr_g", ProceedEscalationRequest{Resolution: "approved"}) //nolint:errcheck
	args, _ := fk.planBody["args"].(map[string]any)
	if args["target"] != "my-alias" {
		t.Errorf("plan target = %v, want the harness-wide agent connection", args["target"])
	}
}

func TestRemediator_ApprovesAnLSNTarget(t *testing.T) {
	fk := newPITRFakes(t)
	r := fk.remediator()
	f := droppedTable
	f.PITR = &PITRSpec{TargetType: "lsn"}
	if err := r.SetPITR(f, droppedTableOut); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ProceedEscalation(t.Context(), "plr_g", ProceedEscalationRequest{Resolution: "approved"}); err != nil {
		t.Fatal(err)
	}
	target, _ := fk.gateBody["recovery_target"].(map[string]any)
	args, _ := fk.planBody["args"].(map[string]any)
	if target["type"] != "lsn" || target["value"] != "0/3000028" || args["recovery_target_type"] != "lsn" {
		t.Errorf("approval target %v, plan args %v", target, args)
	}
}

func TestFetchPITRPlan_ErrorBranches(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"a body that is not JSON", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>bad gateway</html>")) }, "unparseable body"},
		{"a plan that is not JSON", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"output": "not a plan"}) //nolint:errcheck
		}, "unreadable plan"},
		{"a server error with no message", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("{}")) //nolint:errcheck
		}, "HTTP 500"},
	} {
		srv := httptest.NewServer(tc.handler)
		gate := newPITRFakes(t)
		r := NewRemediator(&HarnessConfig{GatewayURL: gate.gateway.URL, SysadminAgentURL: srv.URL})
		r.SetPITR(droppedTable, droppedTableOut) //nolint:errcheck
		_, err := r.ProceedEscalation(t.Context(), "plr_g", ProceedEscalationRequest{Resolution: "approved"})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
		if gate.gateCalls != 0 {
			t.Errorf("%s: the gate was approved without a readable plan", tc.name)
		}
	}
}

// ── both harness entry points arm the PITR approval ──────────────────────────

// A PITR fault is approved by the Remediator that SetPITR armed; a harness
// that builds a Remediator and forgets to call it would run the triage, then
// approve the gate with no target (the gateway would refuse it). The go-test
// harness did exactly that until 2026-10-28; neither can be driven here
// without a stack, so this holds both files to the call.
func TestHarnessEntryPoints_CallSetPITR(t *testing.T) {
	for _, file := range []string{"../cmd/faulttest/main.go", "../faulttest/faulttest_test.go"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if !strings.Contains(src, "SetPITR(f, cycle.InjectOutput)") {
			t.Errorf("%s never calls SetPITR(f, cycle.InjectOutput): a PITR fault would be approved with no target", file)
		}
		if strings.Contains(src, "RunGateLoop") || strings.Contains(src, "ProceedEscalation(") {
			if strings.Index(src, "SetPITR(f, cycle.InjectOutput)") > strings.Index(src, "RunGateLoop") && strings.Contains(src, "RunGateLoop(") {
				t.Errorf("%s arms SetPITR after it first drives a gate", file)
			}
		}
	}
}
