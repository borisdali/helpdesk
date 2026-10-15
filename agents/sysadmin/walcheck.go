package main

// WAL-continuity check for pgBackRest restores (v0.32).
//
// restore_from_backup runs `pgbackrest --delta restore`: it lays down the
// latest backup and lets PostgreSQL replay archived WAL from that backup's
// start to the end of the archive, following timeline history to the newest
// timeline (recovery_target_timeline=latest, PostgreSQL's default). That only
// works if every WAL segment on that path is actually in the repo. Backup age
// (BackupStale) says nothing about it: a fresh backup can sit on a broken
// chain, and a missing segment is only discovered after PGDATA has already
// been overwritten.
//
// The check reproduces PostgreSQL's own segment selection, using only what
// the repo itself holds:
//
//  1. The backup's WAL start/stop segment and LSNs, from `pgbackrest info`.
//  2. The newest timeline, from the archive's <TLI>.history files, and that
//     history file's contents: every ancestor timeline and the LSN where it
//     forked. The newest history file alone describes the whole path.
//  3. The segment files on the timelines along that path, from one filtered
//     `pgbackrest repo-ls`.
//
// For timeline T that forked at LSN X, PostgreSQL reads segments before
// segno(X) from T and from segno(X) onward from T's child (the child's copy
// of the fork segment), so each timeline on the path owns a closed segment
// range and the newest one owns everything from its fork segment onward.
// .partial segments are never fetched by restore_command and don't count.
//
// The WAL segment size isn't recorded in `pgbackrest info` or the backup
// manifest. It is derived exactly instead: the backup's start LSN and start
// segment name are both known, and only one power-of-two segment size maps
// the first to the second.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// WAL continuity statuses.
const (
	WALContinuous = "continuous" // every segment from the backup to the archive end is present
	WALBroken     = "broken"     // a segment is missing, or the backup isn't on the newest timeline's history
	WALUnverified = "unverified" // the check could not run; nothing is known either way
	WALNoBackup   = "no_backup"  // there is no backup to restore from, so there is nothing to check
)

// maxReportedWALGaps bounds the gap list so a badly damaged archive can't
// produce an unbounded result.
const maxReportedWALGaps = 20

// WALGap is one run of consecutive missing segments on one timeline.
type WALGap struct {
	Timeline     uint32 `json:"timeline"`
	FirstMissing string `json:"first_missing"`
	LastMissing  string `json:"last_missing"`
	Count        uint64 `json:"count"`
}

// WALContinuity reports whether the latest backup can be restored to the end
// of the archived WAL.
type WALContinuity struct {
	// Status is continuous, broken, unverified or no_backup. restore_from_backup
	// refuses unless it is continuous.
	Status string `json:"status"`
	// Detail explains a broken or unverified status in one sentence.
	Detail      string `json:"detail,omitempty"`
	BackupLabel string `json:"backup_label,omitempty"`
	ArchiveID   string `json:"archive_id,omitempty"`
	// BackupStartWAL/BackupStopWAL bound the WAL the backup itself needs to
	// reach a consistent state.
	BackupStartWAL string `json:"backup_start_wal,omitempty"`
	BackupStopWAL  string `json:"backup_stop_wal,omitempty"`
	// ConsistencyReachable is false when a segment between BackupStartWAL and
	// BackupStopWAL is missing: PostgreSQL could not even start from this
	// backup. No omitempty: false is a finding, not an absent value.
	ConsistencyReachable bool     `json:"consistency_reachable"`
	LatestTimeline       uint32   `json:"latest_timeline,omitempty"`
	TimelinePath         []uint32 `json:"timeline_path,omitempty"`
	// RecoverableToWAL is the last segment of the unbroken chain: where
	// recovery would actually stop. Equal to ArchiveEndWAL when continuous.
	RecoverableToWAL string `json:"recoverable_to_wal,omitempty"`
	// ArchiveEndWAL/ArchiveEndTime are the newest segment on the path and when
	// it reached the repo: the most recent point restore-to-latest can reach.
	ArchiveEndWAL    string   `json:"archive_end_wal,omitempty"`
	ArchiveEndTime   string   `json:"archive_end_time,omitempty"`
	Gaps             []WALGap `json:"gaps,omitempty"`
	SegmentSizeBytes uint64   `json:"segment_size_bytes,omitempty"`
}

// walCheckInput is what `pgbackrest info` says about the backup being checked.
type walCheckInput struct {
	Stanza      string
	BackupLabel string
	ArchiveID   string
	WALStart    string // segment name, e.g. 000000010000000000000003
	WALStop     string
	LSNStart    string // e.g. 0/3000028
	LSNStop     string
	// SizeHints are the LSN/segment pairs of every backup in the stanza,
	// used to pin the WAL segment size.
	SizeHints []lsnSegment
}

// historyEntry is one line of a <TLI>.history file: timeline TLI forked off
// at LSN SwitchLSN.
type historyEntry struct {
	TLI       uint32
	SwitchLSN uint64
}

type walFile struct {
	time int64
}

var (
	walSegmentNameRe = regexp.MustCompile(`^([0-9A-F]{24})(?:-[0-9a-f]{40})?(?:\.(?:gz|lz4|zst|bz2|xz))?$`)
	historyFileRe    = regexp.MustCompile(`^([0-9A-F]{8})\.history$`)
)

// parseLSN parses PostgreSQL's "X/Y" LSN notation.
func parseLSN(s string) (uint64, error) {
	hi, lo, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok {
		return 0, fmt.Errorf("invalid LSN %q", s)
	}
	h, err := strconv.ParseUint(hi, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN %q: %w", s, err)
	}
	l, err := strconv.ParseUint(lo, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN %q: %w", s, err)
	}
	return h<<32 | l, nil
}

// parseSegmentName splits a 24-hex-digit WAL segment name into its timeline
// and its (logical id, segment) pair.
func parseSegmentName(name string) (tli uint32, logID, seg uint64, err error) {
	if len(name) != 24 {
		return 0, 0, 0, fmt.Errorf("invalid WAL segment name %q", name)
	}
	t, err1 := strconv.ParseUint(name[0:8], 16, 32)
	l, err2 := strconv.ParseUint(name[8:16], 16, 32)
	s, err3 := strconv.ParseUint(name[16:24], 16, 32)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, fmt.Errorf("invalid WAL segment name %q", name)
	}
	return uint32(t), l, s, nil
}

func segsPerLogID(segSize uint64) uint64 { return 0x100000000 / segSize }

func segmentName(tli uint32, segno, segSize uint64) string {
	per := segsPerLogID(segSize)
	return fmt.Sprintf("%08X%08X%08X", tli, segno/per, segno%per)
}

// lsnSegment pairs an LSN with the name of the segment it falls in, as
// pgbackrest info reports for each backup's start and stop.
type lsnSegment struct {
	LSN  string
	Name string
}

const defaultWALSegmentSize = 16 << 20

// inferSegmentSize returns the WAL segment size (1 MiB to 1 GiB, powers of
// two, as initdb allows) consistent with every LSN/segment pair given.
//
// One pair whose LSN lies past the first 1 MiB of its logical WAL id pins the
// size exactly. Only when every pair lies inside that first 1 MiB is it
// ambiguous; then PostgreSQL's 16 MiB default is used. That fallback is safe:
// a wrong size can only make real segments look missing (too large discards
// segment numbers it can't hold; too small expects ones that never exist), so
// it errs toward "broken", never toward a false "continuous".
func inferSegmentSize(pairs []lsnSegment) (uint64, error) {
	candidates := map[uint64]bool{}
	for shift := 20; shift <= 30; shift++ {
		candidates[uint64(1)<<shift] = true
	}
	for _, p := range pairs {
		if p.LSN == "" || p.Name == "" {
			continue
		}
		lsn, err := parseLSN(p.LSN)
		if err != nil {
			return 0, err
		}
		_, logID, seg, err := parseSegmentName(p.Name)
		if err != nil {
			return 0, err
		}
		for size := range candidates {
			per := segsPerLogID(size)
			if seg >= per || lsn/size != logID*per+seg {
				delete(candidates, size)
			}
		}
	}
	switch {
	case len(candidates) == 0:
		return 0, fmt.Errorf("no WAL segment size is consistent with the backups' LSNs and segment names")
	case len(candidates) == 1:
		for size := range candidates {
			return size, nil
		}
	}
	if candidates[defaultWALSegmentSize] {
		return defaultWALSegmentSize, nil
	}
	return 0, fmt.Errorf("cannot tell the WAL segment size from the backups' LSNs (%d candidates)", len(candidates))
}

// parseHistory parses a <TLI>.history file. Comment and blank lines are
// skipped; each entry is "parentTLI<TAB>switchLSN<TAB>reason".
func parseHistory(text string) ([]historyEntry, error) {
	var entries []historyEntry
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("invalid history line %q", line)
		}
		tli, err := strconv.ParseUint(fields[0], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid history line %q: %w", line, err)
		}
		lsn, err := parseLSN(fields[1])
		if err != nil {
			return nil, fmt.Errorf("invalid history line %q: %w", line, err)
		}
		entries = append(entries, historyEntry{TLI: uint32(tli), SwitchLSN: lsn})
	}
	return entries, nil
}

// historyTimelines returns the timelines that have a .history file in a
// top-level `repo-ls --output=text` listing of the archive directory.
func historyTimelines(topListing string) []uint32 {
	var tlis []uint32
	for _, line := range strings.Split(topListing, "\n") {
		if m := historyFileRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			if v, err := strconv.ParseUint(m[1], 16, 32); err == nil {
				tlis = append(tlis, uint32(v))
			}
		}
	}
	return tlis
}

// latestTimeline is the timeline recovery_target_timeline=latest picks: the
// highest one with a history file, or the backup's own if none is newer.
func latestTimeline(backupTLI uint32, historyTLIs []uint32) uint32 {
	latest := backupTLI
	for _, t := range historyTLIs {
		if t > latest {
			latest = t
		}
	}
	return latest
}

// timelinePath returns the timelines replay walks through, from the backup's
// timeline to latest, and the fork LSN of each one except the last. history
// is latest's history file (empty when latest is the backup's timeline).
func timelinePath(backupTLI, latest uint32, history []historyEntry) ([]uint32, map[uint32]uint64, error) {
	switches := map[uint32]uint64{}
	if latest == backupTLI {
		return []uint32{backupTLI}, switches, nil
	}
	var path []uint32
	found := false
	for _, e := range history {
		if e.TLI == backupTLI {
			found = true
		}
		if found {
			path = append(path, e.TLI)
			switches[e.TLI] = e.SwitchLSN
		}
	}
	if !found {
		return nil, nil, fmt.Errorf("the backup's timeline %d is not in the history of the newest timeline %d, so PostgreSQL would refuse to recover along it", backupTLI, latest)
	}
	return append(path, latest), switches, nil
}

// parseWALListing reads a `repo-ls --recurse --output=json` listing and
// returns the complete (non-.partial) segments per timeline, keyed by segno.
func parseWALListing(listingJSON string, segSize uint64) (map[uint32]map[uint64]walFile, error) {
	var raw map[string]struct {
		Type string `json:"type"`
		Time int64  `json:"time"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonObjectPrefix(listingJSON))), &raw); err != nil {
		return nil, fmt.Errorf("parsing repo-ls output: %w", err)
	}
	per := segsPerLogID(segSize)
	files := map[uint32]map[uint64]walFile{}
	for path, entry := range raw {
		if entry.Type != "file" {
			continue
		}
		base := path[strings.LastIndexByte(path, '/')+1:]
		m := walSegmentNameRe.FindStringSubmatch(base)
		if m == nil {
			continue // .partial, .backup, .history
		}
		tli, logID, seg, err := parseSegmentName(m[1])
		if err != nil || seg >= per {
			continue
		}
		if files[tli] == nil {
			files[tli] = map[uint64]walFile{}
		}
		files[tli][logID*per+seg] = walFile{time: entry.Time}
	}
	return files, nil
}

// jsonObjectPrefix strips any WARN/INFO preamble before the first `{`, for
// the same reason jsonArrayPrefix exists.
func jsonObjectPrefix(output string) string {
	if idx := strings.IndexByte(output, '{'); idx >= 0 {
		return output[idx:]
	}
	return output
}

// evaluateWALContinuity is the pure core of the check. topListing is the
// archive directory's top-level `repo-ls --output=text`; historyText is the
// newest timeline's history file (empty when that is the backup's own
// timeline); listingJSON is the recursive `repo-ls --output=json` of the
// timelines on the path.
func evaluateWALContinuity(in walCheckInput, topListing, historyText, listingJSON string) WALContinuity {
	res := WALContinuity{
		BackupLabel:    in.BackupLabel,
		ArchiveID:      in.ArchiveID,
		BackupStartWAL: in.WALStart,
		BackupStopWAL:  in.WALStop,
	}
	unverified := func(format string, args ...any) WALContinuity {
		res.Status = WALUnverified
		res.Detail = fmt.Sprintf(format, args...)
		return res
	}

	plan, err := planWALCheck(in, topListing, historyText)
	if err != nil {
		if pe, ok := err.(*walPathError); ok {
			res.Status = WALBroken
			res.Detail = pe.Error()
			res.LatestTimeline = pe.latest
			return res
		}
		return unverified("%v", err)
	}
	res.SegmentSizeBytes = plan.segSize
	res.LatestTimeline = plan.path[len(plan.path)-1]
	res.TimelinePath = plan.path

	files, err := parseWALListing(listingJSON, plan.segSize)
	if err != nil {
		return unverified("%v", err)
	}

	// Walk the path in replay order. Segment numbers only grow along it.
	var gaps []WALGap
	addGap := func(tli uint32, from, to uint64) {
		if len(gaps) < maxReportedWALGaps {
			gaps = append(gaps, WALGap{
				Timeline:     tli,
				FirstMissing: segmentName(tli, from, plan.segSize),
				LastMissing:  segmentName(tli, to, plan.segSize),
				Count:        to - from + 1,
			})
		}
	}
	var (
		recoverableTLI  uint32
		recoverableSeg  uint64
		haveRecoverable bool
		broken          bool
		endTLI          uint32
		endSeg          uint64
		endTime         int64
		haveEnd         bool
	)
	markPresent := func(tli uint32, seg uint64) {
		if !broken {
			recoverableTLI, recoverableSeg, haveRecoverable = tli, seg, true
		}
		endTLI, endSeg, endTime, haveEnd = tli, seg, files[tli][seg].time, true
	}

	start := plan.startSeg
	for i, tli := range plan.path {
		last := i == len(plan.path)-1
		var end uint64
		if last {
			// The newest timeline runs to its highest archived segment.
			end = start
			hasAny := false
			for seg := range files[tli] {
				if seg >= start && (!hasAny || seg > end) {
					end, hasAny = seg, true
				}
			}
			if !hasAny {
				break // nothing archived after the last fork: recovery ends there
			}
		} else {
			forkSeg := plan.switches[tli] / plan.segSize
			if forkSeg <= start {
				continue // the fork is in the segment replay starts from: the child's copy is read instead
			}
			end = forkSeg - 1
		}
		gapStart, inGap := uint64(0), false
		for seg := start; seg <= end; seg++ {
			if _, ok := files[tli][seg]; ok {
				if inGap {
					addGap(tli, gapStart, seg-1)
					inGap = false
				}
				markPresent(tli, seg)
				continue
			}
			if !inGap {
				gapStart, inGap = seg, true
			}
			broken = true
		}
		if inGap {
			addGap(tli, gapStart, end)
		}
		if !last {
			start = plan.switches[tli] / plan.segSize
		}
	}

	stopSeg := plan.stopSeg
	res.ConsistencyReachable = haveRecoverable && recoverableSeg >= stopSeg
	if haveRecoverable {
		res.RecoverableToWAL = segmentName(recoverableTLI, recoverableSeg, plan.segSize)
	}
	if haveEnd {
		res.ArchiveEndWAL = segmentName(endTLI, endSeg, plan.segSize)
		if endTime > 0 {
			res.ArchiveEndTime = time.Unix(endTime, 0).UTC().Format(time.RFC3339)
		}
	}
	res.Gaps = gaps

	switch {
	case !res.ConsistencyReachable:
		res.Status = WALBroken
		if len(gaps) > 0 {
			res.Detail = fmt.Sprintf("WAL segment %s, needed for backup %s to reach a consistent state, is missing from the repo: PostgreSQL could not start from this backup", gaps[0].FirstMissing, in.BackupLabel)
		} else {
			res.Detail = fmt.Sprintf("the WAL backup %s needs (%s to %s) is not in the repo: PostgreSQL could not start from this backup", in.BackupLabel, in.WALStart, in.WALStop)
		}
	case broken:
		res.Status = WALBroken
		res.Detail = fmt.Sprintf("WAL is missing from %s on timeline %d: recovery would stop at %s instead of reaching the end of the archive (%s)",
			gaps[0].FirstMissing, gaps[0].Timeline, res.RecoverableToWAL, res.ArchiveEndWAL)
	default:
		res.Status = WALContinuous
	}
	return res
}

// walPathError marks a definite finding (the backup is on the wrong branch),
// as opposed to an inability to run the check.
type walPathError struct {
	msg    string
	latest uint32
}

func (e *walPathError) Error() string { return e.msg }

type walPlan struct {
	segSize  uint64
	path     []uint32
	switches map[uint32]uint64
	startSeg uint64
	stopSeg  uint64
}

// planWALCheck derives the segment size and the timeline path from the
// backup and the archive's history.
func planWALCheck(in walCheckInput, topListing, historyText string) (walPlan, error) {
	if in.WALStart == "" || in.WALStop == "" || in.LSNStart == "" || in.LSNStop == "" {
		return walPlan{}, fmt.Errorf("pgbackrest info reported no WAL range for backup %s", in.BackupLabel)
	}
	lsnStart, err := parseLSN(in.LSNStart)
	if err != nil {
		return walPlan{}, err
	}
	lsnStop, err := parseLSN(in.LSNStop)
	if err != nil {
		return walPlan{}, err
	}
	hints := append([]lsnSegment{{in.LSNStart, in.WALStart}, {in.LSNStop, in.WALStop}}, in.SizeHints...)
	segSize, err := inferSegmentSize(hints)
	if err != nil {
		return walPlan{}, err
	}
	backupTLI, _, _, err := parseSegmentName(in.WALStart)
	if err != nil {
		return walPlan{}, err
	}
	latest := latestTimeline(backupTLI, historyTimelines(topListing))
	var history []historyEntry
	if latest != backupTLI {
		if strings.TrimSpace(historyText) == "" {
			return walPlan{}, fmt.Errorf("history file for timeline %d is empty or unreadable", latest)
		}
		if history, err = parseHistory(historyText); err != nil {
			return walPlan{}, err
		}
	}
	path, switches, err := timelinePath(backupTLI, latest, history)
	if err != nil {
		return walPlan{}, &walPathError{msg: err.Error(), latest: latest}
	}
	if len(path) > 1 {
		if fork := switches[backupTLI]; lsnStop > fork {
			return walPlan{}, &walPathError{
				msg: fmt.Sprintf("backup %s ends at %s, after timeline %d forked at %X/%X: the backup is on a branch the newest timeline %d does not descend from",
					in.BackupLabel, in.LSNStop, backupTLI, fork>>32, uint32(fork), latest),
				latest: latest,
			}
		}
	}
	return walPlan{
		segSize:  segSize,
		path:     path,
		switches: switches,
		startSeg: lsnStart / segSize,
		stopSeg:  lsnStop / segSize,
	}, nil
}

// walListingFilter returns the repo-ls --filter regex that keeps only segment
// directories of the given timelines.
func walListingFilter(path []uint32) string {
	parts := make([]string, len(path))
	for i, t := range path {
		parts[i] = fmt.Sprintf("%08X", t)
	}
	sort.Strings(parts)
	return "^(" + strings.Join(parts, "|") + ")[0-9A-F]{8}/"
}

// pgbackrestRunner runs one pgbackrest invocation against the check's target.
type pgbackrestRunner func(args []string) (string, error)

// checkWALContinuity runs the check for the latest backup in result, using
// run for the three repo reads it needs.
func checkWALContinuity(result GetPgBackRestStatusResult, run pgbackrestRunner) WALContinuity {
	in, ok := result.walCheckInput()
	if !ok {
		return WALContinuity{Status: WALNoBackup, Detail: "no backup has completed, so there is nothing to restore from"}
	}
	base := "archive/" + in.Stanza + "/" + in.ArchiveID
	stanzaArg := "--stanza=" + in.Stanza

	top, err := run([]string{stanzaArg, "repo-ls", "--output=text", base})
	if err != nil {
		return WALContinuity{Status: WALUnverified, BackupLabel: in.BackupLabel, ArchiveID: in.ArchiveID,
			Detail: fmt.Sprintf("listing %s failed (repo-ls needs pgBackRest 2.26 or later): %v", base, err)}
	}
	// The newest timeline's history file names every ancestor and fork LSN.
	// It's only needed when that timeline isn't the backup's own.
	var historyText string
	if backupTLI, _, _, perr := parseSegmentName(in.WALStart); perr == nil {
		if latest := latestTimeline(backupTLI, historyTimelines(top)); latest != backupTLI {
			historyText, err = run([]string{stanzaArg, "repo-get", fmt.Sprintf("%s/%08X.history", base, latest)})
			if err != nil {
				return WALContinuity{Status: WALUnverified, BackupLabel: in.BackupLabel, ArchiveID: in.ArchiveID,
					Detail: fmt.Sprintf("reading the history file for timeline %d failed: %v", latest, err)}
			}
		}
	}
	plan, err := planWALCheck(in, top, historyText)
	if err != nil {
		// Let the pure evaluation classify it (broken vs unverified).
		return evaluateWALContinuity(in, top, historyText, "{}")
	}
	listing, err := run([]string{stanzaArg, "repo-ls", "--recurse", "--output=json", "--filter=" + walListingFilter(plan.path), base})
	if err != nil {
		return WALContinuity{Status: WALUnverified, BackupLabel: in.BackupLabel, ArchiveID: in.ArchiveID,
			Detail: fmt.Sprintf("listing WAL segments in %s failed: %v", base, err)}
	}
	return evaluateWALContinuity(in, top, historyText, listing)
}
