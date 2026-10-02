package main

import (
	"context"
	"path/filepath"
	"testing"

	"helpdesk/internal/audit"
)

// TestRecordGateAcknowledged_TransitionTarget_SetsDecisionAgent is a
// regression test for a real bug found live during v0.30 development: an
// informed gate for a same-domain TRANSITION_TO hop (e.g. triage →
// remediation) left Decision.Agent empty, because recordGateAcknowledged
// only ever read run.EscalatedTo — never run.TransitionedTo — even though
// the gate mechanism fires for both. An empty Agent isn't cosmetic: the
// auditor's "unknown agent" check (cmd/auditor/main.go) has no way to
// recognize an empty string as a legitimate pbs_*-series target the way it
// already skips a populated one, so every TRANSITION_TO-driven informed
// gate spuriously fired an "unknown agent" security warning.
func TestRecordGateAcknowledged_TransitionTarget_SetsDecisionAgent(t *testing.T) {
	store, err := audit.NewStore(audit.StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	g := &Gateway{auditor: audit.NewGatewayAuditor(store)}

	run := &audit.PlaybookRun{
		RunID:          "plr_test01",
		TransitionedTo: "pbs_db_backup_archiving_remediate",
		// EscalatedTo deliberately empty — this run took the TRANSITION_TO
		// ending, not ESCALATE_TO.
	}
	g.recordGateAcknowledged(context.Background(), run, "faulttest", "approved", "force", "", "")

	events, err := store.Query(context.Background(), audit.QueryOptions{
		TraceIDPrefix: run.RunID,
		EventTypes:    []audit.EventType{audit.EventTypeGateAcknowledged},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 gate_acknowledged event, got %d", len(events))
	}
	if events[0].Decision == nil {
		t.Fatal("Decision is nil")
	}
	if events[0].Decision.Agent != "pbs_db_backup_archiving_remediate" {
		t.Errorf("Decision.Agent = %q, want pbs_db_backup_archiving_remediate (fell back to TransitionedTo)", events[0].Decision.Agent)
	}
}

// TestRecordGateAcknowledged_EscalationTarget_StillWorks confirms the fix
// didn't change behavior for the original, older ESCALATE_TO case.
func TestRecordGateAcknowledged_EscalationTarget_StillWorks(t *testing.T) {
	store, err := audit.NewStore(audit.StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	g := &Gateway{auditor: audit.NewGatewayAuditor(store)}

	run := &audit.PlaybookRun{
		RunID:       "plr_test02",
		EscalatedTo: "pbs_sysadmin_replica_connectivity_triage",
	}
	g.recordGateAcknowledged(context.Background(), run, "faulttest", "approved", "force", "", "")

	events, err := store.Query(context.Background(), audit.QueryOptions{
		TraceIDPrefix: run.RunID,
		EventTypes:    []audit.EventType{audit.EventTypeGateAcknowledged},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 gate_acknowledged event, got %d", len(events))
	}
	if events[0].Decision == nil || events[0].Decision.Agent != "pbs_sysadmin_replica_connectivity_triage" {
		t.Errorf("Decision.Agent = %v, want pbs_sysadmin_replica_connectivity_triage", events[0].Decision)
	}
}
