package main

// Verification of a point-in-time recovery (v0.33).
//
// A point-in-time recovery ends where the target says, not at the end of the
// archive, so restoreverify.go's comparison (last restored segment against the
// archive end) does not apply: PostgreSQL reads ahead of the stop point, so
// the last "restored log file" line is a segment past it. What PostgreSQL
// does log, captured from a real run on 2026-10-27 (testdata/pitr/):
//
//	starting point-in-time recovery to 2026-10-27 19:37:21.437973+00
//	recovery stopping before commit of transaction 742, time 2026-10-27 19:37:23.578405+00
//	redo done at 0/17000028
//	selected new timeline ID: 12
//
//	starting point-in-time recovery to WAL location (LSN) "0/19000318"
//	recovery stopping after WAL location (LSN) "0/19000318"
//
// and, when the archive ends before the target:
//
//	FATAL:  recovery ended before configured recovery target was reached
//
// The time rule: "stopping before commit ... time T" names the first commit
// AFTER the target (T > target); "stopping after commit" names the last one
// at or before it (T <= target).

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// pitrLog is what PostgreSQL's log says about a point-in-time recovery's end.
type pitrLog struct {
	// Started is the newest "starting point-in-time recovery" line.
	Started bool
	// StopLine is the newest "recovery stopping ..." line after it.
	StopLine string
	// StopKind is "before" or "after".
	StopKind string
	// StopTime is the commit time of a time-target stop, as logged.
	StopTime string
	// StopLSN is the LSN of an LSN-target stop, as logged.
	StopLSN string
	// NotReached is true when recovery ended with the target-not-reached
	// FATAL after the newest start line.
	NotReached bool
}

var (
	pitrStartedRe    = regexp.MustCompile(`starting point-in-time recovery to `)
	pitrStopCommitRe = regexp.MustCompile(`recovery stopping (before|after) commit of transaction \d+, time (\S+ \S+)`)
	pitrStopLSNRe    = regexp.MustCompile(`recovery stopping (before|after) WAL location \(LSN\) "([0-9A-F]+/[0-9A-F]+)"`)
	pitrNotReachedRe = regexp.MustCompile(`FATAL:\s+recovery ended before configured recovery target was reached`)
)

// parsePITRLog reads the newest point-in-time recovery in log.
func parsePITRLog(log string) pitrLog {
	lines := strings.Split(log, "\n")
	start := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if pitrStartedRe.MatchString(lines[i]) {
			start = i
			break
		}
	}
	if start < 0 {
		return pitrLog{}
	}
	pl := pitrLog{Started: true}
	for _, line := range lines[start:] {
		if m := pitrStopCommitRe.FindStringSubmatch(line); m != nil {
			pl.StopKind, pl.StopTime, pl.StopLine = m[1], m[2], strings.TrimSpace(line)
		}
		if m := pitrStopLSNRe.FindStringSubmatch(line); m != nil {
			pl.StopKind, pl.StopLSN, pl.StopLine = m[1], m[2], strings.TrimSpace(line)
		}
		if pitrNotReachedRe.MatchString(line) {
			pl.NotReached = true
		}
	}
	return pl
}

// pitrLogTime parses a timestamp as PostgreSQL logs it ("+00" offsets).
func pitrLogTime(s string) (time.Time, error) {
	return time.Parse(pitrTimeFormat, s)
}

// comparePITRRecovery is the pure core of verify_restore_recovery for a
// point-in-time restore.
func comparePITRRecovery(pred *RestorePrediction, rec recoveryLog, pl pitrLog) (status, detail string) {
	if pl.NotReached {
		return RestoreTargetNotReached, fmt.Sprintf("recovery ran out of WAL before the %s target %s and PostgreSQL shut down: the archive does not reach it. "+
			"PGDATA is intact; repeat the restore with an earlier target", pred.TargetType, pred.TargetValue)
	}
	if !rec.Found {
		return RestoreNotFound, "no completed recovery (\"selected new timeline ID\") in the PostgreSQL log — the instance may not have been restarted yet, or recovery is still running"
	}
	if rec.NewTimeline < pred.ExpectedTimeline {
		return RestoreNotFound, fmt.Sprintf("the newest recovery in the log selected timeline %d, older than the %d this restore should create — the log predates the restart, or recovery hasn't finished", rec.NewTimeline, pred.ExpectedTimeline)
	}
	if rec.NewTimeline > pred.ExpectedTimeline {
		return RestoreMismatch, fmt.Sprintf("recovery selected timeline %d, expected %d: another recovery or promotion happened after this restore", rec.NewTimeline, pred.ExpectedTimeline)
	}
	if !pl.Started || pl.StopLine == "" {
		return RestoreMismatch, fmt.Sprintf("recovery promoted to timeline %d without a \"recovery stopping\" line: it was not the %s target %s that ended it", rec.NewTimeline, pred.TargetType, pred.TargetValue)
	}
	switch pred.TargetType {
	case TargetTime:
		target, err := pitrLogTime(pred.TargetValue)
		stop, err2 := pitrLogTime(pl.StopTime)
		if err != nil || err2 != nil || pl.StopTime == "" {
			return RestoreUnverified, fmt.Sprintf("recovery stopped (%s) but the stop time could not be compared with the target %s", pl.StopLine, pred.TargetValue)
		}
		switch {
		case pl.StopKind == "before" && !stop.After(target):
			return RestoreMismatch, fmt.Sprintf("recovery stopped before a commit at %s, which is not after the target %s: it stopped earlier than asked", pl.StopTime, pred.TargetValue)
		case pl.StopKind == "after" && stop.After(target):
			return RestoreMismatch, fmt.Sprintf("recovery stopped after a commit at %s, which is later than the target %s: it went past it", pl.StopTime, pred.TargetValue)
		}
	case TargetLSN:
		target, err := parseLSN(pred.TargetValue)
		stop, err2 := parseLSN(pl.StopLSN)
		if err != nil || err2 != nil || pl.StopLSN == "" {
			return RestoreUnverified, fmt.Sprintf("recovery stopped (%s) but the stop LSN could not be compared with the target %s", pl.StopLine, pred.TargetValue)
		}
		if stop != target {
			return RestoreMismatch, fmt.Sprintf("recovery stopped at LSN %s, not the target %s", pl.StopLSN, pred.TargetValue)
		}
		if redo, err := parseLSN(rec.RedoDoneLSN); err == nil && redo < target {
			return RestoreMismatch, fmt.Sprintf("redo done at %s, before the target %s", rec.RedoDoneLSN, pred.TargetValue)
		}
	default:
		return RestoreUnverified, fmt.Sprintf("unknown recovery target type %q in the prediction", pred.TargetType)
	}
	return RestoreVerified, fmt.Sprintf("recovery stopped at the %s target %s (%s; redo done at %s) and selected timeline %d, as predicted",
		pred.TargetType, pred.TargetValue, pl.StopLine, orNone(rec.RedoDoneLSN), rec.NewTimeline)
}
