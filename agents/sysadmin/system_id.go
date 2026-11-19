package main

// The source's PostgreSQL system identifier, read for the drills the recovery
// estimate counts.
//
// A restore drill is linked to its source by system_identifier, not by the name
// in infra.json: a name can be reused for a rebuilt server, and the identifier
// cannot (a cluster made with initdb has a new one, a restore of the same backup
// keeps it). The estimate matches drills by identifier when it is known here, and
// by name, saying so, when it is not.
//
// It is read the way the instance can be asked. A running instance is asked
// (pg_control_system); one that is down is read off its data directory with
// pg_controldata, which needs no server. Where neither works (a stopped
// container that cannot be entered, no pg_controldata on the host) the answer is
// "not known", never a guess, and never an error: this is a refinement of an
// estimate and must not stop a plan.

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"time"
)

// systemIDScript prints system_id=<digits> or system_id= when it cannot tell.
// Local-socket psql as the postgres OS user, like every maintenance script, and
// then pg_controldata on the data directory (the one pgBackRest is configured for,
// else $PGDATA), found the way pg_waldump is: by the version in PG_VERSION, then
// on the PATH.
const systemIDScript = `export LC_ALL=C
SID=$(psql -X -At -d postgres -c "SELECT system_identifier FROM pg_control_system()" 2>/dev/null | head -1)
if [ -z "$SID" ]; then
  DD=$(grep '^pg1-path=' /etc/pgbackrest.conf 2>/dev/null | head -1 | cut -d= -f2)
  [ -n "$DD" ] || DD="${PGDATA:-}"
  BIN=""
  if [ -n "$DD" ] && [ -r "$DD/PG_VERSION" ]; then
    MAJOR=$(cat "$DD/PG_VERSION")
    for p in "/usr/lib/postgresql/$MAJOR/bin/pg_controldata" "/usr/pgsql-$MAJOR/bin/pg_controldata"; do
      if [ -x "$p" ]; then BIN="$p"; break; fi
    done
  fi
  if [ -z "$BIN" ]; then BIN=$(command -v pg_controldata 2>/dev/null); fi
  if [ -n "$BIN" ] && [ -n "$DD" ]; then
    SID=$("$BIN" "$DD" 2>/dev/null | grep '^Database system identifier' | head -1 | cut -d: -f2 | tr -d ' ')
  fi
fi
echo "system_id=$SID"
`

var systemIDRe = regexp.MustCompile(`^[0-9]{1,24}$`)

// parseSystemID reads the script's output. Anything that is not a plain number is
// "not known": an error message that ended up on the line must not be taken for
// an identifier.
func parseSystemID(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "system_id="); ok {
			if v = strings.TrimSpace(v); systemIDRe.MatchString(v) {
				return v
			}
			return ""
		}
	}
	return ""
}

// systemIDTimeout bounds the read: the plan must not wait on it.
const systemIDTimeout = 15 * time.Second

// sourceSystemIDFn reads the system identifier of the database a call targets, or
// "" when it cannot be read. A variable so a test can say what it reads.
var sourceSystemIDFn = readSourceSystemID

func readSourceSystemID(ctx context.Context, target string) string {
	inst, err := instanceFor(target)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, systemIDTimeout)
	defer cancel()
	out, err := inst.maintenance(ctx, systemIDScript, false)
	if err != nil {
		slog.Debug("system identifier not read", "target", target, "err", err)
		return ""
	}
	return parseSystemID(out)
}
