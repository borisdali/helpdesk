package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// DBRollbackMode selects how pre-mutation state is captured for DML rollback.
type DBRollbackMode string

const (
	// RollbackModeRowCapture uses SELECT to snapshot affected rows before a DML
	// operation (Tier 1). Works on any PostgreSQL instance with normal SELECT access.
	// Bounded by the blast-radius max_rows_affected policy limit.
	RollbackModeRowCapture DBRollbackMode = "row_capture"

	// RollbackModeWALDecode uses logical replication slots and wal2json to decode
	// the exact WAL changes produced by the DML (Tier 2). Captures cascades and
	// trigger effects that Tier 1 misses, with no TOCTOU gap.
	// Requires: wal_level=logical, REPLICATION privilege.
	RollbackModeWALDecode DBRollbackMode = "wal_decode"

	// RollbackModeNone disables rollback capture (operation exceeds blast-radius or
	// capability detection failed).
	RollbackModeNone DBRollbackMode = "none"
)

// DBRollbackCapability describes the rollback capabilities of a target database
// connection, as determined by DetectRollbackCapability.
type DBRollbackCapability struct {
	// Mode is the auto-selected (or config-overridden) capture tier.
	Mode DBRollbackMode
	// WALLevel is the value of the wal_level GUC ("minimal", "replica", "logical").
	WALLevel string
	// HasReplication is true when the connecting user has the REPLICATION privilege.
	HasReplication bool
	// ReplicaIdentity maps table names to their relreplident value:
	//   'd' = default (PK only), 'f' = full (all columns), 'n' = nothing, 'i' = index.
	// Used by Tier 2 to determine whether old-values are available for UPDATE/DELETE.
	ReplicaIdentity map[string]string
}

// ReplicaIdentityFull returns true when the given table has REPLICA IDENTITY FULL,
// meaning WAL decode will include all column values for UPDATE and DELETE old-images.
func (c *DBRollbackCapability) ReplicaIdentityFull(schema, table string) bool {
	if c == nil || c.ReplicaIdentity == nil {
		return false
	}
	key := table
	if schema != "" && schema != "public" {
		key = schema + "." + table
	}
	return c.ReplicaIdentity[key] == "f"
}

// DetectRollbackCapability probes db to determine the best available rollback mode.
// schema is used to scope the REPLICA IDENTITY query; pass "" or "public" for the
// default schema.
//
// If modeOverride is set (from infrastructure.json "rollback_mode"), it takes
// precedence over auto-detection but still populates the capability fields.
func DetectRollbackCapability(ctx context.Context, db *sql.DB, schema, modeOverride string) (*DBRollbackCapability, error) {
	cap := &DBRollbackCapability{
		ReplicaIdentity: make(map[string]string),
	}

	// --- wal_level ---
	var walLevel string
	if err := db.QueryRowContext(ctx, "SHOW wal_level").Scan(&walLevel); err != nil {
		slog.Debug("rollback_cap: could not read wal_level", "err", err)
	}
	cap.WALLevel = walLevel

	// --- REPLICATION privilege ---
	var hasReplication bool
	if err := db.QueryRowContext(ctx,
		"SELECT rolreplication FROM pg_roles WHERE rolname = current_user",
	).Scan(&hasReplication); err != nil {
		slog.Debug("rollback_cap: could not read rolreplication", "err", err)
	}
	cap.HasReplication = hasReplication

	// --- REPLICA IDENTITY for tables in the schema ---
	if schema == "" {
		schema = "public"
	}
	rows, err := db.QueryContext(ctx, `
		SELECT relname, relreplident::text
		FROM pg_class
		JOIN pg_namespace ON pg_namespace.oid = relnamespace
		WHERE nspname = $1 AND relkind = 'r'
	`, schema)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var tableName, ident string
			if scanErr := rows.Scan(&tableName, &ident); scanErr == nil {
				key := tableName
				if schema != "public" {
					key = schema + "." + tableName
				}
				cap.ReplicaIdentity[key] = ident
			}
		}
	}

	// --- Select mode ---
	switch strings.ToLower(modeOverride) {
	case "wal_decode":
		cap.Mode = RollbackModeWALDecode
	case "row_capture":
		cap.Mode = RollbackModeRowCapture
	case "none":
		cap.Mode = RollbackModeNone
	default:
		// Auto-detect: prefer WAL decode when all prerequisites are met.
		if walLevel == "logical" && hasReplication {
			cap.Mode = RollbackModeWALDecode
		} else {
			cap.Mode = RollbackModeRowCapture
		}
	}

	return cap, nil
}

// WALBracket manages an ephemeral logical replication slot that brackets a single
// DML operation. Call Open before the DML and Close (deferred) after to capture
// the WAL changes. The slot is always dropped in Close, even on error.
type WALBracket struct {
	slotName  string
	lsnBefore string
	db        *sql.DB
	traceID   string
}

// NewWALBracket creates a WALBracket. traceID is embedded in the slot name for
// traceability; it is truncated to 8 characters.
func NewWALBracket(db *sql.DB, traceID string) *WALBracket {
	short := traceID
	if len(short) > 8 {
		short = short[:8]
	}
	return &WALBracket{
		slotName: fmt.Sprintf("helpdesk_rbk_%s", short),
		db:       db,
		traceID:  traceID,
	}
}

// Open creates the replication slot and captures the current WAL LSN.
// Must be called before the DML executes.
func (w *WALBracket) Open(ctx context.Context) error {
	_, err := w.db.ExecContext(ctx,
		"SELECT pg_create_logical_replication_slot($1, 'wal2json')", w.slotName)
	if err != nil {
		return fmt.Errorf("create replication slot %s: %w", w.slotName, err)
	}
	if err := w.db.QueryRowContext(ctx, "SELECT pg_current_wal_lsn()::text").Scan(&w.lsnBefore); err != nil {
		// Slot was created; drop it before returning the error.
		w.dropSlot(ctx)
		return fmt.Errorf("read lsn_before: %w", err)
	}
	return nil
}

// Close peeks at the WAL changes since Open, parses the wal2json output, drops
// the replication slot, and returns the captured changes.
// Always drops the slot, even on error.
func (w *WALBracket) Close(ctx context.Context) (lsnBefore, lsnAfter string, raw string, err error) {
	defer w.dropSlot(ctx)

	lsnBefore = w.lsnBefore

	var lsnAfterVal string
	if scanErr := w.db.QueryRowContext(ctx, "SELECT pg_current_wal_lsn()::text").Scan(&lsnAfterVal); scanErr != nil {
		return lsnBefore, "", "", fmt.Errorf("read lsn_after: %w", scanErr)
	}
	lsnAfter = lsnAfterVal

	// Peek (not consume) the changes so we don't advance the slot permanently.
	row := w.db.QueryRowContext(ctx,
		"SELECT data FROM pg_logical_slot_peek_changes($1, $2, NULL, 'format-version', '2') LIMIT 1",
		w.slotName, lsnAfter)
	var data string
	if scanErr := row.Scan(&data); scanErr != nil && scanErr != sql.ErrNoRows {
		return lsnBefore, lsnAfter, "", fmt.Errorf("peek wal2json changes: %w", scanErr)
	}
	return lsnBefore, lsnAfter, data, nil
}

func (w *WALBracket) dropSlot(ctx context.Context) {
	// Use a fresh context in case the caller's context is already cancelled.
	dropCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := w.db.ExecContext(dropCtx, "SELECT pg_drop_replication_slot($1)", w.slotName); err != nil {
		slog.Warn("rollback_cap: failed to drop replication slot",
			"slot", w.slotName, "err", err)
	}
}

// StaleSlotCleanup drops any leftover helpdesk_rbk_* replication slots older
// than maxAge. Call this as a background goroutine at agent startup.
func StaleSlotCleanup(ctx context.Context, db *sql.DB, maxAge time.Duration) {
	rows, err := db.QueryContext(ctx, `
		SELECT slot_name
		FROM pg_replication_slots
		WHERE slot_name LIKE 'helpdesk_rbk_%'
		  AND slot_type = 'logical'
		  AND active = false
		  AND (now() - confirmed_flush_lsn::text::timestamptz) > $1
	`, maxAge.String())
	if err != nil {
		slog.Warn("stale_slot_cleanup: query failed", "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		if _, err := db.ExecContext(ctx, "SELECT pg_drop_replication_slot($1)", name); err != nil {
			slog.Warn("stale_slot_cleanup: drop failed", "slot", name, "err", err)
		} else {
			slog.Info("stale_slot_cleanup: dropped stale slot", "slot", name)
		}
	}
}
