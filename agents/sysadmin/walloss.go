package main

// Counting what a restore discards, from the WAL that was read (v0.35, Tier B).
//
// Where recovery stops decides what is lost (walrange.go has the rules). This
// applies them to the commit and abort records that were read, in WAL order:
//
//   - TIME target: recovery stops before the first commit or abort whose
//     timestamp is later than the target. That record and every commit after
//     it, whatever its own timestamp, is discarded.
//   - LSN target: recovery applies the first record that starts at or after the
//     target and stops there. Every commit after that record is discarded.
//
// What it reports is always what it could establish: a count that is a lower
// bound says so (the read did not reach the end of what the instance wrote, or
// a record was not understood), and a read that could not be made says why. It
// never turns "could not read" into "nothing lost".

import (
	"fmt"
	"strings"
	"time"
)

// Transaction-loss statuses.
const (
	// TxLossMeasured: the commits were counted from the WAL.
	TxLossMeasured = "measured"
	// TxLossUnavailable: they could not be counted; Reason says why. The
	// archive-based figures of the estimate stand alone.
	TxLossUnavailable = "unavailable"
)

// TransactionLoss is the number of transactions a restore discards, from the WAL.
type TransactionLoss struct {
	Status string `json:"status"`
	// Commits is how many committed transactions come after the stop point.
	// AtLeast says it is a lower bound: the read stopped short of the end of what
	// the instance wrote (the archive's end for a down instance, the cap on the
	// number of segments, an unreadable data directory) or met a record it did
	// not understand.
	Commits *int `json:"commits,omitempty"`
	AtLeast bool `json:"at_least,omitempty"`
	// FirstAt/LastAt and FirstLSN/LastLSN are the first and last discarded commit.
	FirstAt  string `json:"first_commit_at,omitempty"`
	LastAt   string `json:"last_commit_at,omitempty"`
	FirstLSN string `json:"first_commit_lsn,omitempty"`
	LastLSN  string `json:"last_commit_lsn,omitempty"`
	// SegmentsRead is how many WAL segments were decoded, ReadMillis how long it
	// took (the first real measurement of what a read costs).
	SegmentsRead int   `json:"segments_read,omitempty"`
	ReadMillis   int64 `json:"read_millis,omitempty"`
	// Decoder is the pg_waldump that produced the count, as it reports itself
	// ("pg_waldump (PostgreSQL) 16.15 ..."), so a surprise can be put down to a
	// version. The reader was built against the output of 16 and 18.
	Decoder string `json:"decoder,omitempty"`
	// Basis says in a sentence what the count rests on and where it stops.
	Basis string `json:"basis,omitempty"`
	// Reason says why it could not be measured.
	Reason string `json:"reason,omitempty"`
}

func unavailableLoss(format string, args ...any) *TransactionLoss {
	return &TransactionLoss{Status: TxLossUnavailable, Reason: fmt.Sprintf(format, args...)}
}

// countDiscarded applies the stop rule to what was read. parts are the parsed
// output of each range, in order; firstRecord is the LSN of the first record at
// or after an LSN target (nil when it was not read).
func countDiscarded(plan WALReadPlan, parts []WALCommits, firstRecord *uint64) *TransactionLoss {
	var (
		ends   []WALTxEnd
		unread int
	)
	for _, p := range parts {
		ends = append(ends, p.Ends...)
		unread += p.Unread
	}

	// Records were read and not one could be taken for a commit or an abort: the
	// output was not understood (a format this does not know), which must not read
	// as an exact "no transactions".
	if len(ends) == 0 && unread > 0 {
		return unavailableLoss("the decoder's output was not understood: %d transaction record(s) were read and none could be taken for a commit or an abort, so this PostgreSQL version's output format may differ from the ones this was built against", unread)
	}
	discarded, how, failed := discardedCommits(plan, ends, firstRecord)
	if failed != nil {
		return failed
	}

	n := len(discarded)
	loss := &TransactionLoss{Status: TxLossMeasured, Commits: &n, SegmentsRead: plan.Segments}
	if n > 0 {
		first, last := discarded[0], discarded[n-1]
		loss.FirstLSN, loss.LastLSN = lsnText(first.LSN), lsnText(last.LSN)
		if !first.TimeUnread {
			loss.FirstAt = first.Time.UTC().Format(time.RFC3339)
		}
		if !last.TimeUnread {
			loss.LastAt = last.Time.UTC().Format(time.RFC3339)
		}
	}
	loss.AtLeast = !plan.Complete() || unread > 0
	loss.Basis = lossBasis(plan, how, unread)
	return loss
}

// discardedCommits applies the stop rule to the commit and abort records, in WAL
// order: the commits recovery would throw away, and a sentence on how the stop was
// found. A non-nil loss means the stop could not be found, and is the answer.
func discardedCommits(plan WALReadPlan, ends []WALTxEnd, firstRecord *uint64) ([]WALCommit, string, *TransactionLoss) {
	var discarded []WALCommit
	var how string
	switch plan.Mode {
	case "time":
		stop := -1
		for i, e := range ends {
			if e.TimeUnread {
				// Past this record the stop cannot be told from the rest: a commit whose
				// time is unknown could be the one.
				return nil, "", unavailableLoss("a commit or abort record's time (LSN %s) could not be read as UTC, so the point where recovery stops cannot be found", lsnText(e.LSN))
			}
			if e.Time.After(plan.TargetTime) {
				stop = i
				break
			}
		}
		if stop >= 0 {
			for _, e := range ends[stop:] {
				if e.Commit {
					discarded = append(discarded, e.WALCommit)
				}
			}
			how = fmt.Sprintf("recovery stops before the first commit or abort later than the target time (LSN %s)", lsnText(ends[stop].LSN))
		} else {
			how = "no commit or abort in the WAL read is later than the target time"
		}
	case "lsn":
		boundary := plan.TargetLSN
		if firstRecord != nil {
			boundary = *firstRecord
			how = fmt.Sprintf("recovery applies the first record at or after the target LSN (%s) and stops after it", lsnText(boundary))
		} else {
			how = "the first record at or after the target LSN was not identified, so the count may include one commit that recovery keeps"
		}
		for _, e := range ends {
			if e.Commit && e.LSN > boundary {
				discarded = append(discarded, e.WALCommit)
			}
		}
	default:
		return nil, "", unavailableLoss("no stop point to apply")
	}
	return discarded, how, nil
}

// lossBasis says what the count rests on: how the WAL was read, where recovery
// stops, and where the read ends.
func lossBasis(plan WALReadPlan, how string, unread int) string {
	parts := []string{"counted by decoding the WAL with pg_waldump from " + plan.Start, how}
	switch {
	case plan.Truncated:
		parts = append(parts, fmt.Sprintf("the read stopped after %d segments, so there may be more", plan.Segments))
	case plan.TailSkipped != "":
		parts = append(parts, plan.TailSkipped+", so there may be more")
	case plan.Serving:
		parts = append(parts, "read up to the running instance's own position; it grows until the instance is stopped")
	default:
		parts = append(parts, "read to the end of the archive: WAL the instance had not archived is not counted, so there may be more")
	}
	if unread > 0 {
		parts = append(parts, fmt.Sprintf("%d transaction record(s) were not understood and are not counted", unread))
	}
	return strings.Join(parts, "; ")
}

// Sentence is the count in a few words a person can read at the gate.
func (l *TransactionLoss) Sentence() string {
	switch {
	case l == nil:
		return ""
	case l.Status != TxLossMeasured || l.Commits == nil:
		return "Transactions could not be counted (" + l.Reason + ")."
	}
	noun := "transactions"
	if *l.Commits == 1 {
		noun = "transaction"
	}
	if l.AtLeast {
		return fmt.Sprintf("At least %d %s committed after the target would be discarded (counted from the WAL).", *l.Commits, noun)
	}
	return fmt.Sprintf("%d %s committed after the target would be discarded (counted from the WAL).", *l.Commits, noun)
}
