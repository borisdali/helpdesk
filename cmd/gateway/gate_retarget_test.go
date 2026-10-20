package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
)

// retargetInfra is the two targets of VM test 2.1 (2026-10-17): the Docker
// pgBackRest fixture and the real VM.
func retargetInfra() *infra.Config {
	return &infra.Config{DBServers: map[string]infra.DBServer{
		"pgbackrest-db": {
			Name:             "pgBackRest Fault Test Database",
			ConnectionString: "host=localhost port=15435 dbname=testdb user=postgres password=testpass",
			ContainerName:    "helpdesk-test-pg-pgbackrest",
		},
		"pgbackrest-ubuntu-vm": {
			Name:             "pgBackRest Real Ubuntu VM",
			ConnectionString: "host=pgbackrest-vm.test port=5432 dbname=postgres user=postgres password=testpass",
			SystemdUnit:      "postgresql@16-main",
		},
	}}
}

const (
	vmConn     = "host=pgbackrest-vm.test port=5432 dbname=postgres user=postgres password=testpass"
	dockerConn = "host=localhost port=15435 dbname=testdb user=postgres password=testpass"
)

func TestGateRetargetRefusal(t *testing.T) {
	cfg := retargetInfra()
	cases := []struct {
		name          string
		req           ProceedEscalationRequest
		leaf, primary audit.PlaybookRun
		wantRefused   string // substring of the refusal, "" for allowed
	}{
		{"VM test 2.1: diagnosed the VM, approval names Docker",
			ProceedEscalationRequest{ConnectionString: dockerConn}, audit.PlaybookRun{ConnectionString: vmConn}, audit.PlaybookRun{}, "is not the target this run diagnosed"},
		{"same string", ProceedEscalationRequest{ConnectionString: vmConn}, audit.PlaybookRun{ConnectionString: vmConn}, audit.PlaybookRun{}, ""},
		{"config key for the same target", ProceedEscalationRequest{ConnectionString: "pgbackrest-ubuntu-vm"},
			audit.PlaybookRun{ConnectionString: vmConn}, audit.PlaybookRun{}, ""},
		{"display name for the same target", ProceedEscalationRequest{ConnectionString: "pgBackRest Fault Test Database"},
			audit.PlaybookRun{ConnectionString: dockerConn}, audit.PlaybookRun{}, ""},
		{"container name for the same target", ProceedEscalationRequest{ConnectionString: "helpdesk-test-pg-pgbackrest"},
			audit.PlaybookRun{ConnectionString: dockerConn}, audit.PlaybookRun{}, ""},
		{"same database, fields reordered, other user and no password",
			ProceedEscalationRequest{ConnectionString: "dbname=postgres user=admin host=pgbackrest-vm.test port=5432"},
			audit.PlaybookRun{ConnectionString: vmConn}, audit.PlaybookRun{}, ""},
		{"same host, other database", ProceedEscalationRequest{ConnectionString: "host=pgbackrest-vm.test port=5432 dbname=other"},
			audit.PlaybookRun{ConnectionString: vmConn}, audit.PlaybookRun{}, "is not the target"},
		{"config key for the other target", ProceedEscalationRequest{ConnectionString: "pgbackrest-db"},
			audit.PlaybookRun{ConnectionString: vmConn}, audit.PlaybookRun{}, "is not the target"},
		{"unknown names that differ", ProceedEscalationRequest{ConnectionString: "prod-db"},
			audit.PlaybookRun{ConnectionString: "staging-db"}, audit.PlaybookRun{}, "is not the target"},
		{"no connection_string in the approval: falls back as before", ProceedEscalationRequest{},
			audit.PlaybookRun{ConnectionString: vmConn}, audit.PlaybookRun{}, ""},
		{"run has no target recorded: nothing to compare", ProceedEscalationRequest{ConnectionString: dockerConn},
			audit.PlaybookRun{}, audit.PlaybookRun{}, ""},
		{"leaf has none, primary run's is the target", ProceedEscalationRequest{ConnectionString: dockerConn},
			audit.PlaybookRun{}, audit.PlaybookRun{ConnectionString: vmConn}, "is not the target"},
		{"other namespace", ProceedEscalationRequest{Namespace: "prod"}, audit.PlaybookRun{Namespace: "helpdesk-test"}, audit.PlaybookRun{}, "namespace \"prod\" is not"},
		{"same namespace", ProceedEscalationRequest{Namespace: "helpdesk-test"}, audit.PlaybookRun{Namespace: "helpdesk-test"}, audit.PlaybookRun{}, ""},
	}
	for _, c := range cases {
		got := gateRetargetRefusal(cfg, c.req, &c.leaf, &c.primary)
		if (c.wantRefused == "") != (got == "") || !strings.Contains(got, c.wantRefused) {
			t.Errorf("%s: refusal = %q, want %q", c.name, got, c.wantRefused)
		}
	}
}

// The VM test 2.1 bug through the real handler: the gate was raised on a
// diagnosis of the VM, the approval names the Docker fixture. It must be
// refused with 409 before anything is written: the triage run stays
// gate_pending (no PATCH) and no remediation run is started (no POST).
func TestHandleProceedEscalation_RefusesRetarget(t *testing.T) {
	run := &audit.PlaybookRun{
		RunID:            "plr_gate_vm01",
		Outcome:          audit.OutcomeGatePending,
		TransitionedTo:   "pbs_pgbackrest_restore_remediate",
		FindingsSummary:  "pg_control missing; backup healthy",
		ConnectionString: vmConn,
	}
	remedPB := &audit.Playbook{PlaybookID: "pb_restore01", SeriesID: "pbs_pgbackrest_restore_remediate",
		Name: "pgBackRest restore", ExecutionMode: "agent", IsActive: true}
	runData, _ := json.Marshal(run)
	var mu sync.Mutex
	var writes []string
	var runStartBody string
	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/playbook-runs/"):
			if strings.HasSuffix(r.URL.Path, "/next") || r.URL.Query().Get("prior_run_id") != "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write(runData) //nolint:errcheck
		case r.Method == http.MethodGet && r.URL.Query().Get("series_id") != "":
			json.NewEncoder(w).Encode(map[string]any{"playbooks": []*audit.Playbook{remedPB}}) //nolint:errcheck
		case r.Method == http.MethodPatch || r.Method == http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			writes = append(writes, r.Method+" "+r.URL.Path)
			if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/runs") {
				runStartBody = string(b)
			}
			mu.Unlock()
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(map[string]string{"run_id": "plr_rem_vm01"}) //nolint:errcheck
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(auditSrv.Close)

	gw := makePlaybookRunGateway(auditSrv.URL, nil)
	gw.infra = retargetInfra()

	body, _ := json.Marshal(map[string]string{"resolution": "approved", "resolved_by": "alice", "approval_mode": "force",
		"connection_string": dockerConn, "purpose": "remediation"})
	rec := postProceedEscalation(t, gw, "plr_gate_vm01", string(body))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "is not the target this run diagnosed") {
		t.Fatalf("got %d %s, want 409 naming the retarget", rec.Code, rec.Body.String())
	}
	mu.Lock()
	if len(writes) != 0 {
		t.Errorf("refused approval still wrote to auditd: %v", writes)
	}
	mu.Unlock()

	// The same target by its config key is allowed and proceeds (502: no A2A
	// client wired), the remediation run started against it.
	body, _ = json.Marshal(map[string]string{"resolution": "approved", "resolved_by": "alice", "approval_mode": "force",
		"connection_string": "pgbackrest-ubuntu-vm", "purpose": "remediation"})
	rec = postProceedEscalation(t, gw, "plr_gate_vm01", string(body))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("same target by config key: got %d %s, want 502 (proceeded, no A2A client)", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(runStartBody, `"connection_string":"pgbackrest-ubuntu-vm"`) {
		t.Errorf("remediation run start = %s, want it against the approved (same) target", runStartBody)
	}
}

// The refusal is returned to whoever sent the approval: it must name both
// targets without either password (found in the live check, 2026-10-19: the
// 409 echoed the run's stored password=testpass).
func TestGateRetargetRefusal_NoPasswords(t *testing.T) {
	got := gateRetargetRefusal(retargetInfra(), ProceedEscalationRequest{ConnectionString: dockerConn},
		&audit.PlaybookRun{ConnectionString: vmConn}, &audit.PlaybookRun{})
	if got == "" || strings.Contains(got, "testpass") || !strings.Contains(got, "host=localhost") || !strings.Contains(got, "host=pgbackrest-vm.test") {
		t.Errorf("refusal = %q, want both targets named and no password", got)
	}
}

func TestRedactConnString(t *testing.T) {
	for in, want := range map[string]string{
		"host=db port=5432 password=s3cret user=x": "host=db port=5432 password=*** user=x",
		"host=db port=5432":                        "host=db port=5432",
		"postgres://u:s3cret@db:5432/app":          "postgres://u:xxxxx@db:5432/app",
		"pgbackrest-ubuntu-vm":                     "pgbackrest-ubuntu-vm",
	} {
		if got := redactConnString(in); got != want {
			t.Errorf("redactConnString(%q) = %q, want %q", in, got, want)
		}
	}
}
