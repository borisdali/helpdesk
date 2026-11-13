package main

// Reading commits out of WAL with pg_waldump (v0.35, Tier B: the exact loss).
//
// What the archive-based estimate cannot say is how many transactions a
// restore throws away, or which of them. pg_waldump decodes the WAL, and every
// transaction that wrote anything ends in a COMMIT record with its transaction
// id, its exact LSN and a timestamp. This file reads those records. It does not
// run anything: where the binary is and how it is invoked depend on the host.
//
// Built against output captured on a real Ubuntu 24.04 host with PostgreSQL
// 16.15 (testdata/waldump_commit_est.txt is one of its lines, verbatim), which
// showed three things the local fixtures did not:
//
//   - The timestamp ends in a zone ABBREVIATION ("EST"), and pg_waldump is a
//     libc program, so which one follows the TZ of the process that runs it.
//     An abbreviation is not an offset (EST, IST and CST each name more than
//     one), so none is guessed: the command is run with TZ=UTC, which yields
//     "UTC", and a commit whose zone is anything else is still COUNTED but has
//     no time, and is reported as such.
//   - A DDL commit carries a long run of invalidation messages: one real line
//     was 1245 bytes, and they are the commits that matter most (a dropped
//     table). A line scanner with a token limit fails on exactly those, so the
//     reader here has no line limit and keeps only the head of each line.
//   - A segment in pg_wal is not necessarily WAL: PostgreSQL recycles old
//     files into names for segments that do not exist yet, and pg_waldump says
//     "could not find a valid record" on them. What lies past the instance's
//     own position (pg_current_wal_lsn) is not read.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// WALCommit is one transaction commit read from WAL.
type WALCommit struct {
	// LSN is where the commit record starts, XID the transaction it commits.
	LSN uint64 `json:"lsn"`
	XID uint64 `json:"xid"`
	// Time is when it committed, in UTC. Zero when the zone it was printed in
	// could not be read as UTC or a numeric offset (see TimeUnread).
	Time time.Time `json:"time"`
	// TimeUnread says the commit was counted but its time was not read: the zone
	// was an abbreviation other than UTC, or the timestamp did not parse.
	TimeUnread bool `json:"time_unread,omitempty"`
}

// WALTxEnd is one transaction ending, a commit or an abort, in the order the
// records appear in WAL. A time-target recovery stops before the first of these,
// in this order, whose timestamp is later than the target (an abort counts: it
// carries a time too), so the order and the aborts matter even though only the
// commits are counted.
type WALTxEnd struct {
	WALCommit
	Commit bool `json:"commit"`
}

// WALCommits is what reading a stretch of WAL found.
type WALCommits struct {
	Commits []WALCommit
	// Ends is every commit and abort, in WAL order (Commits is the commits among
	// them).
	Ends []WALTxEnd
	// Aborts counts transactions that rolled back in the stretch. They lose
	// nothing, and are kept apart so a count of commits is never padded by them.
	Aborts int
	// Unread counts Transaction records the reader did not recognise as a commit
	// or an abort (a form it has not been shown). They are not counted as
	// anything, and a caller that needs the commit count to be exact must treat a
	// non-zero value as "at least this many".
	Unread int
	// EndError is the text of pg_waldump's own error line when it ended on one
	// ("pg_waldump: error: ..."). EndKind says what it means and EndAt, for the
	// end of valid WAL, where that was.
	EndError string
	EndKind  string
	EndAt    uint64
}

// What a pg_waldump error line means. Only the forms below have been seen on a
// real host (captured 2026-11-10); anything else is WALEndOther and is treated
// as a failure, never as a normal end: the safe direction is to read less.
const (
	// WALEndOfValid: it read every valid record in a live segment and reached the
	// zeroed space after the instance's own position ("invalid record length at
	// 0/430001F8: expected at least 24, got 0"). The commits before it are all
	// there is. Bounding the read with -e at the instance's position avoids the
	// message altogether (exit 0, no stderr), which is how it should be asked.
	WALEndOfValid = "end_of_valid_wal"
	// WALEndNotWAL: the file holds no valid record where one was asked for ("could
	// not find a valid record after 0/44000000"): a recycled file named for a
	// segment that does not exist yet.
	WALEndNotWAL = "not_wal"
	// WALEndMissing: the segment file is not where it was looked for ("could not
	// locate WAL file ...": the archive has no such segment on this timeline).
	WALEndMissing = "missing_segment"
	// WALEndOther: any other error line.
	WALEndOther = "other"
)

// Captured error forms. The zero-length form is only the end of the written WAL
// when what it found is zero bytes: any other length is damage.
var (
	walEndZeroed  = regexp.MustCompile(`invalid record length at ([0-9A-Fa-f]+/[0-9A-Fa-f]+): expected at least \d+, got 0\b`)
	walEndNotWAL  = regexp.MustCompile(`could not find a valid record after `)
	walEndMissing = regexp.MustCompile(`could not locate WAL file `)
)

func classifyWALEnd(msg string) (kind string, at uint64) {
	switch {
	case walEndZeroed.MatchString(msg):
		m := walEndZeroed.FindStringSubmatch(msg)
		lsn, err := parseLSN(m[1])
		if err != nil {
			return WALEndOther, 0
		}
		return WALEndOfValid, lsn
	case walEndNotWAL.MatchString(msg):
		return WALEndNotWAL, 0
	case walEndMissing.MatchString(msg):
		return WALEndMissing, 0
	}
	return WALEndOther, 0
}

// A commit or an abort record, from the head of its line. The head is all that
// is read: after the timestamp come the relations, statistics and invalidation
// messages, which can run to thousands of bytes and say nothing about the count.
//
//	rmgr: Transaction len (rec/tot):   1245/  1245, tx:        784, lsn: 0/420087A0, prev 0/42008768, desc: COMMIT 2026-11-09 12:18:25.702361 EST; rels: ...
var walTxnRecord = regexp.MustCompile(
	`^rmgr: Transaction .*?tx:\s+(\d+), lsn: ([0-9A-Fa-f]+/[0-9A-Fa-f]+), prev [0-9A-Fa-f]+/[0-9A-Fa-f]+, desc: ` +
		`(COMMIT|COMMIT_PREPARED|ABORT|ABORT_PREPARED)\b(?:\s+\d+:)?\s+` + // a prepared one names the prepared xid first
		`(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d(?:\.\d+)?)\s+(\S+?)(?:;|\s|$)`)

// walBenignTxnKind matches the Transaction records that end no transaction: the
// assignment of subtransaction ids, cache invalidations and a PREPARE (whose
// commit or abort arrives later as COMMIT_PREPARED or ABORT_PREPARED).
var walBenignTxnKind = regexp.MustCompile(`desc: (ASSIGNMENT|INVALIDATIONS|PREPARE)\b`)

// canonicalLSN is PostgreSQL's "X/Y" with one to eight hex digits each side.
var canonicalLSN = regexp.MustCompile(`^[0-9A-Fa-f]{1,8}/[0-9A-Fa-f]{1,8}$`)

// walHeadLimit is how much of each line is kept: enough for the head above, far
// less than the longest line, and bounded whatever the input.
const walHeadLimit = 1024

// ParseWALCommits reads pg_waldump output and returns the commits in it, in the
// order they appear. Lines that are not Transaction records are ignored.
func ParseWALCommits(r io.Reader) (WALCommits, error) {
	var out WALCommits
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		head, err := readLineHead(br)
		if len(head) > 0 {
			out.consume(head)
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

// readLineHead reads one line and returns its first walHeadLimit bytes, dropping
// the rest of a long line as it goes rather than buffering it.
func readLineHead(br *bufio.Reader) (string, error) {
	var head []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if room := walHeadLimit - len(head); room > 0 {
			if len(chunk) > room {
				chunk = chunk[:room]
			}
			head = append(head, chunk...)
		}
		if err != nil {
			return string(head), err
		}
		if !isPrefix {
			return string(head), nil
		}
	}
}

func (o *WALCommits) consume(line string) {
	switch {
	case strings.HasPrefix(line, "pg_waldump: error:"):
		o.EndError = strings.TrimSpace(strings.TrimPrefix(line, "pg_waldump: error:"))
		o.EndKind, o.EndAt = classifyWALEnd(o.EndError)
		return
	case !strings.HasPrefix(line, "rmgr: Transaction"):
		return
	}
	m := walTxnRecord.FindStringSubmatch(line)
	if m == nil {
		// A Transaction record that is not a commit or an abort in the form read.
		// The kinds that end no transaction are named; anything else is NOT assumed
		// harmless. Only COMMIT and ABORT lines used to be counted as not understood,
		// so a version that changed how a commit is described (a rename, a different
		// case, a new layout) would have read as no commits at all, and a plan would
		// have said an exact zero: the "nothing lost" this must never say by accident.
		if !walBenignTxnKind.MatchString(line) {
			o.Unread++
		}
		return
	}
	xid, _ := strconv.ParseUint(m[1], 10, 64)
	lsn, err := parseLSN(m[2])
	if err != nil {
		o.Unread++
		return
	}
	c := WALCommit{LSN: lsn, XID: xid}
	if t, ok := parseWALTime(m[4], m[5]); ok {
		c.Time = t
	} else {
		c.TimeUnread = true
	}
	if strings.HasPrefix(m[3], "ABORT") {
		o.Aborts++
		o.Ends = append(o.Ends, WALTxEnd{WALCommit: c})
		return
	}
	o.Commits = append(o.Commits, c)
	o.Ends = append(o.Ends, WALTxEnd{WALCommit: c, Commit: true})
}

// parseWALTime reads a pg_waldump timestamp as UTC. Only a zone that is
// unambiguous is accepted: "UTC" (what TZ=UTC prints) or a numeric offset
// ("+05", "-0530", "+05:30"). Any other abbreviation is refused, not guessed.
func parseWALTime(stamp, zone string) (time.Time, bool) {
	layout := "2006-01-02 15:04:05"
	if strings.Contains(stamp, ".") {
		layout += ".999999"
	}
	local, err := time.Parse(layout, stamp)
	if err != nil {
		return time.Time{}, false
	}
	var offset time.Duration
	switch zone {
	case "UTC", "GMT":
	default:
		d, ok := parseNumericZone(zone)
		if !ok {
			return time.Time{}, false
		}
		offset = d
	}
	return local.Add(-offset).UTC(), true
}

// parseNumericZone reads "+05", "-0530" or "+05:30" as an offset east of UTC.
func parseNumericZone(z string) (time.Duration, bool) {
	if len(z) < 2 || (z[0] != '+' && z[0] != '-') {
		return 0, false
	}
	sign := time.Duration(1)
	if z[0] == '-' {
		sign = -1
	}
	digits := strings.ReplaceAll(z[1:], ":", "")
	if len(digits) != 2 && len(digits) != 4 {
		return 0, false
	}
	h, err := strconv.Atoi(digits[:2])
	if err != nil {
		return 0, false
	}
	m := 0
	if len(digits) == 4 {
		if m, err = strconv.Atoi(digits[2:]); err != nil {
			return 0, false
		}
	}
	if h > 14 || m > 59 {
		return 0, false
	}
	return sign * (time.Duration(h)*time.Hour + time.Duration(m)*time.Minute), true
}

// walDumpArgs are the arguments that make pg_waldump print the transaction
// records of one segment file between two LSNs, validated: the LSNs are the
// only caller-supplied text that reaches a command line, so they are checked,
// not quoted and hoped for. end may be empty (to the end of the file).
func walDumpArgs(segmentPath, start, end string) ([]string, error) {
	if segmentPath == "" || strings.ContainsAny(segmentPath, "\x00\n") {
		return nil, errors.New("pg_waldump: no usable segment path")
	}
	args := []string{"-r", "Transaction"}
	for _, f := range []struct{ flag, v string }{{"-s", start}, {"-e", end}} {
		if f.v == "" {
			continue
		}
		// The canonical form only: parseLSN also trims whitespace, which must not
		// reach a command line.
		if !canonicalLSN.MatchString(f.v) {
			return nil, fmt.Errorf("pg_waldump: %s is not an LSN: %q", f.flag, f.v)
		}
		args = append(args, f.flag, f.v)
	}
	return append(args, segmentPath), nil
}

// walDumpEnv is the environment pg_waldump runs in: UTC, so that a commit time
// is printed as one the parser can trust. It is not set by the host's own zone.
var walDumpEnv = []string{"TZ=UTC"}
