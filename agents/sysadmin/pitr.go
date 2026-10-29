package main

// Point-in-time recovery, pure logic (v0.33).
//
// restore_from_backup restores the latest backup and replays to the end of the
// archive. A point-in-time restore stops earlier, at a target a human chose:
// "the table was dropped at 14:05". This file holds the parts that need no
// infrastructure: validating the target, choosing the backup that can reach
// it, and building the pgBackRest command. The target is never chosen by the
// model: the gateway injects it from the approved request (see the plan in
// docs/BACKUP.md), and everything here treats it as untrusted input anyway,
// because it ends up in a shell script that runs as the postgres user.

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Recovery target types supported in v1. Named restore points and xids can't
// be checked against the archive before PGDATA is overwritten, so they wait.
const (
	TargetTime = "time"
	TargetLSN  = "lsn"
)

// RecoveryTarget is where a point-in-time restore must stop.
type RecoveryTarget struct {
	// Type is "time" or "lsn".
	Type string `json:"type"`
	// Value is an RFC3339 timestamp with a zone ("2026-10-27T14:05:00Z"), or
	// an LSN ("0/3000028").
	Value string `json:"value"`
}

// pitrTimeFormat is what PostgreSQL's recovery_target_time and pgBackRest's
// --target accept: a timestamp with an explicit UTC offset, no "T".
const pitrTimeFormat = "2006-01-02 15:04:05.999999999-07"

// pitrSafeValue is the only shape a normalized target can have. It is checked
// again after normalization so that a future change to the parsing can't let
// shell syntax through.
var pitrSafeValue = regexp.MustCompile(`^[0-9A-Fa-f :./+-]+$`)

// Normalize validates the target and returns the value as pgBackRest's
// --target wants it. now is passed in so tests are deterministic.
func (t RecoveryTarget) Normalize(now time.Time) (string, error) {
	var out string
	switch t.Type {
	case TargetTime:
		ts, err := time.Parse(time.RFC3339, strings.TrimSpace(t.Value))
		if err != nil {
			return "", fmt.Errorf("recovery target time %q is not RFC3339 with a time zone (e.g. 2026-10-27T14:05:00Z): %w", t.Value, err)
		}
		if ts.After(now) {
			return "", fmt.Errorf("recovery target time %s is in the future", ts.UTC().Format(time.RFC3339))
		}
		out = ts.UTC().Format(pitrTimeFormat)
	case TargetLSN:
		lsn, err := parseLSN(t.Value)
		if err != nil {
			return "", fmt.Errorf("recovery target LSN: %w", err)
		}
		out = fmt.Sprintf("%X/%X", lsn>>32, lsn&0xFFFFFFFF)
	case "":
		return "", fmt.Errorf("recovery target type is required (time or lsn)")
	default:
		return "", fmt.Errorf("recovery target type %q is not supported (time or lsn)", t.Type)
	}
	if !pitrSafeValue.MatchString(out) {
		return "", fmt.Errorf("recovery target %q contains characters that are not allowed", out)
	}
	return out, nil
}

// selectPITRBackup returns the backup to restore for target: the newest one
// that finished strictly before it. pgBackRest would pick one itself for a
// time target, but passing --set makes the WAL check and the restore agree on
// which backup, and lets Go enforce the rule instead of trusting the tool.
// Backups with Error set are skipped. The error says why none qualifies.
func selectPITRBackup(backups []PgBackRestBackup, target RecoveryTarget, now time.Time) (PgBackRestBackup, error) {
	norm, err := target.Normalize(now)
	if err != nil {
		return PgBackRestBackup{}, err
	}
	var targetTime time.Time
	var targetLSN uint64
	switch target.Type {
	case TargetTime:
		targetTime, _ = time.Parse(time.RFC3339, strings.TrimSpace(target.Value))
	case TargetLSN:
		targetLSN, _ = parseLSN(norm)
	}
	var best *PgBackRestBackup
	var oldest time.Time
	for i := range backups {
		b := &backups[i]
		if b.Error {
			continue
		}
		if oldest.IsZero() || b.StopTime.Before(oldest) {
			oldest = b.StopTime
		}
		switch target.Type {
		case TargetTime:
			if !b.StopTime.Before(targetTime) {
				continue
			}
		case TargetLSN:
			stop, err := parseLSN(b.LSNStop)
			if err != nil || stop >= targetLSN {
				continue
			}
		}
		if best == nil || b.StopTime.After(best.StopTime) {
			best = b
		}
	}
	if best == nil {
		if oldest.IsZero() {
			return PgBackRestBackup{}, fmt.Errorf("no usable backup in the repository")
		}
		return PgBackRestBackup{}, fmt.Errorf("no backup finished before the recovery target %s (the oldest usable backup finished at %s)",
			norm, oldest.UTC().Format(time.RFC3339))
	}
	return *best, nil
}

// pitrRestoreCommand is the pgBackRest invocation for a point-in-time
// restore. timeline is the timeline the cluster was on before the restore:
// pinned so that a second PITR after this one has an unambiguous path
// (recovery_target_timeline=latest would follow whichever branch is newest).
// promote, because pgBackRest's other option, pause, leaves the instance
// read-only on the old timeline with no "selected new timeline" line to verify.
func pitrRestoreCommand(stanza, label string, target RecoveryTarget, normalized string, timeline uint32) string {
	return fmt.Sprintf("pgbackrest --stanza=%s --delta --set=%s --type=%s --target=%s --target-timeline=%d --target-action=promote restore",
		shellQuote(stanza), shellQuote(label), target.Type, shellQuote(normalized), timeline)
}
