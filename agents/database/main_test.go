package main

import (
	"os"
	"path/filepath"
	"testing"

	"helpdesk/internal/evidence"
)

// TestMain loads the real, shipped objective_evidence.yaml before any test
// runs — deliberately not a hand-written test fixture, mirroring
// agents/k8s/tools_test.go's TestMain exactly. Individual tests
// (withActiveConnectionEvidenceRules/withReplicationEvidenceRules in
// tools_test.go) still override these package vars locally, with
// t.Cleanup restoring whatever TestMain set here — so this baseline and
// per-test overrides compose correctly, they don't conflict.
func TestMain(m *testing.M) {
	rulesByTool, err := evidence.LoadRules("objective_evidence.yaml")
	if err != nil {
		panic("agents/database/objective_evidence.yaml failed to load — fix the file, don't skip this: " + err.Error())
	}
	activeConnectionEvidenceRules = rulesByTool["get_active_connections"]
	replicationEvidenceRules = rulesByTool["get_replication_status"]
	backupArchiverEvidenceRules = rulesByTool["get_backup_status"]
	os.Exit(m.Run())
}

func writeDBRulesFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadDBEvidenceRules_ValidFile guards specifically against a wrong map
// key silently leaving one tool's rules empty forever — see
// agents/k8s/main_test.go's identical test for the full rationale.
func TestLoadDBEvidenceRules_ValidFile(t *testing.T) {
	path := writeDBRulesFile(t, `- tool: get_active_connections
  probe: idle_in_transaction_seconds
  operator: ">"
  threshold: 300
  signal: idle_in_transaction_stuck
- tool: get_replication_status
  probe: disconnected
  operator: "=="
  threshold: true
  signal: replica_disconnected
- tool: get_backup_status
  probe: archiving_stale
  operator: "=="
  threshold: true
  signal: backup_archiving_stale
`)
	activeConnRules, replicationRules, backupRules := loadDBEvidenceRules(path)
	if len(activeConnRules) != 1 {
		t.Errorf("got %d get_active_connections rules, want 1", len(activeConnRules))
	}
	if len(replicationRules) != 1 {
		t.Errorf("got %d get_replication_status rules, want 1", len(replicationRules))
	}
	if len(backupRules) != 1 {
		t.Errorf("got %d get_backup_status rules, want 1", len(backupRules))
	}
	if len(activeConnRules) == 1 && activeConnRules[0].Signal != "idle_in_transaction_stuck" {
		t.Errorf("got signal %q, want idle_in_transaction_stuck", activeConnRules[0].Signal)
	}
	if len(replicationRules) == 1 && replicationRules[0].Signal != "replica_disconnected" {
		t.Errorf("got signal %q, want replica_disconnected", replicationRules[0].Signal)
	}
	if len(backupRules) == 1 && backupRules[0].Signal != "backup_archiving_stale" {
		t.Errorf("got signal %q, want backup_archiving_stale", backupRules[0].Signal)
	}
}

func TestLoadDBEvidenceRules_OnlyOneToolPresent(t *testing.T) {
	path := writeDBRulesFile(t, `- tool: get_active_connections
  probe: idle_in_transaction_seconds
  operator: ">"
  threshold: 300
  signal: idle_in_transaction_stuck
`)
	activeConnRules, replicationRules, backupRules := loadDBEvidenceRules(path)
	if len(activeConnRules) != 1 {
		t.Errorf("got %d get_active_connections rules, want 1", len(activeConnRules))
	}
	if len(replicationRules) != 0 {
		t.Errorf("got %d get_replication_status rules, want 0 (file never mentions it)", len(replicationRules))
	}
	if len(backupRules) != 0 {
		t.Errorf("got %d get_backup_status rules, want 0 (file never mentions it)", len(backupRules))
	}
}

func TestLoadDBEvidenceRules_MalformedFile_ReturnsNilNil(t *testing.T) {
	path := writeDBRulesFile(t, `- tool: get_active_connections
  probe: idle_in_transction_seconds
  operator: ">"
  threshold: 300
  signal: idle_in_transaction_stuck
`) // "idle_in_transction_seconds" — typo, unknown probe
	activeConnRules, replicationRules, backupRules := loadDBEvidenceRules(path)
	if activeConnRules != nil || replicationRules != nil || backupRules != nil {
		t.Errorf("got (%v, %v, %v), want (nil, nil, nil) for a malformed rules file", activeConnRules, replicationRules, backupRules)
	}
}

func TestLoadDBEvidenceRules_MissingFile_ReturnsNilNil(t *testing.T) {
	activeConnRules, replicationRules, backupRules := loadDBEvidenceRules(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if activeConnRules != nil || replicationRules != nil || backupRules != nil {
		t.Errorf("got (%v, %v, %v), want (nil, nil, nil) for a missing file", activeConnRules, replicationRules, backupRules)
	}
}

// TestObjectiveEvidenceYAML_Valid is a narrower, more directly diagnosable
// duplicate of what TestMain already enforces by panicking on load failure
// — see agents/k8s/main_test.go's identical test for the rationale.
func TestObjectiveEvidenceYAML_Valid(t *testing.T) {
	activeConnRules, replicationRules, backupRules := loadDBEvidenceRules("objective_evidence.yaml")
	if len(activeConnRules) == 0 {
		t.Error("expected at least one get_active_connections rule from the real shipped file")
	}
	if len(replicationRules) == 0 {
		t.Error("expected at least one get_replication_status rule from the real shipped file")
	}
	if len(backupRules) == 0 {
		t.Error("expected at least one get_backup_status rule from the real shipped file")
	}
}
