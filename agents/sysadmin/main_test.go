package main

import (
	"os"
	"testing"

	"helpdesk/internal/audit"
	"helpdesk/internal/evidence"
)

// TestMain loads the real, shipped objective_evidence.yaml before any test
// runs — deliberately not a hand-written test fixture, mirroring
// agents/database/main_test.go's/agents/k8s/tools_test.go's TestMain exactly.
// Closes a real pre-existing gap: unlike its two siblings, agents/sysadmin
// had no test loading its own real YAML file at all, so a syntax error or
// invalid confirmation_probe name (e.g. a typo) would only ever surface at
// agent startup, not at test time. Individual tests
// (withPgBackRestEvidenceRules in pgbackrest_test.go) still override
// pgbackrestEvidenceRules locally, with t.Cleanup restoring whatever
// TestMain set here — so this baseline and per-test overrides compose
// correctly, they don't conflict.
func TestMain(m *testing.M) {
	rulesByTool, err := evidence.LoadRules("objective_evidence.yaml")
	if err != nil {
		panic("agents/sysadmin/objective_evidence.yaml failed to load — fix the file, don't skip this: " + err.Error())
	}
	pgbackrestEvidenceRules = rulesByTool["get_pgbackrest_status"]
	restoreVerifyEvidenceRules = rulesByTool["verify_restore_recovery"]
	os.Exit(m.Run())
}

// TestObjectiveEvidenceYAML_Valid is a narrower, more directly diagnosable
// duplicate of what TestMain already enforces by panicking on load failure
// — see agents/database/main_test.go's identical test for the rationale.
func TestObjectiveEvidenceYAML_Valid(t *testing.T) {
	pgbackrestRules, restoreVerifyRules := loadSysadminEvidenceRules("objective_evidence.yaml")
	if len(pgbackrestRules) == 0 {
		t.Error("expected at least one get_pgbackrest_status rule from the real shipped file")
	}
	if len(restoreVerifyRules) == 0 {
		t.Error("expected a verify_restore_recovery rule from the real shipped file")
	}
}

// TestObjectiveEvidenceYAML_PgBackRestConfirmation runs the shipped rules'
// confirmation settings against real EVIDENCE quotes. History:
//   - 2026-10-08: resource_named_in_quote confirmed or not depending on
//     whether the stanza name "main" happened to appear anywhere in the
//     response; switched to evidence_quote_contains_value.
//   - 2026-10-16: that probe looks for the fired value "true", which no
//     verbatim quote of a broken chain or a stanza error contains. The two
//     "live" quotes below are exactly what make faulttest-gateway-pgbackrest
//     got back — both correct, both left unconfirmed. Switched to
//     evidence_quote_contains_any with terms naming the raw data.
//
// The negative cases are the reason the terms aren't field names: a healthy
// result's quote (the live db-pgdata-corrupted one) has the same field names,
// and a model quoting only that while the signal fired has misread it.
func TestObjectiveEvidenceYAML_PgBackRestConfirmation(t *testing.T) {
	const healthyQuote = `status_code: 0; backup_stale: false; last_backup_label: "20261016-213142F"; wal_continuity.status: "continuous"; consistency_reachable: true`
	cases := []struct {
		signal, name, quote string
		want                bool
	}{
		{"pgbackrest_wal_chain_broken", "live wal-gap quote",
			`wal_continuity.status='broken'; consistency_reachable=false; detail='the WAL backup 20261016-213142F needs (0000000F0000000000000005 to 0000000F0000000000000005) is not in the repo: PostgreSQL could not start from this backup'`, true},
		{"pgbackrest_wal_chain_broken", "gap after the backup, detail only",
			`WAL segment 000000010000000000000007 on timeline 1 is missing: recovery would stop at 000000010000000000000006`, true},
		{"pgbackrest_wal_chain_broken", "backup off the newest timeline",
			`the backup is on a branch the newest timeline 3 does not descend from`, true},
		{"pgbackrest_wal_chain_broken", "healthy fields, misread", healthyQuote, false},
		{"pgbackrest_backup_unhealthy", "live repo-unreadable quote",
			`repo_status_message":"[PathOpenError] unable to list file info for path '/var/lib/pgbackrest/backup': [13] Permission denied`, true},
		{"pgbackrest_backup_unhealthy", "stale backup", `backup_stale: true; last_backup_time: 2026-10-13T02:00:00Z`, true},
		{"pgbackrest_backup_unhealthy", "never backed up", `status_message: "no valid backups"`, true},
		{"pgbackrest_backup_unhealthy", "healthy fields, misread", healthyQuote, false},
		{"pgbackrest_backup_unhealthy", "status ok and an empty error field", `status_message: ok; last_backup_error: ""`, false},
	}
	rules := map[string]evidence.Rule{}
	pgbackrestRules, _ := loadSysadminEvidenceRules("objective_evidence.yaml")
	for _, r := range pgbackrestRules {
		rules[r.Signal] = r
	}
	for _, c := range cases {
		r, ok := rules[c.signal]
		if !ok {
			t.Fatalf("no %s rule in the real shipped file", c.signal)
		}
		ev := audit.ObjectiveEvidence{
			Tool: r.Tool, Signal: r.Signal, Value: true,
			ConfirmationProbe: r.ConfirmationProbe, ConfirmationOperator: r.ConfirmationOperator,
			ConfirmationThreshold: r.ConfirmationThreshold, ConfirmationTerms: r.ConfirmationTerms,
		}
		outcome := evidence.HopOutcome{Report: &audit.DiagnosticReport{
			Hypotheses: []audit.DiagnosticHypothesis{{IsPrimary: true, Confidence: 0.95, Evidence: c.quote}},
		}}
		got, err := evidence.Confirmed(outcome, ev)
		if err != nil {
			t.Fatalf("%s / %s: Confirmed: %v", c.signal, c.name, err)
		}
		if got != c.want {
			t.Errorf("%s / %s: confirmed = %v, want %v (quote %q)", c.signal, c.name, got, c.want, c.quote)
		}
	}
}

// TestObjectiveEvidenceYAML_WALChainBroken_Shipped verifies the shipped rules
// file activates the v0.32 WAL-continuity signal.
func TestObjectiveEvidenceYAML_WALChainBroken_Shipped(t *testing.T) {
	pgbackrestRules, _ := loadSysadminEvidenceRules("objective_evidence.yaml")
	for _, r := range pgbackrestRules {
		if r.Signal == "pgbackrest_wal_chain_broken" {
			if r.Probe != "wal_chain_broken" || r.Tool != "get_pgbackrest_status" {
				t.Errorf("rule = %+v, want probe wal_chain_broken on get_pgbackrest_status", r)
			}
			return
		}
	}
	t.Fatal("expected a pgbackrest_wal_chain_broken rule in the real shipped file")
}

// TestObjectiveEvidenceYAML_PITRSignals_Shipped verifies the shipped rules
// file wires both verify_restore_recovery signals to the probes the Go schema
// registers, and that the Helm copy of the file is the same file.
func TestObjectiveEvidenceYAML_PITRSignals_Shipped(t *testing.T) {
	_, rules := loadSysadminEvidenceRules("objective_evidence.yaml")
	want := map[string]string{
		"restore_recovery_mismatch":  "recovery_mismatch",
		"restore_target_not_reached": "recovery_target_not_reached",
	}
	for _, r := range rules {
		if probe, ok := want[r.Signal]; ok {
			if r.Probe != probe || r.Tool != "verify_restore_recovery" {
				t.Errorf("%s: rule = %+v, want probe %s", r.Signal, r, probe)
			}
			delete(want, r.Signal)
		}
	}
	if len(want) > 0 {
		t.Errorf("missing shipped rules for %v", want)
	}

	repo, err := os.ReadFile("objective_evidence.yaml")
	if err != nil {
		t.Fatal(err)
	}
	helm, err := os.ReadFile("../../deploy/helm/helpdesk/files/objective-evidence-sysadmin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(repo) != string(helm) {
		t.Error("deploy/helm/helpdesk/files/objective-evidence-sysadmin.yaml differs from agents/sysadmin/objective_evidence.yaml: copy it over")
	}
}
