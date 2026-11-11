package main

// Running pg_waldump on the instance (v0.35, Tier B).
//
// One script, run as the postgres OS user through pgInstance.maintenance (the
// same path every other maintenance script takes, so Docker, Kubernetes, SSH
// hosts and a colocated install all work): it finds the binary, fetches the
// archived segments into a temp directory, runs pg_waldump over each range and
// prints only what the parser reads, between markers. Everything that reaches a
// command line is validated first (segment names, LSNs, the stanza); the data
// directory is quoted.
//
// Built against output captured on a real Ubuntu 24.04 host (see waldump.go):
//
//   - the binary is /usr/lib/postgresql/N/bin/pg_waldump, not on PATH, found by
//     the server's major version (PG_VERSION in the data directory, or the data
//     directory pgBackRest is configured for), then on PATH;
//   - it runs with TZ=UTC, so a commit time is not a zone abbreviation;
//   - a read of the live segment is bounded with -e at the instance's own
//     position (exit 0, nothing on stderr); an unbounded one ends on an error;
//   - how a read ended is reported (range_rc), because a read cut short by a
//     timeout looks like a short, successful one and would be counted as such.

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// walCountEnv turns the count off: a plan then says it was not made, and why.
// The read runs commands on a production host at plan time (an archive-get per
// segment and pg_waldump over up to walReadMaxSegments of WAL), so an operator
// has a way to stop it without a rebuild. It is on unless set to 0, false, off
// or no.
const walCountEnv = "HELPDESK_PITR_COUNT_TRANSACTIONS"

func transactionCountEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(walCountEnv))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// walReadTimeout bounds the whole read, fetches included. The script bounds each
// pg_waldump run itself (timeout 40) where the host has timeout(1).
var walReadTimeout = 60 * time.Second

// walReadOutputCap bounds what the script prints (head -c), and so what is held
// in memory. A read that reaches it is not trusted.
const walReadOutputCap = 48 << 20

var (
	walStanzaRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	walSegNameRe = regexp.MustCompile(`^[0-9A-F]{24}$`)
	walDirRe     = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)
	walRecordLSN = regexp.MustCompile(`lsn: ([0-9A-Fa-f]+/[0-9A-Fa-f]+)`)
)

// walReadScript is the script for plan. stanza is the pgBackRest stanza the
// archive-get calls name; dataDir the instance's data directory when known
// (otherwise the one pgBackRest is configured for is used).
func walReadScript(stanza, dataDir string, p WALReadPlan) (string, error) {
	if !walStanzaRe.MatchString(stanza) {
		return "", fmt.Errorf("the stanza %q is not a name this can pass to pgbackrest", stanza)
	}
	if dataDir != "" && !walDirRe.MatchString(dataDir) {
		return "", fmt.Errorf("the data directory %q is not a path this can pass to a shell", dataDir)
	}
	if len(p.Ranges) == 0 {
		return "", fmt.Errorf("there is nothing to read")
	}
	var b strings.Builder
	dd := ""
	if dataDir != "" {
		dd = shellQuote(dataDir)
	}
	b.WriteString(`DD=` + dd + `
[ -n "$DD" ] || DD=$(grep '^pg1-path=' /etc/pgbackrest.conf 2>/dev/null | head -1 | cut -d= -f2)
BIN=""
if [ -n "$DD" ] && [ -r "$DD/PG_VERSION" ]; then
  MAJOR=$(cat "$DD/PG_VERSION")
  for p in "/usr/lib/postgresql/$MAJOR/bin/pg_waldump" "/usr/pgsql-$MAJOR/bin/pg_waldump"; do
    if [ -x "$p" ]; then BIN="$p"; break; fi
  done
fi
if [ -z "$BIN" ]; then BIN=$(command -v pg_waldump 2>/dev/null); fi
if [ -z "$BIN" ]; then echo "waldump_missing=1"; exit 0; fi
T=$(mktemp -d 2>/dev/null) || { echo "waldump_tmp_failed=1"; exit 0; }
trap 'rm -rf "$T"' EXIT
TO=""; if command -v timeout >/dev/null 2>&1; then TO="timeout 40"; fi
export TZ=UTC
echo "waldump_version=$("$BIN" --version 2>&1 | head -1)"
`)
	for i, r := range p.Ranges {
		if !walSegNameRe.MatchString(r.First) || !walSegNameRe.MatchString(r.Last) {
			return "", fmt.Errorf("range %d has a segment name that is not one: %q..%q", i, r.First, r.Last)
		}
		var lsnArgs []string
		for _, f := range []struct{ flag, v string }{{"-s", r.StartLSN}, {"-e", r.EndLSN}} {
			if f.v == "" {
				continue
			}
			if !canonicalLSN.MatchString(f.v) {
				return "", fmt.Errorf("range %d: %s is not an LSN: %q", i, f.flag, f.v)
			}
			lsnArgs = append(lsnArgs, f.flag, f.v)
		}
		fmt.Fprintf(&b, "echo \"range_begin=%d\"\n", i)
		dir := `"$DD/pg_wal"`
		switch r.Source {
		case WALFromArchive:
			if len(r.Names) == 0 {
				return "", fmt.Errorf("range %d is from the archive and names no segments", i)
			}
			for _, n := range r.Names {
				if !walSegNameRe.MatchString(n) {
					return "", fmt.Errorf("range %d names a segment that is not one: %q", i, n)
				}
			}
			dir = `"$T"`
			fmt.Fprintf(&b, "ok=1\nfor s in %s; do\n  if ! pgbackrest --stanza=%s archive-get \"$s\" \"$T/$s\" >/dev/null 2>&1; then echo \"fetch_failed=$s\"; ok=0; break; fi\ndone\nif [ \"$ok\" = 1 ]; then\n",
				strings.Join(r.Names, " "), stanza)
		case WALFromPgWal:
			b.WriteString("if [ -n \"$DD\" ]; then\n")
		default:
			return "", fmt.Errorf("range %d has no known source: %q", i, r.Source)
		}
		// The first record at or after an LSN target, to know which record recovery
		// applies there.
		if i == 0 && p.Mode == "lsn" && r.StartLSN != "" {
			fmt.Fprintf(&b, "  echo \"first_record=$(\"$BIN\" -p %s -s %s -n 1 %s 2>/dev/null | head -1 | cut -c1-220)\"\n", dir, r.StartLSN, r.First)
		}
		args := strings.Join(append([]string{"-p " + dir, "-r Transaction"}, append(joinPairs(lsnArgs), r.First, r.Last)...), " ")
		fmt.Fprintf(&b, "  ( $TO \"$BIN\" %s 2>&1; echo $? > \"$T/rc.%d\" ) | cut -c1-220 | head -c %d\n", args, i, walReadOutputCap)
		// A newline first: pg_waldump killed mid-line (a timeout) leaves a record with
		// none, and the marker would be glued onto it and lost.
		fmt.Fprintf(&b, "  echo\n  echo \"range_rc=$(cat \"$T/rc.%d\" 2>/dev/null)\"\nfi\n", i)
		fmt.Fprintf(&b, "echo \"range_end=%d\"\n", i)
	}
	return b.String(), nil
}

// joinPairs turns ["-s","0/1","-e","0/2"] into ["-s 0/1","-e 0/2"].
func joinPairs(in []string) []string {
	var out []string
	for i := 0; i+1 < len(in); i += 2 {
		out = append(out, in[i]+" "+in[i+1])
	}
	return out
}

// walRangeResult is what one range printed.
type walRangeResult struct {
	// FetchFailed names the segment the archive would not give up.
	FetchFailed string
	// RC is pg_waldump's exit status; nil when it did not report one.
	RC     *int
	Parsed WALCommits
}

// walReadOutput is the script's output, split at its markers.
type walReadOutput struct {
	Missing     bool
	TmpFailed   bool
	Version     string
	FirstRecord *uint64
	Ranges      []walRangeResult
	// Capped says the output reached walReadOutputCap and is not to be trusted.
	Capped bool
}

// parseWALReadOutput splits the script's output into its ranges and parses each
// range's records.
func parseWALReadOutput(out string) (walReadOutput, error) {
	var res walReadOutput
	res.Capped = len(out) >= walReadOutputCap-4096
	var (
		cur     *walRangeResult
		buf     strings.Builder
		flushed bool
	)
	flush := func() error {
		if cur == nil || flushed {
			return nil
		}
		flushed = true
		p, err := ParseWALCommits(strings.NewReader(buf.String()))
		cur.Parsed = p
		return err
	}
	for _, line := range strings.Split(out, "\n") {
		k, v, isMarker := strings.Cut(line, "=")
		if isMarker {
			switch k {
			case "waldump_missing":
				res.Missing = true
				continue
			case "waldump_tmp_failed":
				res.TmpFailed = true
				continue
			case "waldump_version":
				res.Version = strings.TrimSpace(v)
				continue
			case "range_begin":
				res.Ranges = append(res.Ranges, walRangeResult{})
				cur, flushed = &res.Ranges[len(res.Ranges)-1], false
				buf.Reset()
				continue
			case "fetch_failed":
				if cur != nil {
					cur.FetchFailed = strings.TrimSpace(v)
				}
				continue
			case "first_record":
				if m := walRecordLSN.FindStringSubmatch(v); m != nil {
					if l, err := parseLSN(m[1]); err == nil {
						res.FirstRecord = &l
					}
				}
				continue
			case "range_rc":
				if cur != nil {
					if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
						cur.RC = &n
					}
					if err := flush(); err != nil {
						return res, err
					}
				}
				continue
			case "range_end":
				if err := flush(); err != nil {
					return res, err
				}
				cur = nil
				continue
			}
		}
		if cur != nil && line != "" {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	return res, flush()
}

// walReadFn runs the script on the instance. A variable so a test can say what
// the instance printed.
var walReadFn = func(ctx context.Context, inst pgInstance, script string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, walReadTimeout)
	defer cancel()
	return inst.maintenance(ctx, script, false)
}

// walReadClock is the clock the read is timed with, a variable for a test.
var walReadClock = time.Now

// measureTransactionLoss counts the transactions the restore in plan would
// discard, by decoding the WAL on the instance. It always returns something: a
// count, or the reason there is none. live is what the serving instance
// reported about itself, nil when it is down.
func measureTransactionLoss(ctx context.Context, inst pgInstance, plan PITRPlan, live *LivePreState) *TransactionLoss {
	if !transactionCountEnabled() {
		return unavailableLoss("counting transactions is turned off (%s)", walCountEnv)
	}
	rp, why := planWALRead(plan.Target, plan.NormalizedTarget, plan.WALContinuity, live, walReadMaxSegments)
	if why != "" {
		return unavailableLoss("%s", why)
	}
	dir := ""
	if live != nil {
		dir = live.DataDirectory
	}
	script, err := walReadScript(plan.Stanza, dir, rp)
	if err != nil {
		return unavailableLoss("%v", err)
	}
	began := walReadClock()
	out, err := walReadFn(ctx, inst, script)
	took := walReadClock().Sub(began)
	if err != nil {
		return unavailableLoss("reading the WAL on the instance failed after %s: %v", took.Round(time.Millisecond), err)
	}
	parsed, err := parseWALReadOutput(out)
	if err != nil {
		return unavailableLoss("the WAL decoder's output could not be read: %v", err)
	}
	if reason := parsed.failure(rp); reason != "" {
		return unavailableLoss("%s", reason)
	}
	parts := make([]WALCommits, len(parsed.Ranges))
	for i, r := range parsed.Ranges {
		parts[i] = r.Parsed
	}
	var first *uint64
	if rp.Mode == "lsn" {
		first = parsed.FirstRecord
	}
	loss := countDiscarded(rp, parts, first)
	loss.ReadMillis = took.Milliseconds()
	loss.Decoder = parsed.Version
	return loss
}

// failure says why the read cannot be trusted, or "". Anything short of every
// range having run to a clean end is a failure: a count made from part of the
// WAL is not offered as if it were the whole.
func (o walReadOutput) failure(p WALReadPlan) string {
	switch {
	case o.Missing:
		return "pg_waldump was not found on the instance (looked for it by the server's major version under /usr/lib/postgresql and /usr/pgsql, and on PATH)"
	case o.TmpFailed:
		return "no temporary directory could be created on the instance to read the WAL from"
	case o.Capped:
		return fmt.Sprintf("the decoded WAL was larger than the %d MiB this reads, so it was not counted", walReadOutputCap>>20)
	case len(o.Ranges) < len(p.Ranges):
		return fmt.Sprintf("the read ended after %d of %d ranges", len(o.Ranges), len(p.Ranges))
	}
	for i, r := range o.Ranges {
		switch {
		case r.FetchFailed != "":
			return fmt.Sprintf("the archive would not give up segment %s (pgbackrest archive-get failed)", r.FetchFailed)
		case r.RC == nil:
			if p.Ranges[i].Source == WALFromPgWal {
				return "the instance's data directory could not be found, so its own WAL was not read"
			}
			return "pg_waldump did not report how it ended, so the read may have been cut short"
		case *r.RC != 0:
			why := r.Parsed.EndError
			if why == "" {
				why = "no error line"
			}
			return fmt.Sprintf("pg_waldump failed on range %d (exit %d): %s", i+1, *r.RC, why)
		}
	}
	return ""
}
