package main

// plan_point_in_time_recovery (v0.33): the dry run of a point-in-time restore.
//
// It changes nothing. It answers, before anyone approves anything: which
// backup would be restored, whether the archived WAL from that backup to the
// end of the archive is whole, whether the target lies inside the archive,
// and what the restore would throw away. restore_to_point_in_time recomputes
// the same plan and refuses when it no longer matches the digest the human
// approved.
//
// v1 is conservative: the WAL must be continuous from the selected backup to
// the END of the archive (not just to the target), the same bar as
// restore_from_backup, and a target past the archive end is refused because
// reaching it would need WAL that was never archived.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/adk/agent"

	"helpdesk/internal/audit"
)

// Plan statuses.
const (
	PITRFeasible    = "feasible"
	PITRNotFeasible = "not_feasible"
	PITRUnverified  = "unverified"
)

// PlanPITRArgs defines arguments for plan_point_in_time_recovery.
type PlanPITRArgs struct {
	Target             string `json:"target,omitempty" jsonschema:"Server ID from infrastructure config. Required whenever infrastructure config is loaded — omitting it then is treated as a resolution failure and errors loudly. If your connection_string/server ID doesn't match a known infrastructure entry, do not guess or omit target — report that as its own finding and escalate."`
	Stanza             string `json:"stanza,omitempty" jsonschema:"pgBackRest stanza name. When omitted, the only stanza present is used — an error if more than one exists."`
	RecoveryTargetType string `json:"recovery_target_type" jsonschema:"required,Where to stop: 'time' or 'lsn'."`
	RecoveryTargetVal  string `json:"recovery_target_value" jsonschema:"required,For 'time' an RFC3339 timestamp with a time zone (2026-10-27T14:05:00Z); for 'lsn' a WAL location (0/3000028). Use the value you were given — never infer or guess one."`
}

// PITRPlan is the result of plan_point_in_time_recovery.
type PITRPlan struct {
	// Status is feasible, not_feasible or unverified.
	Status string `json:"status"`
	// Detail says why a plan is not feasible or unverified, or what a
	// feasible one will and will not do.
	Detail string `json:"detail"`
	Stanza string `json:"stanza,omitempty"`
	// Target is the recovery target as given; NormalizedTarget the value
	// pgBackRest is passed.
	Target           RecoveryTarget `json:"recovery_target"`
	NormalizedTarget string         `json:"normalized_target,omitempty"`
	BackupLabel      string         `json:"backup_label,omitempty"`
	BackupStopTime   string         `json:"backup_stop_time,omitempty"`
	// TargetTimeline is the timeline the restore would pin; ExpectedTimeline
	// the one PostgreSQL should create. v1 uses the archive's newest timeline.
	TargetTimeline   uint32 `json:"target_timeline,omitempty"`
	ExpectedTimeline uint32 `json:"expected_timeline,omitempty"`
	ArchiveEndWAL    string `json:"archive_end_wal,omitempty"`
	ArchiveEndTime   string `json:"archive_end_time,omitempty"`
	// DataLoss says what the restore discards.
	DataLoss      string         `json:"data_loss,omitempty"`
	WALContinuity *WALContinuity `json:"wal_continuity,omitempty"`
	// PlanDigest identifies the decision: stanza, backup, target and
	// timeline. Not the archive end: on a live instance the archive keeps
	// growing, and a digest that moved with it would always drift.
	PlanDigest string `json:"plan_digest,omitempty"`
}

// pitrPlanDigest is the short hash a human approves and the restore tool
// recomputes.
func pitrPlanDigest(stanza, label, normalized string, timeline uint32) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d", stanza, label, normalized, timeline)))
	return hex.EncodeToString(sum[:])[:16]
}

// assessPITRPlan is the pure core: backup is the one selectPITRBackup chose
// and wal the continuity check for it.
func assessPITRPlan(stanza string, target RecoveryTarget, normalized string, backup PgBackRestBackup, wal *WALContinuity) PITRPlan {
	plan := PITRPlan{
		Stanza:           stanza,
		Target:           target,
		NormalizedTarget: normalized,
		BackupLabel:      backup.Label,
		BackupStopTime:   backup.StopTime.UTC().Format(time.RFC3339),
		WALContinuity:    wal,
	}
	notFeasible := func(format string, args ...any) PITRPlan {
		plan.Status, plan.Detail = PITRNotFeasible, fmt.Sprintf(format, args...)
		return plan
	}
	unverified := func(format string, args ...any) PITRPlan {
		plan.Status, plan.Detail = PITRUnverified, fmt.Sprintf(format, args...)
		return plan
	}

	if wal == nil {
		return unverified("the WAL continuity of backup %s was not checked", backup.Label)
	}
	plan.ArchiveEndWAL, plan.ArchiveEndTime = wal.ArchiveEndWAL, wal.ArchiveEndTime
	switch wal.Status {
	case WALContinuous:
	case WALBroken:
		return notFeasible("the archived WAL after backup %s is not continuous: %s. A human must decide whether a restore that cannot be verified to reach the end of the archive is worth it", backup.Label, wal.Detail)
	default:
		return unverified("WAL continuity of backup %s is %s: %s", backup.Label, wal.Status, wal.Detail)
	}
	if wal.LatestTimeline == 0 {
		return unverified("the check did not report the archive's newest timeline")
	}
	plan.TargetTimeline, plan.ExpectedTimeline = wal.LatestTimeline, wal.LatestTimeline+1

	switch target.Type {
	case TargetTime:
		tt, err := time.Parse(time.RFC3339, strings.TrimSpace(target.Value))
		end, err2 := time.Parse(time.RFC3339, wal.ArchiveEndTime)
		if err != nil || err2 != nil {
			return unverified("the target time or the archive end time (%q) could not be read", wal.ArchiveEndTime)
		}
		if !tt.Before(end) {
			return notFeasible("the target %s is not before the time the newest archived WAL segment (%s) reached the repository, %s: reaching it would need WAL that was never archived. restore_from_backup restores to the end of the archive",
				normalized, wal.ArchiveEndWAL, wal.ArchiveEndTime)
		}
		plan.DataLoss = fmt.Sprintf("everything committed after %s, up to the end of the archive at %s, is discarded", normalized, wal.ArchiveEndTime)
	case TargetLSN:
		lsn, err := parseLSN(normalized)
		_, logID, seg, err2 := parseSegmentName(wal.ArchiveEndWAL)
		if err != nil || err2 != nil || !validSegmentSize(wal.SegmentSizeBytes) {
			return unverified("the target LSN cannot be placed in the archive: the archive end segment or the WAL segment size is unknown")
		}
		endSeg := logID*segsPerLogID(wal.SegmentSizeBytes) + seg
		targetSeg := lsn / wal.SegmentSizeBytes
		if targetSeg > endSeg {
			return notFeasible("the target %s is in a WAL segment after the newest archived one (%s): reaching it would need WAL that was never archived", normalized, wal.ArchiveEndWAL)
		}
		plan.DataLoss = fmt.Sprintf("all WAL after %s, up to the end of the archive (%s, %d segment(s) later), is discarded", normalized, wal.ArchiveEndWAL, endSeg-targetSeg)
	default:
		return unverified("unknown recovery target type %q", target.Type)
	}

	plan.PlanDigest = pitrPlanDigest(stanza, backup.Label, normalized, plan.TargetTimeline)
	plan.Status = PITRFeasible
	plan.Detail = fmt.Sprintf("backup %s can be restored and the WAL from it to the end of the archive is continuous; the target is inside the archive. "+
		"This is plausible, not certain: the archive's times are coarse, and recovery can still run out of WAL before the target", backup.Label)
	return plan
}

func planPITRImpl(ctx context.Context, args PlanPITRArgs) (PITRPlan, error) {
	target := RecoveryTarget{Type: args.RecoveryTargetType, Value: args.RecoveryTargetVal}
	now := time.Now()
	normalized, err := target.Normalize(now)
	if err != nil {
		return PITRPlan{}, fmt.Errorf("plan_point_in_time_recovery: %w", err)
	}
	status, err := readPgBackRestStatus(ctx, GetPgBackRestStatusArgs{Target: args.Target, Stanza: args.Stanza}, false)
	if err != nil {
		return PITRPlan{}, fmt.Errorf("plan_point_in_time_recovery: reading the backup state: %w", err)
	}
	plan := PITRPlan{Stanza: status.Stanza, Target: target, NormalizedTarget: normalized}
	if status.StatusCode != 0 {
		plan.Status = PITRNotFeasible
		plan.Detail = fmt.Sprintf("pgBackRest reports the stanza unhealthy (status_code=%d: %s)", status.StatusCode, status.StatusMessage)
		return plan, nil
	}
	backup, err := selectPITRBackup(status.Backups, target, now)
	if err != nil {
		plan.Status = PITRNotFeasible
		plan.Detail = err.Error()
		return plan, nil
	}
	wal := checkWALContinuityFor(status, backup.Label, func(walArgs []string) (string, error) {
		return runPgBackRestOnTarget(ctx, "plan_point_in_time_recovery", args.Target, walArgs)
	})
	return assessPITRPlan(status.Stanza, target, normalized, backup, &wal), nil
}

func planPITRTool(ctx agent.ToolContext, args PlanPITRArgs) (PITRPlan, error) {
	start := time.Now()
	result, err := planPITRImpl(ctx, args)
	duration := time.Since(start)
	if err == nil {
		slog.Info("tool ok", "name", "plan_point_in_time_recovery", "ms", duration.Milliseconds(), "status", result.Status)
	}
	if toolAuditor != nil {
		var errMsg string
		if err != nil {
			errMsg = err.Error()
		}
		resultJSON, _ := json.Marshal(result)
		toolAuditor.RecordToolCall(ctx, audit.ToolCall{
			Name:       "plan_point_in_time_recovery",
			Parameters: map[string]any{"target": args.Target, "stanza": args.Stanza, "recovery_target_type": args.RecoveryTargetType, "recovery_target_value": args.RecoveryTargetVal},
		}, audit.ToolResult{
			Output: string(resultJSON),
			Error:  errMsg,
		}, duration)
	}
	return result, err
}
