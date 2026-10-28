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
	"sync"
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
	// AllowStopServing is the human's authorization, given at the gate, for
	// this restore to stop a serving instance. Without it a serving instance
	// is refused. Not part of the model-facing schema (RestoreToPITModelArgs).
	AllowStopServing bool `json:"allow_stop_serving,omitempty"`
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
	// PreStop is what a serving instance reported just before it was stopped:
	// its WAL position and clock, the most that could have been recovered.
	PreStop *LivePreState `json:"pre_stop,omitempty"`
	// Stopped says this tool stopped a serving instance (and how). It is left
	// stopped: restart_container / restart_service starts it, and recovery to
	// the target runs on startup.
	Stopped string `json:"stopped,omitempty"`
}

// pitrRefusedMarker starts the line the restore script prints when it refuses
// before changing anything, so a caller that stopped the instance for it knows
// the data directory is untouched and the instance can simply be started again.
const pitrRefusedMarker = "HELPDESK_PITR_REFUSED: "

// pitrRestoreScript is the maintenance script: refuse a standby, a live
// postmaster or too little room for the saved WAL, save pg_wal aside (a
// --delta restore deletes it), restore. restoreCmd is pitrRestoreCommand's
// output. Exit status 3, 4 and 5 are the refusals (each prints
// pitrRefusedMarker), so a caller can tell them from a pgBackRest failure.
func pitrRestoreScript(restoreCmd string) string {
	return `set -e
PGDATA=$(grep '^pg1-path=' /etc/pgbackrest.conf | cut -d= -f2)
if [ -e "$PGDATA/standby.signal" ]; then
  echo "` + pitrRefusedMarker + `$PGDATA/standby.signal exists, so this is a standby, and a point-in-time restore would fork its timeline"
  exit 3
fi
if [ -f "$PGDATA/postmaster.pid" ]; then
  PID=$(head -1 "$PGDATA/postmaster.pid")
  if [ -n "$PID" ] && [ "$(cat /proc/$PID/comm 2>/dev/null)" = postgres ]; then
    echo "` + pitrRefusedMarker + `postmaster $PID is running"
    exit 4
  fi
  rm -f "$PGDATA/postmaster.pid"
fi
if WAL=$(cd "$PGDATA/pg_wal" 2>/dev/null && pwd -P); then
  PARENT=$(dirname "$(cd "$PGDATA" && pwd -P)")
  if [ -n "${HELPDESK_WAL_SAVE_IN_REPO:-}" ]; then
    PARENT=$(grep '^repo1-path=' /etc/pgbackrest.conf | cut -d= -f2)
  fi
  NEED=$(du -sk "$WAL" 2>/dev/null | cut -f1)
  FREE=$(df -Pk "$PARENT" 2>/dev/null | awk 'NR==2{print $4}')
  if [ -n "$NEED" ] && [ -n "$FREE" ] && [ "$FREE" -lt $((NEED + NEED / 10 + 1024)) ]; then
    echo "` + pitrRefusedMarker + `not enough room beside the data directory to keep the WAL that is in pg_wal (${NEED} KB to save, ${FREE} KB free in $PARENT)"
    exit 5
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

// pitrInFlight holds the targets with a point-in-time restore running in this
// agent. A model that retries a slow tool call (the archive flush alone can
// wait a minute and a half) would otherwise start a second restore on a data
// directory the first is rewriting. In-process: the agent runs as one replica.
var pitrInFlight = struct {
	sync.Mutex
	targets map[string]bool
}{targets: map[string]bool{}}

// beginPITRestore claims target, or reports that a restore of it is running.
func beginPITRestore(target string) (release func(), ok bool) {
	pitrInFlight.Lock()
	defer pitrInFlight.Unlock()
	if pitrInFlight.targets[target] {
		return nil, false
	}
	pitrInFlight.targets[target] = true
	return func() {
		pitrInFlight.Lock()
		delete(pitrInFlight.targets, target)
		pitrInFlight.Unlock()
	}, true
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
	release, ok := beginPITRestore(args.Target)
	if !ok {
		return RestoreToPITResult{}, fmt.Errorf("restore_to_point_in_time: refusing to restore — a point-in-time restore of %q is already running; do not retry it, wait for its result", args.Target)
	}
	defer release()
	if !inst.local && policyEnforcer != nil {
		policyCtx := agentutil.WithToolName(ctx, "restore_to_point_in_time")
		if err := policyEnforcer.CheckTool(policyCtx, "host", args.Target, policy.ActionDestructive, inst.host.Tags,
			fmt.Sprintf("point-in-time restore (stanza=%s, %s target %s)", stanza, target.Type, normalized), inst.host.Sensitivity); err != nil {
			slog.Warn("policy denied point-in-time restore", "target", args.Target, "err", err)
			return RestoreToPITResult{}, err
		}
	}
	planArgs := PlanPITRArgs{Target: args.Target, Stanza: stanza, RecoveryTargetType: target.Type, RecoveryTargetVal: target.Value}
	var result RestoreToPITResult

	// A serving instance is restored only when the human's gate approval said
	// the tool may stop it. Everything that can be checked while it is still
	// running is checked first, and a failure at any of those steps leaves it
	// running (pitr_live.go).
	live := false
	if up, readyOut := inst.serving(ctx); up {
		if !args.AllowStopServing {
			return result, fmt.Errorf("restore_to_point_in_time: refusing to restore — postgres is still accepting connections (pg_isready: %s). "+
				"Stopping a serving instance needs the gate approval to allow it (allow_stop_serving), which only a human can give", readyOut)
		}
		live = true
		// Nothing that could keep a colocated instance down and bring it back
		// exists: refuse before probing or flushing anything.
		if inst.local {
			return result, fmt.Errorf("restore_to_point_in_time: refusing to restore — a colocated instance (no infrastructure config) cannot be stopped and restored by this tool: only a Docker/Podman container, a systemd host or a Kubernetes StatefulSet can. The instance was not stopped")
		}
		pre, err := livePreflight(ctx, inst)
		if err != nil {
			return result, fmt.Errorf("restore_to_point_in_time: refusing to restore — %w. The instance was not stopped", err)
		}
		result.PreStop = &pre
		if reason := pre.refusal(target, normalized); reason != "" {
			return result, fmt.Errorf("restore_to_point_in_time: refusing to restore — %s. The instance was not stopped", reason)
		}
		if err := flushArchive(ctx, inst); err != nil {
			return result, fmt.Errorf("restore_to_point_in_time: refusing to restore — %w. The instance was not stopped", err)
		}
	}

	// The plan is recomputed now, not trusted from the approval: the repo may
	// have changed since. It must be feasible and be the plan that was
	// approved. For a serving instance it is recomputed from the repo after
	// the flush, and must no longer need one.
	var plan PITRPlan
	if live {
		plan, err = awaitFlushedPlan(ctx, planArgs)
	} else {
		plan, err = planPITRImpl(ctx, planArgs)
	}
	if err != nil {
		return result, fmt.Errorf("restore_to_point_in_time: refusing to restore — could not plan the recovery: %w", err)
	}
	result.Plan = plan
	if plan.Status != PITRFeasible {
		return result, fmt.Errorf("restore_to_point_in_time: refusing to restore — the plan is %s: %s", plan.Status, plan.Detail)
	}
	if plan.ArchiveFlushRequired {
		return result, fmt.Errorf("restore_to_point_in_time: refusing to restore — the WAL holding the target did not reach the archive after the flush (newest archived segment %s at %s). The instance was not stopped",
			plan.ArchiveEndWAL, plan.ArchiveEndTime)
	}
	if plan.PlanDigest != strings.TrimSpace(args.PlanDigest) {
		return result, fmt.Errorf("restore_to_point_in_time: refusing to restore — the plan changed since it was approved (approved %s, now %s: backup %s, timeline %d). Run plan_point_in_time_recovery again and have the new plan approved",
			args.PlanDigest, plan.PlanDigest, plan.BackupLabel, plan.TargetTimeline)
	}

	// Stop it. From here the instance is down whatever happens next.
	var stopOut string
	if live {
		stopOut, err = inst.stopServing(ctx)
		if err != nil {
			return result, fmt.Errorf("restore_to_point_in_time: stopping the instance: %w. It may still be running; nothing else was changed", err)
		}
		inst = inst.afterStop()
		result.Stopped = stopOut
		if err := confirmStopped(ctx, inst); err != nil {
			return result, fmt.Errorf("restore_to_point_in_time: %w. The restore was not run", err)
		}
	} else {
		stopOut, err = inst.quiesce(ctx)
		if err != nil {
			return result, fmt.Errorf("restore_to_point_in_time: stopping the instance before the restore: %w", err)
		}
	}

	script := pitrRestoreScript(pitrRestoreCommand(plan.Stanza, plan.BackupLabel, target, plan.NormalizedTarget, plan.TargetTimeline))
	if inst.k8s() {
		script = "HELPDESK_WAL_SAVE_IN_REPO=1; export HELPDESK_WAL_SAVE_IN_REPO\n" + script
	}
	raw, err := inst.maintenance(ctx, script, true)
	savedTo, _, out := parseWALTailOutput(raw)
	result.Output, result.SavedWAL = out, savedTo
	result.HowToRecover = pitrRecoverNote(plan, savedTo)
	if err != nil {
		if live && strings.Contains(raw, pitrRefusedMarker) {
			// The script refused before it changed anything: put the instance
			// back as it was.
			reason := strings.TrimSpace(raw[strings.Index(raw, pitrRefusedMarker)+len(pitrRefusedMarker):])
			if line, _, ok := strings.Cut(reason, "\n"); ok {
				reason = line
			}
			if resumed, rerr := inst.startAfterStop(ctx); rerr != nil {
				result.HowToRecover = "The instance was stopped for the restore and could not be started again (" + rerr.Error() + "): start it with restart_container / restart_service. " + result.HowToRecover
				return result, fmt.Errorf("restore_to_point_in_time: the restore refused (%s) before changing anything, but starting the instance again failed: %v", reason, rerr)
			} else {
				return result, fmt.Errorf("restore_to_point_in_time: the restore refused (%s) before changing anything; the instance was started again (%s)", reason, strings.TrimSpace(resumed))
			}
		}
		// pgBackRest may have stopped partway through --delta: PGDATA is not
		// usable, and running the same restore again is the way forward.
		if live {
			result.HowToRecover = "This tool stopped the instance and it is still stopped. " + result.HowToRecover
		}
		return result, fmt.Errorf("restore_to_point_in_time: %w: %s\n%s", err, out, result.HowToRecover)
	}
	switch {
	case inst.quiesced():
		result.Quiesced = stopOut + " — call restart_container to resume it; recovery runs on startup and stops at the target"
	case live:
		result.Stopped = stopOut + " — start it with restart_container / restart_service: recovery runs on startup and stops at the target"
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

// stopConfirmWait bounds how long confirmStopped waits for the instance to
// stop answering; a variable so tests don't wait it out.
var stopConfirmWait = 15 * time.Second

// confirmStopped checks the instance no longer accepts connections after
// stopServing, giving a slow shutdown a few seconds. It does not start it
// again: an instance that is still up is no worse off than before.
func confirmStopped(ctx context.Context, inst pgInstance) error {
	deadline := time.Now().Add(stopConfirmWait)
	for {
		up, out := inst.serving(ctx)
		if !up {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the instance is still accepting connections after it was stopped (pg_isready: %s)", out)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
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
		RecoveryTargetType: ap.Type, RecoveryTargetVal: ap.Value, PlanDigest: ap.PlanDigest, AllowStopServing: ap.StopServing})
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
			"recovery_target_type": args.RecoveryTargetType, "recovery_target_value": args.RecoveryTargetVal, "plan_digest": args.PlanDigest,
			"allow_stop_serving": args.AllowStopServing},
	}, audit.ToolResult{
		Output: string(resultJSON),
		Error:  errMsg,
	}, duration)
}
