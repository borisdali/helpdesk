package main

// Preserving the unarchived pg_wal tail across restore_from_backup (v0.32
// item 13).
//
// Committed transactions live in pg_wal before they reach the archive: the
// segment being written, and completed segments still marked .ready when
// archiving lags or is broken. `pgbackrest --delta restore` removes every file
// the backup's manifest doesn't list, pg_wal's contents included, so recovery
// stopped at the end of the archive and those commits were lost even though
// they were on disk the whole time. Found by the canary test on the Docker
// fixture (2026-10-19): with archiving broken, a row in a .ready segment and a
// row in the open segment were both gone after the restore, pg_wal empty.
//
// The fix is the one the PostgreSQL docs give for archive recovery: move the
// segments aside before the restore and copy them back before startup.
// Recovery restores from the archive first and falls back to pg_wal, so it
// replays the tail and stops at its first invalid record, CRC-checked like
// any other WAL. Same fixture, same crash: both rows survived.
//
// The canary run also found a second hazard: nothing archives the
// copied-back segments, and the new timeline then depends on WAL the repo
// doesn't have — the next restore would stop at the old archive end. So
// every copied-back segment recovery will replay past the archive end is
// pushed with pgbackrest archive-push before startup, except the newest,
// which PostgreSQL itself archives as .partial when it promotes. Not a .ready
// marker: the integration test found (2026-10-19) that PostgreSQL deletes the
// marker during archive recovery, and after promotion deletes the segment
// itself as if archived — it would be gone from both pg_wal and the repo.
//
// Only segments whose first page header matches their own name count:
// PostgreSQL pre-creates future segments by renaming old ones, and those
// still hold the old content. Copying one back is harmless (recovery rejects
// it like any end of WAL), but archiving one would put garbage in the repo
// under a real segment's name.

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// PgWalTail reports what restore_from_backup did with the WAL that was in
// pg_wal when it ran.
type PgWalTail struct {
	// SavedTo holds the original copy of every segment moved out of pg_wal.
	// It is kept so nothing is lost if recovery doesn't go as predicted;
	// remove it once verify_restore_recovery reports verified.
	SavedTo string `json:"saved_to"`
	// CopiedBack lists every segment put back into pg_wal before startup.
	CopiedBack []string `json:"copied_back,omitempty"`
	// ReplayThroughWAL is the newest copied-back segment recovery should
	// replay, continuing past the archive end; empty when pg_wal held
	// nothing newer than the archive.
	ReplayThroughWAL string `json:"replay_through_wal,omitempty"`
	// PushedToArchive lists the segments pushed to the archive before
	// startup, so the new timeline doesn't depend on WAL only this host has.
	PushedToArchive []string `json:"pushed_to_archive,omitempty"`
	Detail          string   `json:"detail"`
}

// Marker lines the restore script prints for restoreFromBackupImpl.
const (
	walTailSavedMarker   = "helpdesk-wal-tail-saved="
	walTailSegmentMarker = "helpdesk-wal-tail-segment="
)

// restoreScriptWithWALTail is restore_from_backup's restore command. Before
// the restore it moves pg_wal's segment files to a directory beside PGDATA —
// outside it, since --delta restore would clean anything inside — and after
// it copies them back and prints each one's first page header for
// planWALTail. set -e: if the tail can't be saved, the restore doesn't run,
// rather than silently losing it again.
func restoreScriptWithWALTail(stanza string) string {
	return `set -e
PGDATA=$(grep '^pg1-path=' /etc/pgbackrest.conf | cut -d= -f2)
rm -f "$PGDATA/postmaster.pid"
SAVED=
if WAL=$(cd "$PGDATA/pg_wal" 2>/dev/null && pwd -P); then
  SAVED="$(dirname "$(cd "$PGDATA" && pwd -P)")/pg_wal.saved-$(date -u +%Y%m%dT%H%M%SZ)"
  mkdir "$SAVED"
  echo "` + walTailSavedMarker + `$SAVED"
  for f in "$WAL"/*; do
    n=${f##*/}
    case "$n" in *[!0-9A-F]*) continue;; esac
    [ ${#n} -eq 24 ] || continue
    mv "$f" "$SAVED/"
  done
fi
pgbackrest --stanza=` + shellQuote(stanza) + ` --delta restore
if [ -n "$SAVED" ]; then
  WAL=$(cd "$PGDATA/pg_wal" && pwd -P)
  for f in "$SAVED"/*; do
    [ -f "$f" ] || continue
    n=${f##*/}
    cp -p "$f" "$WAL/$n"
    echo "` + walTailSegmentMarker + `$n $(od -An -tx1 -N` + fmt.Sprint(walLongHeaderLen) + ` "$f" | tr -d ' \n')"
  done
fi`
}

// pushToArchiveScript archive-pushes names from pg_wal, as PostgreSQL's own
// archive_command would (relative path, from PGDATA). names are 24-hex
// segment names checked by planWALTail, so they need no quoting. pgBackRest
// accepts a segment it already holds with the same checksum.
func pushToArchiveScript(stanza string, names []string) string {
	var b strings.Builder
	b.WriteString(`set -e
PGDATA=$(grep '^pg1-path=' /etc/pgbackrest.conf | cut -d= -f2)
cd "$PGDATA"
for n in`)
	for _, n := range names {
		b.WriteString(" " + n)
	}
	b.WriteString(`; do pgbackrest --stanza=` + shellQuote(stanza) + ` archive-push "pg_wal/$n"; done`)
	return b.String()
}

// tailSegment is one copied-back segment and its first page header.
type tailSegment struct {
	Name   string
	Header []byte
}

// parseWALTailOutput splits the restore script's marker lines from
// pgBackRest's own output.
func parseWALTailOutput(out string) (savedTo string, segs []tailSegment, rest string) {
	var kept []string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, walTailSavedMarker):
			savedTo = strings.TrimSpace(strings.TrimPrefix(line, walTailSavedMarker))
		case strings.HasPrefix(line, walTailSegmentMarker):
			name, hexHeader, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(line, walTailSegmentMarker)), " ")
			header, _ := hex.DecodeString(hexHeader)
			segs = append(segs, tailSegment{Name: name, Header: header})
		default:
			kept = append(kept, line)
		}
	}
	return savedTo, segs, strings.Join(kept, "\n")
}

// planWALTail decides, from the copied-back segments and the WAL check run
// before the restore, how far recovery should replay past the archive end
// and which segments to push to the archive. Recovery reads segments of the
// newest timeline (the one the server was running on) from pg_wal once the
// archive runs out, one segment number after another, so the tail counts as
// far as it is contiguous from the archive end.
func planWALTail(savedTo string, segs []tailSegment, wal *WALContinuity) *PgWalTail {
	if savedTo == "" {
		return nil
	}
	tail := &PgWalTail{SavedTo: savedTo}
	for _, s := range segs {
		tail.CopiedBack = append(tail.CopiedBack, s.Name)
	}
	if len(segs) == 0 {
		tail.Detail = "pg_wal held no WAL segments; nothing beyond the archive to recover"
		return tail
	}
	if wal == nil || wal.ArchiveEndWAL == "" || wal.LatestTimeline == 0 {
		tail.Detail = fmt.Sprintf("%d segment(s) copied back into pg_wal; without an archive end point their effect can't be predicted", len(segs))
		return tail
	}
	_, endLog, endSeg, err := parseSegmentName(wal.ArchiveEndWAL)
	if err != nil {
		tail.Detail = fmt.Sprintf("%d segment(s) copied back into pg_wal; archive end %q unreadable: %v", len(segs), wal.ArchiveEndWAL, err)
		return tail
	}

	// Real segments of the newest timeline, by segment number.
	byNo := map[uint64]string{}
	var size uint64
	for _, s := range segs {
		tli, logID, seg, err := parseSegmentName(s.Name)
		if err != nil || tli != wal.LatestTimeline {
			continue
		}
		sz, err := parseWALLongHeader(s.Header, s.Name)
		if err != nil || (wal.SegmentSizeBytes != 0 && sz != wal.SegmentSizeBytes) {
			continue
		}
		size = sz
		byNo[logID*segsPerLogID(sz)+seg] = s.Name
	}
	if size == 0 {
		tail.Detail = fmt.Sprintf("%d segment(s) copied back into pg_wal, none a real segment of timeline %d; recovery should end at the archive end %s",
			len(segs), wal.LatestTimeline, wal.ArchiveEndWAL)
		return tail
	}

	endNo := endLog*segsPerLogID(size) + endSeg
	reach := endNo
	// The server's own copy of the archive end's segment number: on a newer
	// timeline than the archived one it holds WAL the archive doesn't.
	if name, ok := byNo[endNo]; ok && name != wal.ArchiveEndWAL {
		tail.ReplayThroughWAL = name
	}
	for {
		name, ok := byNo[reach+1]
		if !ok {
			break
		}
		reach++
		tail.ReplayThroughWAL = name
		if _, ok := byNo[reach+1]; ok {
			// Not the newest: PostgreSQL archives the newest as .partial
			// at promotion, the rest nobody would.
			tail.PushedToArchive = append(tail.PushedToArchive, name)
		}
	}
	sort.Strings(tail.PushedToArchive)
	if tail.ReplayThroughWAL == "" {
		tail.Detail = fmt.Sprintf("%d segment(s) copied back into pg_wal, none newer than the archive end %s; recovery should end there", len(segs), wal.ArchiveEndWAL)
		return tail
	}
	tail.Detail = fmt.Sprintf("pg_wal held WAL past the archive end %s: recovery should replay it through %s, recovering commits that were never archived",
		wal.ArchiveEndWAL, tail.ReplayThroughWAL)
	return tail
}
