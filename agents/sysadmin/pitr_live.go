package main

// Point-in-time restore of a SERVING instance (v0.33, P4).
//
// restore_to_point_in_time restores an instance that is down. It can also be
// authorized, by the human at the gate (audit.RecoveryApproval.StopServing),
// to stop a serving one first. This file is everything that has to be true
// and done before that stop. The order matters, and every step before the
// stop changes nothing that matters (a WAL segment switch at most):
//
//	livePreflight   what the running instance says about itself: not a
//	                standby, no replicas, archiving on, no HA manager, and a
//	                target that is not ahead of it
//	flushArchive    pg_switch_wal, then wait until every completed segment is
//	                archived and the archiver is not failing: the target may be
//	                in WAL that had not been archived yet, and the archive is
//	                what the restore replays
//	awaitFlushedPlan the plan, recomputed from the repo after the flush, must
//	                be feasible without needing a flush, and be the plan the
//	                human approved (the digest)
//	stopServing     (lifecycle.go) only then is the instance stopped
//
// A failure at any of these leaves the instance running. After the stop the
// restore script has its own refusals (standby, live postmaster, disk space);
// when one of those fires nothing has been changed, and the instance is
// started again.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// LivePreState is what a serving instance reported about itself just before
// the restore. It is recorded in the result and so in the audit trail: the
// position the instance was at when it was stopped.
type LivePreState struct {
	InRecovery    bool   `json:"in_recovery"`
	Replicas      int    `json:"replicas"`
	ArchiveMode   string `json:"archive_mode"`
	DataDirectory string `json:"data_directory,omitempty"`
	Timeline      uint32 `json:"timeline"`
	// LSN and Time are the instance's WAL position and clock when it was
	// asked: the most that could have been recovered to.
	LSN       string `json:"lsn"`
	Time      string `json:"time"`
	HAManager string `json:"ha_manager"`
}

// haManagerDetect prints ha_manager=running when an HA manager is running on
// the host, else ha_manager=none. By exact process name, so a process that
// merely mentions one (an editor, a grep) does not refuse a restore; Patroni
// usually runs under Python, so it also matches a command line that ends in
// /patroni. (A first version used `pgrep -f` with the names as patterns: it
// matched the shell running the script, whose command line contains them.)
const haManagerDetect = `if pgrep -x 'patroni|repmgrd|pg_autoctl|stolon-keeper|stolon-sentinel' >/dev/null 2>&1 || pgrep -f '[/]patroni( |$)' >/dev/null 2>&1; then echo "ha_manager=running"; else echo "ha_manager=none"; fi`

// livePreflightScript asks the running instance. Local-socket psql as the
// postgres OS user, like every maintenance script.
const livePreflightScript = `Q() { psql -X -At -d postgres -c "$1"; }
echo "in_recovery=$(Q 'SELECT pg_is_in_recovery()')"
echo "replicas=$(Q 'SELECT count(*) FROM pg_stat_replication')"
echo "archive_mode=$(Q 'SHOW archive_mode')"
echo "data_directory=$(Q 'SHOW data_directory')"
echo "timeline=$(Q 'SELECT timeline_id FROM pg_control_checkpoint()')"
echo "lsn=$(Q 'SELECT pg_current_wal_lsn()')"
echo "time=$(Q "SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')")"
` + haManagerDetect + `
`

// parseLivePreState reads livePreflightScript's key=value lines.
func parseLivePreState(out string) (LivePreState, error) {
	kv := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			kv[k] = strings.TrimSpace(v)
		}
	}
	var p LivePreState
	for _, k := range []string{"in_recovery", "replicas", "archive_mode", "timeline", "lsn", "time"} {
		if kv[k] == "" {
			return p, fmt.Errorf("the instance did not report %s (got %q)", k, strings.TrimSpace(out))
		}
	}
	p.InRecovery = kv["in_recovery"] == "t" || kv["in_recovery"] == "true"
	n, err := strconv.Atoi(kv["replicas"])
	if err != nil {
		return p, fmt.Errorf("replicas = %q", kv["replicas"])
	}
	p.Replicas = n
	tl, err := strconv.ParseUint(kv["timeline"], 10, 32)
	if err != nil {
		return p, fmt.Errorf("timeline = %q", kv["timeline"])
	}
	p.Timeline = uint32(tl)
	p.ArchiveMode, p.DataDirectory, p.LSN, p.Time, p.HAManager = kv["archive_mode"], kv["data_directory"], kv["lsn"], kv["time"], kv["ha_manager"]
	return p, nil
}

// refusal returns why a serving instance must not be restored to target, or
// "". Each is something only the running instance can say.
func (p LivePreState) refusal(target RecoveryTarget, normalized string) string {
	switch {
	case p.InRecovery:
		return "the instance is a standby (pg_is_in_recovery): a point-in-time restore would fork its timeline away from its primary"
	case p.Replicas > 0:
		return fmt.Sprintf("%d replica(s) are attached (pg_stat_replication): a point-in-time restore forks the timeline, and every replica would have to be rebuilt", p.Replicas)
	case p.ArchiveMode != "on" && p.ArchiveMode != "always":
		return fmt.Sprintf("archive_mode is %q: the instance is not archiving WAL, so its recent history cannot be flushed to the archive the restore replays", p.ArchiveMode)
	case p.HAManager != "" && p.HAManager != "none":
		return "an HA manager (Patroni, repmgr, pg_auto_failover or Stolon) is running on the instance: it would fight the stop, and the restore forks the timeline under it"
	}
	switch target.Type {
	case TargetLSN:
		tl, err1 := parseLSN(normalized)
		cur, err2 := parseLSN(p.LSN)
		if err1 == nil && err2 == nil && tl > cur {
			return fmt.Sprintf("the target LSN %s is ahead of the instance's current WAL position %s: that moment has not happened yet", normalized, p.LSN)
		}
	case TargetTime:
		tt, err1 := time.Parse(time.RFC3339, strings.TrimSpace(target.Value))
		now, err2 := time.Parse(time.RFC3339, p.Time)
		if err1 == nil && err2 == nil && tt.After(now) {
			return fmt.Sprintf("the target time %s is ahead of the database's own clock (%s)", target.Value, p.Time)
		}
	}
	return ""
}

func livePreflight(ctx context.Context, inst pgInstance) (LivePreState, error) {
	out, err := inst.maintenance(ctx, livePreflightScript, false)
	if err != nil {
		return LivePreState{}, fmt.Errorf("asking the running instance about itself: %w: %s", err, strings.TrimSpace(out))
	}
	return parseLivePreState(out)
}

// archiveFlushTimeout is how long flushArchive waits for the archiver; a
// variable so tests don't wait it out.
var archiveFlushTimeout = 90 * time.Second

// flushArchiveScript switches WAL and waits until no completed segment is
// waiting to be archived and the archiver has not failed meanwhile. The
// segment the switch completes gets its .ready file at once, so an empty
// archive_status is the proof that it has been handed to the archive. Exit
// status 6 is a flush that cannot be confirmed.
func flushArchiveScript(timeout time.Duration) string {
	return fmt.Sprintf(`Q() { psql -X -At -d postgres -c "$1"; }
PGDATA=$(Q 'SHOW data_directory')
FAILED0=$(Q 'SELECT failed_count FROM pg_stat_archiver')
Q 'SELECT pg_switch_wal()' >/dev/null
i=0
while [ $i -lt %d ]; do
  READY=$(ls "$PGDATA/pg_wal/archive_status" 2>/dev/null | grep -c '\.ready$' || true)
  FAILED=$(Q 'SELECT failed_count FROM pg_stat_archiver')
  if [ "$FAILED" != "$FAILED0" ]; then echo "flush=failed archiver_failed_count=$FAILED0->$FAILED"; exit 6; fi
  if [ "$READY" = "0" ]; then echo "flush=done"; exit 0; fi
  i=$((i+1)); sleep 1
done
echo "flush=timeout segments_still_waiting=$READY"; exit 6`, int(timeout/time.Second))
}

// flushArchive makes the instance hand its completed WAL to the archive and
// confirms it did.
func flushArchive(ctx context.Context, inst pgInstance) error {
	out, err := inst.maintenance(ctx, flushArchiveScript(archiveFlushTimeout), false)
	if err != nil || !strings.Contains(out, "flush=done") {
		return fmt.Errorf("could not confirm the archive was flushed (%s): %v", strings.TrimSpace(out), err)
	}
	return nil
}

// planAfterFlushTimeout bounds awaitFlushedPlan: pgBackRest's async archive
// acknowledges a segment before it is in the repository.
var planAfterFlushTimeout = 90 * time.Second

// planPollInterval is a variable so tests don't wait.
var planPollInterval = 3 * time.Second

// awaitFlushedPlan recomputes the plan from the repo until the flushed WAL is
// in it: feasible, and no longer needing a flush. A plan that is not
// feasible for another reason ends the wait at once.
func awaitFlushedPlan(ctx context.Context, args PlanPITRArgs) (PITRPlan, error) {
	deadline := time.Now().Add(planAfterFlushTimeout)
	for {
		plan, err := planPITRImpl(ctx, args)
		if err != nil {
			return plan, err
		}
		if plan.Status == PITRFeasible && !plan.ArchiveFlushRequired {
			return plan, nil
		}
		if plan.Status != PITRFeasible {
			return plan, nil
		}
		if time.Now().After(deadline) {
			return plan, nil
		}
		select {
		case <-ctx.Done():
			return plan, ctx.Err()
		case <-time.After(planPollInterval):
		}
	}
}
