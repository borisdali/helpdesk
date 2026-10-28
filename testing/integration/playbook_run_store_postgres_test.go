//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestPlaybookRunStore_Migrate_Postgres verifies that the playbook_run_store
// migration is idempotent on a Postgres backend, and that a record written
// on the first open is durable and readable after a fresh second open. This
// exercises the containsAny("duplicate column", "already exists") guard
// fixed in v0.21.0 — previously only the SQLite error string was handled.
func TestPlaybookRunStore_Migrate_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	var runID string

	openPostgresStoreTwice(t, dsn, func(t *testing.T, store *audit.Store, pass int) {
		s, err := audit.NewPlaybookRunStore(store.DB(), store.IsPostgres())
		if err != nil {
			t.Fatalf("NewPlaybookRunStore (pass %d): %v", pass, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if pass == 1 {
			run := &audit.PlaybookRun{
				PlaybookID:    "pbs_e2e_postgres_migrate_test",
				SeriesID:      "pbs_e2e_postgres_migrate_test",
				ExecutionMode: "fleet",
				Operator:      "migrate-test",
				StartedAt:     time.Now().UTC(),
			}
			if err := s.Record(ctx, run); err != nil {
				t.Fatalf("Record: %v", err)
			}
			runID = run.RunID
			return
		}

		got, err := s.GetByRunID(ctx, runID)
		if err != nil {
			t.Fatalf("GetByRunID: %v", err)
		}
		if got.PlaybookID != "pbs_e2e_postgres_migrate_test" {
			t.Errorf("PlaybookID = %q, want pbs_e2e_postgres_migrate_test", got.PlaybookID)
		}
	})
}

// The point-in-time recovery target a run carries (playbook_runs.recovery_target)
// is stored, listed and kept through a reopen on Postgres, and a database that
// predates the column gains it when the store opens.
func TestPlaybookRunStore_RecoveryTarget_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	var runID string

	openPostgresStoreTwice(t, dsn, func(t *testing.T, store *audit.Store, pass int) {
		s, err := audit.NewPlaybookRunStore(store.DB(), store.IsPostgres())
		if err != nil {
			t.Fatalf("NewPlaybookRunStore (pass %d): %v", pass, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if pass == 1 {
			run := &audit.PlaybookRun{
				PlaybookID:     "pb_e2e_pitr",
				SeriesID:       "pbs_pgbackrest_pitr_triage",
				ExecutionMode:  "agent",
				RecoveryTarget: audit.FormatRecoveryTarget("lsn", "0/3000028"),
				StartedAt:      time.Now().UTC(),
			}
			if err := s.Record(ctx, run); err != nil {
				t.Fatalf("Record: %v", err)
			}
			runID = run.RunID
			return
		}

		got, err := s.GetByRunID(ctx, runID)
		if err != nil || got.RecoveryTarget != "lsn=0/3000028" {
			t.Fatalf("GetByRunID = %+v, %v; want recovery_target lsn=0/3000028", got, err)
		}
		if typ, v := audit.ParseRecoveryTarget(got.RecoveryTarget); typ != "lsn" || v != "0/3000028" {
			t.Errorf("parsed %q %q", typ, v)
		}
		runs, err := s.ListBySeriesID(ctx, "pbs_pgbackrest_pitr_triage", 5)
		if err != nil || len(runs) != 1 || runs[0].RecoveryTarget == "" {
			t.Errorf("ListBySeriesID = %v, %v", runs, err)
		}
		// An update (the run concluding) must not clear it.
		if err := s.Update(ctx, runID, "gate_pending", "", "pbs_pgbackrest_pitr_remediate", "plan feasible", "", "", nil, false, "recovery_target_required"); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got, _ = s.GetByRunID(ctx, runID); got.RecoveryTarget == "" || got.GateReason != "recovery_target_required" {
			t.Errorf("after Update: %+v", got)
		}
	})
}

func TestPlaybookRunStore_RecoveryTargetColumnMigration_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	store, err := audit.NewStore(audit.StoreConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The table as it was before the column: no recovery_target.
	if _, err := store.DB().ExecContext(ctx, `CREATE TABLE playbook_runs (
    run_id TEXT NOT NULL PRIMARY KEY, playbook_id TEXT NOT NULL, series_id TEXT NOT NULL,
    execution_mode TEXT NOT NULL DEFAULT 'fleet', outcome TEXT NOT NULL DEFAULT 'unknown',
    escalated_to TEXT NOT NULL DEFAULT '', findings_summary TEXT NOT NULL DEFAULT '',
    diagnostic_report TEXT NOT NULL DEFAULT '', context_id TEXT NOT NULL DEFAULT '',
    operator TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatalf("creating the old table: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO playbook_runs (run_id, playbook_id, series_id, started_at) VALUES ('plr_old_pg', 'pb', 'pbs_x', '2026-10-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	s, err := audit.NewPlaybookRunStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("opening the store on an old database: %v", err)
	}
	old, err := s.GetByRunID(ctx, "plr_old_pg")
	if err != nil || old.RecoveryTarget != "" {
		t.Fatalf("old row = %+v, %v", old, err)
	}
	if err := s.Record(ctx, &audit.PlaybookRun{PlaybookID: "pb", SeriesID: "pbs_x", ExecutionMode: "agent",
		RecoveryTarget: "time=2026-10-27T14:05:00Z", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Record on the migrated table: %v", err)
	}
}
