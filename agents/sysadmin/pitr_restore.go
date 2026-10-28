package main

// restore_to_point_in_time (v0.33): restore the database to a recovery target
// a human chose, instead of to the end of the archive.
//
// Order of operations, everything checkable before anything is changed:
//
//	policy (destructive) -> the instance is down -> the plan is recomputed and
//	must be feasible AND carry the digest the human approved -> quiesce ->
//	one maintenance script: refuse a standby or a live postmaster, move
//	pg_wal aside, pgbackrest restore with the target.
//
// What it does not do, deliberately:
//
//   - It never puts the saved pg_wal back and never pushes it to the archive.
//     restore_from_backup does both because it wants the newest data; here the
//     WAL past the target is exactly what is being discarded, and replaying it
//     would defeat the target. The saved copy stays on the host and the result
//     says where.
//   - It does not stop a serving instance. The existing refusal stands until
//     the live-instance work (it needs the archive flushed first).
//   - It does not choose the target or the backup. The target comes from the
//     approved request, the backup from the plan, and the digest ties the two
//     to what the human saw.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/adk/agent"

	"helpdesk/agentutil"
	"helpdesk/internal/audit"
	"helpdesk/internal/policy"
)

// RestoreToPITArgs defines arguments for restore_to_point_in_time.
type RestoreToPITArgs struct {
	Target             string `json:"target,omitempty" jsonschema:"Server ID from infrastructure config. Required whenever infrastructure config is loaded — omitting it then is treated as a resolution failure and errors loudly. If your connection_string/server ID doesn't match a known infrastructure entry, do not guess or omit target — report that as its own finding and escalate."`
	Stanza             string `json:"stanza,omitempty" jsonschema:"pgBackRest stanza name. When omitted, the only stanza present is used — an error if more than one exists."`
	RecoveryTargetType string `json:"recovery_target_type" jsonschema:"required,Where to stop: 'time' or 'lsn'. Supplied by the approved request, never chosen by you."`
	RecoveryTargetVal  string `json:"recovery_target_value" jsonschema:"required,The approved recovery target: an RFC3339 timestamp with a zone, or an LSN. Use exactly the value you were given."`
	PlanDigest         string `json:"plan_digest" jsonschema:"required,The plan_digest from the plan_point_in_time_recovery result that was approved. The restore recomputes the plan and refuses if it no longer matches."`
}

// RestoreToPITResult is the result of restore_to_point_in_time.
type RestoreToPITResult struct {
	Output string `json:"output"`
	// Plan is the plan the restore ran: backup, target, timelines.
	Plan PITRPlan `json:"plan"`
	// Prediction is where recovery should stop; verify_restore_recovery
	// compares it with what PostgreSQL did after the restart.
	Prediction *RestorePrediction `json:"prediction,omitempty"`
	// SavedWAL is where the pg_wal that was in the data directory was moved:
	// the WAL past the target, kept as a forensic copy. Empty when there was
	// no pg_wal to save.
	SavedWAL string `json:"saved_wal,omitempty"`
	// Quiesced says the instance was stopped for the restore and left that
	// way (Kubernetes), to be resumed with restart_container.
	Quiesced string `json:"quiesced,omitempty"`
	// HowToRecover says how to undo this restore if the target was wrong.
	HowToRecover string `json:"how_to_recover,omitempty"`
}

// pitrRestoreScript is the maintenance script: refuse a standby or a live
// postmaster, save pg_wal aside (a --delta restore deletes it), restore.
// restoreCmd is pitrRestoreCommand's output. Exit status 3 and 4 are the
// refusals, so a caller can tell them from a pgBackRest failure.
func pitrRestoreScript(restoreCmd string) string {
	return `set -e
PGDATA=$(grep '^pg1-path=' /etc/pgbackrest.conf | cut -d= -f2)
if [ -e "$PGDATA/standby.signal" ]; then
  echo "restore_to_point_in_time: refusing: $PGDATA/standby.signal exists, so this is a standby, and a point-in-time restore would fork its timeline" >&2
  exit 3
fi
if [ -f "$PGDATA/postmaster.pid" ]; then
  PID=$(head -1 "$PGDATA/postmaster.pid")
  if [ -n "$PID" ] && [ "$(cat /proc/$PID/comm 2>/dev/null)" = postgres ]; then
    echo "restore_to_point_in_time: refusing: postmaster $PID is running" >&2
    exit 4
  fi
  rm -f "$PGDATA/postmaster.pid"
fi
if WAL=$(cd "$PGDATA/pg_wal" 2>/dev/null && pwd -P); then
  PARENT=$(dirname "$(cd "$PGDATA" && pwd -P)")
  if [ -n "${HELPDESK_WAL_SAVE_IN_REPO:-}" ]; then
    PARENT=$(grep '^repo1-path=' /etc/pgbackrest.conf | cut -d= -f2)
  fi
  SAVED="$PARENT/pg_wal.saved-$(date -u +%Y%m%dT%H%M%SZ)"
  mkdir "$SAVED"
  echo "` + walTailSavedMarker + `$SAVED"
  for f in "$WAL"/*; do
    n=${f##*/}
    case "$n" in *[!0-9A-F]*) continue;; esac
    [ ${#n} -eq 24 ] || continue
    mv "$f" "$SAVED/"
  done
fi
` + restoreCmd
}

func restoreToPITImpl(ctx context.Context, args RestoreToPITArgs) (RestoreToPITResult, error) {
	target := RecoveryTarget{Type: args.RecoveryTargetType, Value: args.RecoveryTargetVal}
	now := time.Now()
	normalized, err := target.Normalize(now)
	if err != nil {
		return RestoreToPITResult{}, fmt.Errorf("restore_to_point_in_time: %w", err)
	}
	if strings.TrimSpace(args.PlanDigest) == "" {
		return RestoreToPITResult{}, fmt.Errorf("restore_to_point_in_time: refusing to restore — plan_digest is required: run plan_point_in_time_recovery and have its plan approved first")
	}
	stanza := args.Stanza
	if stanza == "" {
		status, err := readPgBackRestStatus(ctx, GetPgBackRestStatusArgs{Target: args.Target}, false)
		if err != nil {
			return RestoreToPITResult{}, fmt.Errorf("restore_to_point_in_time: resolving stanza name: %w", err)
		}
		stanza = status.Stanza
	}
	inst, err := instanceFor(args.Target)
	if err != nil {
		return RestoreToPITResult{}, fmt.Errorf("restore_to_point_in_time: %w", err)
	}
	if !inst.local && policyEnforcer != nil {
		policyCtx := agentutil.WithToolName(ctx, "restore_to_point_in_time")
		if err := policyEnforcer.CheckTool(policyCtx, "host", args.Target, policy.ActionDestructive, inst.host.Tags,
			fmt.Sprintf("point-in-time restore (stanza=%s, %s target %s)", stanza, target.Type, normalized), inst.host.Sensitivity); err != nil {
			slog.Warn("policy denied point-in-time restore", "target", args.Target, "err", err)
			return RestoreToPITResult{}, err
		}
	}
	if up, readyOut := inst.serving(ctx); up {
		return RestoreToPITResult{}, fmt.Errorf("restore_to_point_in_time: refusing to restore — postgres is still accepting connections (pg_isready: %s)", readyOut)
	}

	// The plan is recomputed now, not trusted from the approval: the repo may
	// have changed since. It must be feasible and be the plan that was approved.
	plan, err := planPITRImpl(ctx, PlanPITRArgs{Target: args.Target, Stanza: stanza,
		RecoveryTargetType: target.Type, RecoveryTargetVal: target.Value})
	if err != nil {
		return RestoreToPITResult{}, fmt.Errorf("restore_to_point_in_time: refusing to restore — could not plan the recovery: %w", err)
	}
	if plan.Status != PITRFeasible {
		return RestoreToPITResult{Plan: plan}, fmt.Errorf("restore_to_point_in_time: refusing to restore — the plan is %s: %s", plan.Status, plan.Detail)
	}
	if plan.PlanDigest != strings.TrimSpace(args.PlanDigest) {
		return RestoreToPITResult{Plan: plan}, fmt.Errorf("restore_to_point_in_time: refusing to restore — the plan changed since it was approved (approved %s, now %s: backup %s, timeline %d). Run plan_point_in_time_recovery again and have the new plan approved",
			args.PlanDigest, plan.PlanDigest, plan.BackupLabel, plan.TargetTimeline)
	}

	quiesceOut, err := inst.quiesce(ctx)
	if err != nil {
		return RestoreToPITResult{}, fmt.Errorf("restore_to_point_in_time: stopping the instance before the restore: %w", err)
	}
	script := pitrRestoreScript(pitrRestoreCommand(plan.Stanza, plan.BackupLabel, target, plan.NormalizedTarget, plan.TargetTimeline))
	if inst.k8s() {
		script = "HELPDESK_WAL_SAVE_IN_REPO=1; export HELPDESK_WAL_SAVE_IN_REPO\n" + script
	}
	raw, err := inst.maintenance(ctx, script, true)
	savedTo, _, out := parseWALTailOutput(raw)
	result := RestoreToPITResult{Output: out, Plan: plan, SavedWAL: savedTo}
	result.HowToRecover = pitrRecoverNote(plan, savedTo)
	if err != nil {
		// pgBackRest may have stopped partway through --delta: PGDATA is not
		// usable, and running the same restore again is the way forward.
		return result, fmt.Errorf("restore_to_point_in_time: %w: %s\n%s", err, out, result.HowToRecover)
	}
	if inst.quiesced() {
		result.Quiesced = quiesceOut + " — call restart_container to resume it; recovery runs on startup and stops at the target"
	}
	pred := RestorePrediction{
		BackupLabel:      plan.BackupLabel,
		ArchiveEndTime:   plan.ArchiveEndTime,
		ExpectedTimeline: plan.ExpectedTimeline,
		TargetType:       target.Type,
		TargetValue:      plan.NormalizedTarget,
		PredictedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	result.Prediction = &pred
	saveRestorePrediction(args.Target, pred)
	return result, nil
}

// pitrRecoverNote says what is left to fall back on.
func pitrRecoverNote(plan PITRPlan, savedTo string) string {
	var b strings.Builder
	if savedTo != "" {
		fmt.Fprintf(&b, "The WAL that was in pg_wal is saved in %s. ", savedTo)
	}
	fmt.Fprintf(&b, "If the target was wrong, restore_from_backup restores to the end of the archive again (backups and archived WAL are untouched); "+
		"if recovery reports that the target was not reached, repeat this restore with an earlier target.")
	return b.String()
}

// RestoreToPITModelArgs is what the model sees of restore_to_point_in_time:
// which instance, not where to stop. The target, the plan digest and the
// acknowledgement come from the human's approval, carried in the request
// context by the gateway (audit.RecoveryApproval), so a model cannot choose,
// adjust or invent them.
type RestoreToPITModelArgs struct {
	Target string `json:"target,omitempty" jsonschema:"Server ID from infrastructure config. Required whenever infrastructure config is loaded — omitting it then is treated as a resolution failure and errors loudly. If your connection_string/server ID doesn't match a known infrastructure entry, do not guess or omit target — report that as its own finding and escalate."`
	Stanza string `json:"stanza,omitempty" jsonschema:"pgBackRest stanza name. When omitted, the only stanza present is used — an error if more than one exists."`
}

// errNoRecoveryApproval is what the tool answers when no human approved a
// recovery target for this request.
const errNoRecoveryApproval = "restore_to_point_in_time: refusing to restore — no approved recovery target for this request. " +
	"This tool restores only to a target a human supplied and approved (with the plan digest and an acknowledgement that later data is discarded) at the gateway; it takes no target from you. " +
	"Report the plan from plan_point_in_time_recovery and stop: a human approves it at the gate"

// restoreToPITTool is the tool the model calls. Everything that decides what
// is overwritten comes from the approval in ctx.
func restoreToPITTool(ctx agent.ToolContext, margs RestoreToPITModelArgs) (RestoreToPITResult, error) {
	ap := audit.RecoveryApprovalFromContext(ctx)
	if !ap.Approved() {
		err := fmt.Errorf("%s", errNoRecoveryApproval)
		recordRestoreToPIT(ctx, RestoreToPITArgs{Target: margs.Target, Stanza: margs.Stanza}, RestoreToPITResult{}, err, 0)
		return RestoreToPITResult{}, err
	}
	return restoreToPITAudited(ctx, RestoreToPITArgs{Target: margs.Target, Stanza: margs.Stanza,
		RecoveryTargetType: ap.Type, RecoveryTargetVal: ap.Value, PlanDigest: ap.PlanDigest})
}

// restoreToPITAudited runs the restore and writes its audit record.
func restoreToPITAudited(ctx agent.ToolContext, args RestoreToPITArgs) (RestoreToPITResult, error) {
	start := time.Now()
	result, err := restoreToPITImpl(ctx, args)
	duration := time.Since(start)
	if err == nil {
		slog.Info("tool ok", "name", "restore_to_point_in_time", "ms", duration.Milliseconds())
	}
	recordRestoreToPIT(ctx, args, result, err, duration)
	return result, err
}

func recordRestoreToPIT(ctx agent.ToolContext, args RestoreToPITArgs, result RestoreToPITResult, err error, duration time.Duration) {
	if toolAuditor == nil {
		return
	}
	var errMsg string
	if err != nil {
		errMsg = err.Error()
	}
	resultJSON, _ := json.Marshal(result)
	toolAuditor.RecordToolCall(ctx, audit.ToolCall{
		Name: "restore_to_point_in_time",
		Parameters: map[string]any{"target": args.Target, "stanza": args.Stanza,
			"recovery_target_type": args.RecoveryTargetType, "recovery_target_value": args.RecoveryTargetVal, "plan_digest": args.PlanDigest},
	}, audit.ToolResult{
		Output: string(resultJSON),
		Error:  errMsg,
	}, duration)
}
