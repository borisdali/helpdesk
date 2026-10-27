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
// The WAL segment size (16 MiB by default, any power of two from 1 MiB to
// 1 GiB via initdb --wal-segsize) isn't recorded in `pgbackrest info`,
// archive.info, backup.info or the backup manifest. Two independent sources
// are used instead, and they must agree:
//
//   - The page header of one archived segment, which records the cluster's
//     segment size (xlp_seg_size). Its layout has been the same since
//     PostgreSQL 8.3. The check fetches the smallest gz, bz2 or uncompressed
//     segment on the path with `pgbackrest repo-get` (a fourth read) and
//     decompresses it here; lz4 and zst segments aren't read.
//   - The backups' start/stop LSNs and segment names: only the right size
//     maps each LSN to its segment name. This is ambiguous only when every
//     backup starts in the first 1 MiB of a logical WAL id.
//
// A header that contradicts the LSNs makes the check unverified. Without a
// readable header, the LSNs decide, and if they are ambiguous too,
// PostgreSQL's 16 MiB default is assumed; SegmentSizeSource says which.

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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

// Where SegmentSizeBytes came from.
const (
	SegmentSizeFromWALHeader  = "wal_header"  // a WAL segment's page header, confirmed by the backups' LSNs
	SegmentSizeFromBackupLSNs = "backup_lsns" // the backups' LSNs and segment names, which fit only this size
	SegmentSizeAssumedDefault = "default"     // nothing pinned it, so PostgreSQL's 16 MiB default is assumed
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
	// SegmentSizeSource is wal_header, backup_lsns or default.
	SegmentSizeSource string `json:"segment_size_source,omitempty"`
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
	// HeaderSegSize is the segment size read from an archived segment's page
	// header; 0 when none was read.
	HeaderSegSize uint64
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

func validSegmentSize(size uint64) bool {
	return size >= 1<<20 && size <= 1<<30 && size&(size-1) == 0
}

// segmentSizeCandidates returns the WAL segment sizes (1 MiB to 1 GiB, powers
// of two, as initdb allows) consistent with every LSN/segment pair given.
// One pair whose LSN lies past the first 1 MiB of its logical WAL id pins the
// size exactly; pairs inside that first 1 MiB fit every size.
func segmentSizeCandidates(pairs []lsnSegment) (map[uint64]bool, error) {
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
			return nil, err
		}
		_, logID, seg, err := parseSegmentName(p.Name)
		if err != nil {
			return nil, err
		}
		for size := range candidates {
			per := segsPerLogID(size)
			if seg >= per || lsn/size != logID*per+seg {
				delete(candidates, size)
			}
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no WAL segment size is consistent with the backups' LSNs and segment names")
	}
	return candidates, nil
}

// resolveSegmentSize picks the WAL segment size and says where it came from.
// header (0 when no segment header was read) must be one of the sizes the
// LSN pairs allow. Without it, a single remaining candidate is used, and when
// the pairs are ambiguous, PostgreSQL's 16 MiB default. That fallback is safe:
// a wrong size can only make real segments look missing (too large discards
// segment numbers it can't hold; too small expects ones that never exist), so
// it errs toward "broken", never toward a false "continuous".
func resolveSegmentSize(pairs []lsnSegment, header uint64) (uint64, string, error) {
	candidates, err := segmentSizeCandidates(pairs)
	if err != nil {
		return 0, "", err
	}
	if header != 0 {
		if !candidates[header] {
			return 0, "", fmt.Errorf("an archived WAL segment's header says the segment size is %d MiB, but the backups' LSNs and segment names don't fit that size", header>>20)
		}
		return header, SegmentSizeFromWALHeader, nil
	}
	if len(candidates) == 1 {
		for size := range candidates {
			return size, SegmentSizeFromBackupLSNs, nil
		}
	}
	if candidates[defaultWALSegmentSize] {
		return defaultWALSegmentSize, SegmentSizeAssumedDefault, nil
	}
	return 0, "", fmt.Errorf("cannot tell the WAL segment size from the backups' LSNs (%d candidates)", len(candidates))
}

// inferSegmentSize is resolveSegmentSize without a segment header.
func inferSegmentSize(pairs []lsnSegment) (uint64, error) {
	size, _, err := resolveSegmentSize(pairs, 0)
	return size, err
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

// pgbackrestLogLineRe matches a line of pgBackRest's own console log
// ("P00   WARN: …"), which it writes to stdout ahead of a command's output.
var pgbackrestLogLineRe = regexp.MustCompile(`^P\d{2}\s+[A-Z]+:`)

// withoutPgBackRestLog drops pgBackRest's console log lines, and the indented
// continuation lines that follow one (HINT:, DETAIL:), from a text output:
// the text reads (repo-ls --output=text, a .history file) have no JSON
// delimiter to skip to the way jsonObjectPrefix does. Found on Kubernetes
// (2026-10-20): every Service in the pod's namespace injects env vars pgBackRest
// warns about ("environment contains invalid option"), and a .history file
// read with them failed to parse, leaving the WAL check unverified on a
// broken chain. Real history and listing lines never start with whitespace or
// with "P<digits>".
func withoutPgBackRestLog(output string) string {
	var kept []string
	inLog := false
	for _, line := range strings.Split(output, "\n") {
		switch {
		case pgbackrestLogLineRe.MatchString(line):
			inLog = true
		case inLog && line != "" && (line[0] == ' ' || line[0] == '\t'):
		default:
			inLog = false
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
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
	res.SegmentSizeSource = plan.segSizeSource
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
				// Nothing archived after the last fork: recovery ends there.
				// Unless that's still inside the backup's own WAL: then it's
				// missing, and the gap must say so (found on K8s, 2026-10-20:
				// a fresh archive held only the backup's one segment, and
				// with it hidden the gap list came back empty).
				if start <= plan.stopSeg {
					addGap(tli, start, plan.stopSeg)
					broken = true
				}
				break
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
		missing := gaps[0].FirstMissing
		if gaps[0].Count > 1 {
			missing += " to " + gaps[0].LastMissing
		}
		res.Detail = fmt.Sprintf("WAL segment %s on timeline %d is missing: recovery would stop at %s", missing, gaps[0].Timeline, res.RecoverableToWAL)
		if res.ArchiveEndWAL != res.RecoverableToWAL {
			res.Detail += fmt.Sprintf(" instead of reaching the end of the archive (%s)", res.ArchiveEndWAL)
		} else {
			// Nothing later survives, but timeline history shows this
			// timeline ran past the gap before the next one forked.
			res.Detail += fmt.Sprintf(", although timeline %d's history shows WAL past it", gaps[0].Timeline)
		}
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
	segSize       uint64
	segSizeSource string
	path          []uint32
	switches      map[uint32]uint64
	startSeg      uint64
	stopSeg       uint64
}

// planWALPath derives the timeline path from the backup and the archive's
// history. It doesn't need the segment size, so the listing can be filtered
// to the path before the size is known.
func planWALPath(in walCheckInput, topListing, historyText string) ([]uint32, map[uint32]uint64, error) {
	if in.WALStart == "" || in.WALStop == "" || in.LSNStart == "" || in.LSNStop == "" {
		return nil, nil, fmt.Errorf("pgbackrest info reported no WAL range for backup %s", in.BackupLabel)
	}
	lsnStop, err := parseLSN(in.LSNStop)
	if err != nil {
		return nil, nil, err
	}
	backupTLI, _, _, err := parseSegmentName(in.WALStart)
	if err != nil {
		return nil, nil, err
	}
	latest := latestTimeline(backupTLI, historyTimelines(topListing))
	var history []historyEntry
	if latest != backupTLI {
		if strings.TrimSpace(historyText) == "" {
			return nil, nil, fmt.Errorf("history file for timeline %d is empty or unreadable", latest)
		}
		if history, err = parseHistory(historyText); err != nil {
			return nil, nil, err
		}
	}
	path, switches, err := timelinePath(backupTLI, latest, history)
	if err != nil {
		return nil, nil, &walPathError{msg: err.Error(), latest: latest}
	}
	if len(path) > 1 {
		if fork := switches[backupTLI]; lsnStop > fork {
			return nil, nil, &walPathError{
				msg: fmt.Sprintf("backup %s ends at %s, after timeline %d forked at %X/%X: the backup is on a branch the newest timeline %d does not descend from",
					in.BackupLabel, in.LSNStop, backupTLI, fork>>32, uint32(fork), latest),
				latest: latest,
			}
		}
	}
	return path, switches, nil
}

// planWALCheck adds the segment size to planWALPath's timeline path.
func planWALCheck(in walCheckInput, topListing, historyText string) (walPlan, error) {
	path, switches, err := planWALPath(in, topListing, historyText)
	if err != nil {
		return walPlan{}, err
	}
	lsnStart, err := parseLSN(in.LSNStart)
	if err != nil {
		return walPlan{}, err
	}
	lsnStop, _ := parseLSN(in.LSNStop) // validated by planWALPath
	hints := append([]lsnSegment{{in.LSNStart, in.WALStart}, {in.LSNStop, in.WALStop}}, in.SizeHints...)
	segSize, source, err := resolveSegmentSize(hints, in.HeaderSegSize)
	if err != nil {
		return walPlan{}, err
	}
	return walPlan{
		segSize:       segSize,
		segSizeSource: source,
		path:          path,
		switches:      switches,
		startSeg:      lsnStart / segSize,
		stopSeg:       lsnStop / segSize,
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
// run for the repo reads it needs: the archive's top level, the newest
// timeline's history file, the segment listing, and one segment's header.
func checkWALContinuity(result GetPgBackRestStatusResult, run pgbackrestRunner) WALContinuity {
	return checkWALContinuityFor(result, "", run)
}

// checkWALContinuityFor is checkWALContinuity for the backup with the given
// label, or the latest when label is empty.
func checkWALContinuityFor(result GetPgBackRestStatusResult, label string, run pgbackrestRunner) WALContinuity {
	in, ok := result.walCheckInputFor(label)
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
	top = withoutPgBackRestLog(top)
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
			historyText = withoutPgBackRestLog(historyText)
		}
	}
	path, _, err := planWALPath(in, top, historyText)
	if err != nil {
		// Let the pure evaluation classify it (broken vs unverified).
		return evaluateWALContinuity(in, top, historyText, "{}")
	}
	listing, err := run([]string{stanzaArg, "repo-ls", "--recurse", "--output=json", "--filter=" + walListingFilter(path), base})
	if err != nil {
		return WALContinuity{Status: WALUnverified, BackupLabel: in.BackupLabel, ArchiveID: in.ArchiveID,
			Detail: fmt.Sprintf("listing WAL segments in %s failed: %v", base, err)}
	}
	// A header that can't be read isn't a finding: the backups' LSNs still
	// decide the size, and SegmentSizeSource records that they did.
	if file, ok := pickHeaderSegment(listing); ok {
		data, err := run([]string{stanzaArg, "repo-get", base + "/" + file.path})
		if err == nil {
			in.HeaderSegSize, err = segmentSizeFromHeader([]byte(data), file)
		}
		if err != nil {
			slog.Warn("get_pgbackrest_status: WAL segment header not read, inferring the segment size from the backups' LSNs",
				"stanza", in.Stanza, "segment", file.path, "err", err)
		}
	}
	return evaluateWALContinuity(in, top, historyText, listing)
}

// maxHeaderSegmentBytes bounds the one segment fetched for its header. A
// compressed segment is usually a few KB; an uncompressed one is the full
// segment size, so very large uncompressed segments aren't fetched.
const maxHeaderSegmentBytes = 64 << 20

// headerSegment is the archived segment whose header gives the segment size.
type headerSegment struct {
	path        string // relative to the archive id directory
	name        string // 24-hex-digit segment name
	compression string // "gz", "bz2" or "" (uncompressed)
	size        int64
}

// pickHeaderSegment chooses the smallest archived segment in a recursive
// repo-ls listing that this package can decompress.
func pickHeaderSegment(listingJSON string) (headerSegment, bool) {
	var raw map[string]struct {
		Type string `json:"type"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonObjectPrefix(listingJSON))), &raw); err != nil {
		return headerSegment{}, false
	}
	var best headerSegment
	found := false
	for path, entry := range raw {
		if entry.Type != "file" || entry.Size <= 0 || entry.Size > maxHeaderSegmentBytes {
			continue
		}
		base := path[strings.LastIndexByte(path, '/')+1:]
		m := walSegmentNameRe.FindStringSubmatch(base)
		if m == nil {
			continue
		}
		var compression string
		switch {
		case strings.HasSuffix(base, ".gz"):
			compression = "gz"
		case strings.HasSuffix(base, ".bz2"):
			compression = "bz2"
		case strings.Contains(base, "."):
			continue // lz4, zst: no decompressor in the standard library
		}
		c := headerSegment{path: path, name: m[1], compression: compression, size: entry.Size}
		if !found || c.size < best.size || (c.size == best.size && c.path < best.path) {
			best, found = c, true
		}
	}
	return best, found
}

// walLongHeaderLen is the part of XLogLongPageHeaderData read here:
// xlp_magic(2) xlp_info(2) xlp_tli(4) xlp_pageaddr(8) xlp_rem_len(4)
// padding(4) xlp_sysid(8) xlp_seg_size(4) xlp_xlog_blcksz(4).
const walLongHeaderLen = 40

const xlpLongHeader = 0x0002

// segmentSizeFromHeader decompresses the start of a segment fetched with
// repo-get (which decrypts but never decompresses) and reads the segment
// size from its page header.
func segmentSizeFromHeader(data []byte, file headerSegment) (uint64, error) {
	var header []byte
	switch file.compression {
	case "gz", "bz2":
		// Skip anything a remote shell printed ahead of the file itself.
		magic := []byte{0x1f, 0x8b}
		if file.compression == "bz2" {
			magic = []byte("BZh")
		}
		idx := bytes.Index(data, magic)
		if idx < 0 {
			return 0, fmt.Errorf("%s: no %s stream in the repo-get output", file.path, file.compression)
		}
		var r io.Reader
		if file.compression == "gz" {
			zr, err := gzip.NewReader(bytes.NewReader(data[idx:]))
			if err != nil {
				return 0, fmt.Errorf("%s: %w", file.path, err)
			}
			r = zr
		} else {
			r = bzip2.NewReader(bytes.NewReader(data[idx:]))
		}
		header = make([]byte, walLongHeaderLen)
		if _, err := io.ReadFull(r, header); err != nil {
			return 0, fmt.Errorf("%s: decompressing: %w", file.path, err)
		}
		return parseWALLongHeader(header, file.name)
	default:
		// Uncompressed: the header is at the start, or after a text preamble.
		var lastErr error
		for off := 0; off+walLongHeaderLen <= len(data) && off <= 64<<10; {
			size, err := parseWALLongHeader(data[off:off+walLongHeaderLen], file.name)
			if err == nil {
				return size, nil
			}
			lastErr = err
			next := bytes.IndexByte(data[off:], '\n')
			if next < 0 {
				break
			}
			off += next + 1
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("output is shorter than a WAL page header")
		}
		return 0, fmt.Errorf("%s: %w", file.path, lastErr)
	}
}

// parseWALLongHeader reads xlp_seg_size from the long page header that
// starts every WAL segment, and checks the header really belongs to segment
// name: the page address must be that segment's start under the size it
// reports. Fields are in the server's byte order, so both are tried.
func parseWALLongHeader(b []byte, name string) (uint64, error) {
	if len(b) < walLongHeaderLen {
		return 0, fmt.Errorf("WAL page header is %d bytes, want %d", len(b), walLongHeaderLen)
	}
	_, logID, seg, err := parseSegmentName(name)
	if err != nil {
		return 0, err
	}
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		magic := order.Uint16(b[0:2])
		info := order.Uint16(b[2:4])
		size := uint64(order.Uint32(b[32:36]))
		blcksz := order.Uint32(b[36:40])
		if magic>>12 != 0xD || info&xlpLongHeader == 0 || !validSegmentSize(size) ||
			blcksz < 1024 || blcksz > 32768 || blcksz&(blcksz-1) != 0 {
			continue
		}
		per := segsPerLogID(size)
		if seg >= per || order.Uint64(b[8:16]) != (logID*per+seg)*size {
			continue
		}
		return size, nil
	}
	return 0, fmt.Errorf("not a valid first-page header for WAL segment %s", name)
}
