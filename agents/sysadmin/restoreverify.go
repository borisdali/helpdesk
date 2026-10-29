package main

// Post-restore WAL verification (v0.32 item 9).
//
// restore_from_backup decides whether to restore from the WAL-continuity
// check's prediction: recovery from the latest backup reaches archive_end_wal
// and PostgreSQL promotes to latest_timeline+1. Until this existed, the
// remediation playbook reported that prediction as the outcome. This file
// reads what PostgreSQL actually did after the restart — the last segment it
// restored from the archive and the timeline it selected — and compares.
//
// The prediction is recorded by restore_from_backup itself (in its result,
// which is audited, and in restorePredictions below), never passed in by the
// model: an expected value the model copies between tool calls is one it can
// get wrong or make up. Losing it (an agent restart between the two calls)
// gives "unverified", never a pass.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/adk/agent"

	"helpdesk/internal/audit"
	"helpdesk/internal/evidence"
)

// RestorePrediction is where restore_from_backup expected recovery to end,
// taken from the WAL-continuity check it ran immediately before restoring.
type RestorePrediction struct {
	BackupLabel string `json:"backup_label"`
	// RecoverToWAL is the last segment recovery should restore from the
	// archive: the check's archive_end_wal.
	RecoverToWAL string `json:"recover_to_wal"`
	// ArchiveEndTime is when that segment was archived; changes after it were
	// never archived and are not restored.
	ArchiveEndTime string `json:"archive_end_time,omitempty"`
	// ExpectedTimeline is the timeline PostgreSQL should promote to:
	// latest_timeline+1.
	ExpectedTimeline uint32 `json:"expected_timeline"`
	// ReplayThroughWAL is set when restore_from_backup put WAL newer than the
	// archive back into pg_wal (waltail.go): recovery should go on past
	// RecoverToWAL and replay through this segment. PostgreSQL logs nothing
	// for segments it reads from pg_wal, so this is checked against the
	// segment "redo done at" falls in.
	ReplayThroughWAL string `json:"replay_through_wal,omitempty"`
	// SegmentSizeBytes maps the redo-done LSN to a segment.
	SegmentSizeBytes uint64 `json:"segment_size_bytes,omitempty"`
	PredictedAt      string `json:"predicted_at"`
	// TargetType/TargetValue are set for a point-in-time restore
	// (restore_to_point_in_time): the recovery target, normalized, that
	// recovery must stop at. Empty for restore-to-latest. When set,
	// RecoverToWAL and ReplayThroughWAL are unused: recovery ends at the
	// target, not at the end of the archive (pitrverify.go).
	TargetType  string `json:"target_type,omitempty"`
	TargetValue string `json:"target_value,omitempty"`
}

// restorePredictionFrom returns the prediction a restore of wal's backup
// makes, given what was put back into pg_wal (tail, nil if nothing), or nil
// when the check gives no end point to compare against.
func restorePredictionFrom(wal *WALContinuity, tail *PgWalTail, now time.Time) *RestorePrediction {
	if wal == nil || wal.Status != WALContinuous || wal.ArchiveEndWAL == "" || wal.LatestTimeline == 0 {
		return nil
	}
	p := &RestorePrediction{
		BackupLabel:      wal.BackupLabel,
		RecoverToWAL:     wal.ArchiveEndWAL,
		ArchiveEndTime:   wal.ArchiveEndTime,
		ExpectedTimeline: wal.LatestTimeline + 1,
		SegmentSizeBytes: wal.SegmentSizeBytes,
		PredictedAt:      now.UTC().Format(time.RFC3339),
	}
	if tail != nil {
		p.ReplayThroughWAL = tail.ReplayThroughWAL
		// Pushed before startup, so recovery restores them from the archive
		// (it tries the archive first): the archive now ends at the last one.
		if n := len(tail.PushedToArchive); n > 0 {
			p.RecoverToWAL = tail.PushedToArchive[n-1]
		}
	}
	return p
}

// restorePredictions holds the last successful restore's prediction per
// target, for verify_restore_recovery.
var restorePredictions = struct {
	sync.Mutex
	byTarget map[string]RestorePrediction
}{byTarget: map[string]RestorePrediction{}}

func saveRestorePrediction(target string, p RestorePrediction) {
	restorePredictions.Lock()
	defer restorePredictions.Unlock()
	restorePredictions.byTarget[target] = p
}

func loadRestorePrediction(target string) (RestorePrediction, bool) {
	restorePredictions.Lock()
	defer restorePredictions.Unlock()
	p, ok := restorePredictions.byTarget[target]
	return p, ok
}

// recoveryLog is what PostgreSQL's log says about its newest completed
// archive recovery.
type recoveryLog struct {
	Found bool
	// LastRestoredWAL is the last WAL segment (never a .history file) restored
	// from the archive before the timeline switch.
	LastRestoredWAL string
	NewTimeline     uint32
	RedoDoneLSN     string
	ConsistentLSN   string
}

var (
	recoveryRestoredSegmentRe = regexp.MustCompile(`restored log file "([0-9A-F]{24})" from archive`)
	recoveryNewTimelineRe     = regexp.MustCompile(`selected new timeline ID: (\d+)`)
	recoveryRedoDoneRe        = regexp.MustCompile(`redo done at ([0-9A-F]+/[0-9A-F]+)`)
	recoveryConsistentRe      = regexp.MustCompile(`consistent recovery state reached at ([0-9A-F]+/[0-9A-F]+)`)
)

// parseRecoveryLog finds the newest "selected new timeline ID" line in log and
// reads that recovery's lines: the ones after the previous timeline switch (or
// from the top) up to it. Only lines before the switch count — PostgreSQL
// restores .history files after it, and re-reads the last segment just before
// it; neither changes where replay ended. Found live on the VM 2026-10-18:
// the last "restored log file" line overall was 0000001D.history, after
// "selected new timeline ID: 30".
func parseRecoveryLog(log string) recoveryLog {
	lines := strings.Split(log, "\n")
	end := -1
	var tli uint32
	for i := len(lines) - 1; i >= 0; i-- {
		if m := recoveryNewTimelineRe.FindStringSubmatch(lines[i]); m != nil {
			if v, err := strconv.ParseUint(m[1], 10, 32); err == nil {
				end, tli = i, uint32(v)
				break
			}
		}
	}
	if end < 0 {
		return recoveryLog{}
	}
	start := 0
	for i := end - 1; i >= 0; i-- {
		if recoveryNewTimelineRe.MatchString(lines[i]) {
			start = i + 1
			break
		}
	}
	rec := recoveryLog{Found: true, NewTimeline: tli}
	for _, line := range lines[start:end] {
		if m := recoveryRestoredSegmentRe.FindStringSubmatch(line); m != nil {
			rec.LastRestoredWAL = m[1]
		}
		if m := recoveryRedoDoneRe.FindStringSubmatch(line); m != nil {
			rec.RedoDoneLSN = m[1]
		}
		if m := recoveryConsistentRe.FindStringSubmatch(line); m != nil {
			rec.ConsistentLSN = m[1]
		}
	}
	return rec
}

// Restore verification statuses.
const (
	RestoreVerified   = "verified"
	RestoreMismatch   = "mismatch"
	RestoreNotFound   = "not_found"
	RestoreUnverified = "unverified"
	// RestoreTargetNotReached: a point-in-time recovery ran out of WAL before
	// the target. PostgreSQL shut down; PGDATA is intact and the restore can
	// be repeated with an earlier target.
	RestoreTargetNotReached = "target_not_reached"
)

// VerifyRestoreRecoveryArgs defines arguments for verify_restore_recovery.
type VerifyRestoreRecoveryArgs struct {
	Target string `json:"target" jsonschema:"required,Server ID from infrastructure config — the same target restore_from_backup was called with."`
}

// VerifyRestoreRecoveryResult compares restore_from_backup's prediction with
// what PostgreSQL's recovery actually reached.
type VerifyRestoreRecoveryResult struct {
	Target string `json:"target"`
	// Status is verified, mismatch, not_found or unverified.
	Status string `json:"status"`
	Detail string `json:"detail"`
	// Expected is restore_from_backup's prediction; nil when none was
	// recorded for this target.
	Expected *RestorePrediction `json:"expected,omitempty"`
	// ReachedWAL/NewTimeline/RedoDoneLSN/ConsistentLSN are read from
	// PostgreSQL's own recovery log lines.
	ReachedWAL    string `json:"reached_wal,omitempty"`
	NewTimeline   uint32 `json:"new_timeline,omitempty"`
	RedoDoneLSN   string `json:"redo_done_lsn,omitempty"`
	ConsistentLSN string `json:"consistent_lsn,omitempty"`
	// ReplayedThroughWAL is set when verified with a pg_wal tail: the newest
	// segment replayed from pg_wal, past ReachedWAL (the archive's last).
	ReplayedThroughWAL string `json:"replayed_through_wal,omitempty"`
	// LogSource says which log the recovery lines came from.
	LogSource string `json:"log_source,omitempty"`
	// StopLine is PostgreSQL's "recovery stopping ..." line, for a
	// point-in-time restore.
	StopLine string `json:"stop_line,omitempty"`
}

// compareRecovery is the pure core of verify_restore_recovery.
func compareRecovery(pred *RestorePrediction, rec recoveryLog) (status, detail string) {
	if !rec.Found {
		return RestoreNotFound, "no completed recovery (\"selected new timeline ID\") in the PostgreSQL log — the instance may not have been restarted yet, or recovery is still running or failed before promotion"
	}
	if pred == nil {
		return RestoreUnverified, fmt.Sprintf("recovery reached %s and selected timeline %d, but no restore_from_backup prediction is recorded for this target (the agent may have restarted since the restore), so it can't be compared", orNone(rec.LastRestoredWAL), rec.NewTimeline)
	}
	switch {
	case rec.NewTimeline < pred.ExpectedTimeline:
		return RestoreNotFound, fmt.Sprintf("the newest recovery in the log selected timeline %d, older than the %d this restore should create — the log predates the restart, or recovery hasn't finished", rec.NewTimeline, pred.ExpectedTimeline)
	case rec.NewTimeline > pred.ExpectedTimeline:
		return RestoreMismatch, fmt.Sprintf("recovery selected timeline %d, expected %d: another recovery or promotion happened after this restore", rec.NewTimeline, pred.ExpectedTimeline)
	case rec.LastRestoredWAL == "":
		return RestoreUnverified, fmt.Sprintf("recovery promoted to timeline %d, but the log shows no WAL segment restored from the archive, so the point it reached can't be compared with %s", rec.NewTimeline, pred.RecoverToWAL)
	case rec.LastRestoredWAL < pred.RecoverToWAL:
		return RestoreMismatch, fmt.Sprintf("recovery stopped early: the last segment restored was %s, expected %s — WAL after %s was not replayed", rec.LastRestoredWAL, pred.RecoverToWAL, rec.LastRestoredWAL)
	case rec.LastRestoredWAL > pred.RecoverToWAL:
		return RestoreMismatch, fmt.Sprintf("recovery went past the predicted end: the last segment restored was %s, expected %s", rec.LastRestoredWAL, pred.RecoverToWAL)
	}
	// Where redo actually ended, by segment number: the only evidence of
	// replay from pg_wal, which PostgreSQL doesn't log per segment.
	want := pred.RecoverToWAL
	if pred.ReplayThroughWAL != "" {
		want = pred.ReplayThroughWAL
	}
	if redoNo, wantNo, ok := redoSegmentNumbers(rec.RedoDoneLSN, want, pred.SegmentSizeBytes); ok {
		switch {
		case redoNo < wantNo && pred.ReplayThroughWAL != "":
			return RestoreMismatch, fmt.Sprintf("recovery stopped early in the pg_wal tail: redo done at %s, but the WAL put back into pg_wal reaches %s — "+
				"commits in the unreplayed part of the tail are not in the database (the saved copy is still on the host)", rec.RedoDoneLSN, want)
		case redoNo < wantNo:
			return RestoreMismatch, fmt.Sprintf("recovery stopped early: redo done at %s, before %s", rec.RedoDoneLSN, want)
		case redoNo > wantNo:
			return RestoreMismatch, fmt.Sprintf("recovery went past the predicted end: redo done at %s, after %s", rec.RedoDoneLSN, want)
		}
	}
	if pred.ReplayThroughWAL != "" && rec.LastRestoredWAL == pred.ReplayThroughWAL {
		return RestoreVerified, fmt.Sprintf("recovery replayed the archive through %s, including the WAL from pg_wal that restore_from_backup pushed there before startup (redo done at %s), and selected timeline %d, as predicted",
			rec.LastRestoredWAL, orNone(rec.RedoDoneLSN), rec.NewTimeline)
	}
	if pred.ReplayThroughWAL != "" {
		return RestoreVerified, fmt.Sprintf("recovery replayed the archive through %s, then the WAL put back into pg_wal through %s (redo done at %s), and selected timeline %d, as predicted",
			rec.LastRestoredWAL, pred.ReplayThroughWAL, orNone(rec.RedoDoneLSN), rec.NewTimeline)
	}
	return RestoreVerified, fmt.Sprintf("recovery replayed through %s (redo done at %s) and selected timeline %d, as predicted", rec.LastRestoredWAL, orNone(rec.RedoDoneLSN), rec.NewTimeline)
}

// redoSegmentNumbers returns the segment numbers of the redo-done LSN and of
// segment name, or ok=false when either can't be worked out (no LSN in the
// log, or a prediction from before the segment size was recorded). Numbers,
// not names: the archive end can be on an older timeline than the one the
// server ran on (the VM, 2026-10-18: archive end on timeline 27, server on 29).
func redoSegmentNumbers(redoLSN, name string, size uint64) (redoNo, nameNo uint64, ok bool) {
	if redoLSN == "" || !validSegmentSize(size) {
		return 0, 0, false
	}
	lsn, err := parseLSN(redoLSN)
	if err != nil {
		return 0, 0, false
	}
	_, logID, seg, err := parseSegmentName(name)
	if err != nil {
		return 0, 0, false
	}
	return lsn / size, logID*segsPerLogID(size) + seg, true
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// recoveryLogLines is how many log lines each source is asked for: a
// recovery writes a few lines per restored segment, and the log keeps going
// after it.
const recoveryLogLines = 2000

func verifyRestoreRecoveryImpl(ctx context.Context, args VerifyRestoreRecoveryArgs) (VerifyRestoreRecoveryResult, error) {
	if _, err := resolveHost(args.Target); err != nil {
		return VerifyRestoreRecoveryResult{}, err
	}
	res := VerifyRestoreRecoveryResult{Target: args.Target}
	if p, ok := loadRestorePrediction(args.Target); ok {
		res.Expected = &p
	}

	// Where PostgreSQL's recovery lines are depends on the host: container
	// stdout/stderr (docker logs), or the server log file when the journal has
	// only systemd's own lines (the VM, 2026-10-18: Debian's pg_ctl -l writes
	// them to /var/log/postgresql/) or logging_collector is on.
	var rec recoveryLog
	var tried []string
	var texts []string // every log read, for the point-in-time checks
	var found string   // the one the recovery was found in
	if logs, err := getHostLogsImpl(ctx, GetHostLogsArgs{Target: args.Target, Lines: recoveryLogLines}); err == nil {
		tried = append(tried, logs.Runtime+" logs")
		texts = append(texts, logs.Logs)
		if rec = parseRecoveryLog(logs.Logs); rec.Found {
			res.LogSource = logs.Runtime + " logs"
			found = logs.Logs
		}
	} else {
		tried = append(tried, "host logs ("+err.Error()+")")
	}
	if !rec.Found {
		if logs, err := readPgLogFileImpl(ctx, ReadPgLogFileArgs{Target: args.Target, Lines: recoveryLogLines}); err == nil {
			tried = append(tried, "PostgreSQL log file")
			texts = append(texts, logs.Logs)
			if rec = parseRecoveryLog(logs.Logs); rec.Found {
				res.LogSource = "PostgreSQL log file"
				found = logs.Logs
			}
		} else {
			tried = append(tried, "PostgreSQL log file ("+err.Error()+")")
		}
	}

	res.ReachedWAL = rec.LastRestoredWAL
	res.NewTimeline = rec.NewTimeline
	res.RedoDoneLSN = rec.RedoDoneLSN
	res.ConsistentLSN = rec.ConsistentLSN
	if res.Expected != nil && res.Expected.TargetType != "" {
		pl := parsePITRLog(found)
		if !rec.Found {
			for _, t := range texts {
				if pl = parsePITRLog(t); pl.NotReached {
					break
				}
			}
		}
		res.StopLine = pl.StopLine
		res.Status, res.Detail = comparePITRRecovery(res.Expected, rec, pl)
	} else {
		res.Status, res.Detail = compareRecovery(res.Expected, rec)
	}
	if res.Status == RestoreVerified && res.Expected != nil {
		res.ReplayedThroughWAL = res.Expected.ReplayThroughWAL
	}
	if !rec.Found {
		res.Detail += " (read: " + strings.Join(tried, ", ") + ")"
	}
	return res, nil
}

// RestoreVerifySummary is the single item verify_restore_recovery's
// objective-evidence probe thresholds.
type RestoreVerifySummary struct {
	Target string
	Status string
}

// restoreVerifyEvidenceSchema declares recovery_mismatch: true when the
// recovery PostgreSQL ran after a restore ended somewhere other than
// restore_from_backup predicted. not_found and unverified don't fire it — they
// say the comparison couldn't be made, not that it failed.
var restoreVerifyEvidenceSchema = evidence.NewToolSchema[RestoreVerifySummary]("verify_restore_recovery", func(s RestoreVerifySummary) string {
	return s.Target
}).
	Bool("recovery_mismatch", func(s RestoreVerifySummary) bool {
		return s.Status == RestoreMismatch
	}).
	Bool("recovery_target_not_reached", func(s RestoreVerifySummary) bool {
		return s.Status == RestoreTargetNotReached
	}).
	Register()

// restoreVerifyEvidenceRules holds the loaded rules for
// restoreVerifyEvidenceSchema, set by main() at startup.
var restoreVerifyEvidenceRules []evidence.Rule

func verifyRestoreRecoveryTool(ctx agent.ToolContext, args VerifyRestoreRecoveryArgs) (VerifyRestoreRecoveryResult, error) {
	start := time.Now()
	result, err := verifyRestoreRecoveryImpl(ctx, args)
	duration := time.Since(start)
	if err == nil {
		slog.Info("tool ok", "name", "verify_restore_recovery", "ms", duration.Milliseconds(), "status", result.Status)
	}
	if toolAuditor != nil {
		var errMsg string
		if err != nil {
			errMsg = err.Error()
		} else {
			evidence.Evaluate(ctx, toolAuditor, restoreVerifyEvidenceSchema,
				[]RestoreVerifySummary{{Target: args.Target, Status: result.Status}}, restoreVerifyEvidenceRules)
		}
		resultJSON, _ := json.Marshal(result)
		toolAuditor.RecordToolCall(ctx, audit.ToolCall{
			Name:       "verify_restore_recovery",
			Parameters: map[string]any{"target": args.Target},
		}, audit.ToolResult{
			Output: string(resultJSON),
			Error:  errMsg,
		}, duration)
	}
	return result, err
}
