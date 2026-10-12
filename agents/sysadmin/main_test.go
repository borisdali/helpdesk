package main

import (
	"os"
	"testing"

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
	os.Exit(m.Run())
}

// TestObjectiveEvidenceYAML_Valid is a narrower, more directly diagnosable
// duplicate of what TestMain already enforces by panicking on load failure
// — see agents/database/main_test.go's identical test for the rationale.
func TestObjectiveEvidenceYAML_Valid(t *testing.T) {
	pgbackrestRules := loadSysadminEvidenceRules("objective_evidence.yaml")
	if len(pgbackrestRules) == 0 {
		t.Error("expected at least one get_pgbackrest_status rule from the real shipped file")
	}
}

// TestObjectiveEvidenceYAML_PgBackRestBackupUnhealthy_UsesEvidenceQuoteProbe
// is a regression guard for the 2026-10-08 confirmation_probe change: a live
// db-pgdata-corrupted run showed the signal gating inconsistently for the
// identical backup_unhealthy=true condition, purely based on whether the
// model's full response happened to contain the pgBackRest stanza name
// ("main") anywhere — resource_named_in_quote's own check — which has
// nothing to do with whether the model actually engaged with the staleness
// risk. Confirmed live via direct SQL against the real audit trail (see
// project memory). Pins the probe to evidence_quote_contains_value so this
// can't silently regress back.
func TestObjectiveEvidenceYAML_PgBackRestBackupUnhealthy_UsesEvidenceQuoteProbe(t *testing.T) {
	pgbackrestRules := loadSysadminEvidenceRules("objective_evidence.yaml")
	var found bool
	for _, r := range pgbackrestRules {
		if r.Signal != "pgbackrest_backup_unhealthy" {
			continue
		}
		found = true
		if r.ConfirmationProbe != "evidence_quote_contains_value" {
			t.Errorf("pgbackrest_backup_unhealthy confirmation_probe = %q, want evidence_quote_contains_value", r.ConfirmationProbe)
		}
	}
	if !found {
		t.Fatal("expected a pgbackrest_backup_unhealthy rule in the real shipped file")
	}
}
