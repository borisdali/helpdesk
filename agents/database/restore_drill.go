package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"google.golang.org/adk/agent"

	"helpdesk/internal/audit"
	"helpdesk/internal/psqlx"
)

// restore drill verification (v0.36).
//
// A drill is a restore of the source's backup onto a clone the customer
// brought. This tool reads both servers, read-only, and says whether the clone
// is what the record claims: a copy of the same system, not the source itself,
// not a standby of it, consistent, comparable in size, and not able to push its
// own WAL into the source's archive. It records nothing: the gateway writes the
// drill, with this verdict, as a human act. The model never links a drill.

// drillSizeTolerance is how far a clone's size may differ from the source's and
// still be "comparable" for the estimate (design D4).
const drillSizeTolerance = 0.20

// drillFacts is what one server says about itself.
type drillFacts struct {
	Reached  bool
	Err      string // why it was not reached
	Endpoint string // host:port from the connection, never credentials

	SystemID        string
	InRecovery      bool
	WalReceivers    int
	PrimaryConninfo string
	ArchiveMode     string
	ArchiveCommand  string
	ArchiveLibrary  string
	DBBytes         int64
	ServerAddr      string // inet_server_addr:inet_server_port as the server sees itself
}

// drillInput is everything the verdict is made from.
type drillInput struct {
	Source, Clone drillFacts
	// VerifySQL is nil when none was supplied. VerifyRan is false when it was
	// supplied but could not be run.
	VerifySQL *string
	VerifyOK  bool
	VerifyErr string
}

var stanzaRe = regexp.MustCompile(`--stanza[= ]\s*['"]?([^\s'"]+)`)

func stanzaOf(cmd string) string {
	if m := stanzaRe.FindStringSubmatch(cmd); m != nil {
		return m[1]
	}
	return ""
}

func check(name, result, detail string) audit.DrillCheck {
	return audit.DrillCheck{Name: name, Result: result, Detail: detail}
}

// assessDrill makes the verdict. It is pure: every decision here is a table
// test, and the guards err toward refusing (a refusal costs only a missing
// sample, a wrong pass pollutes the source's archive).
func assessDrill(in drillInput) (string, []audit.DrillCheck) {
	var checks []audit.DrillCheck

	if !in.Clone.Reached || !in.Source.Reached {
		who, err := "the clone", in.Clone.Err
		if !in.Source.Reached {
			who, err = "the source", in.Source.Err
		}
		return audit.DrillUnverifiable, []audit.DrillCheck{check("reachable", "fail", fmt.Sprintf("%s could not be reached: %s", who, err))}
	}
	checks = append(checks, check("reachable", "pass", ""))

	// 1. The same system: this is the proof of the link. The keys in infra.json
	// are only labels.
	switch {
	case in.Source.SystemID == "" || in.Clone.SystemID == "":
		checks = append(checks, check("same_system", "fail", "a system identifier could not be read"))
	case in.Source.SystemID != in.Clone.SystemID:
		checks = append(checks, check("same_system", "fail", fmt.Sprintf("the clone is not a copy of the source (system identifier %s, source %s)", in.Clone.SystemID, in.Source.SystemID)))
	default:
		checks = append(checks, check("same_system", "pass", "system identifier "+in.Source.SystemID))
	}

	// 2. Not the source, and not a replica of it.
	checks = append(checks, notTheSource(in))

	// 3. The archive guard.
	checks = append(checks, archiveGuard(in))

	// 4. Consistent and out of recovery.
	switch {
	case in.Clone.InRecovery && in.Clone.WalReceivers > 0:
		checks = append(checks, check("consistent", "fail", "the clone is replaying WAL from a primary"))
	case in.Clone.InRecovery:
		// A paused restore and a standby look alike to a role that cannot see
		// the wal receiver, so this fails closed: promote the clone and record
		// the drill again.
		checks = append(checks, check("consistent", "fail", "the clone is still in recovery (paused at a target, not promoted yet, or a standby); promote it and record the drill again"))
	default:
		checks = append(checks, check("consistent", "pass", "out of recovery"))
	}

	// 5. Size class. Outside it a drill is recorded as proof of recoverability
	// and not counted in the estimate; it is not a refusal.
	checks = append(checks, sizeClass(in))

	// 6. The operator's own check.
	if in.VerifySQL != nil {
		switch {
		case in.VerifyErr != "":
			checks = append(checks, check("verify_sql", "fail", "could not run: "+in.VerifyErr))
		case in.VerifyOK:
			checks = append(checks, check("verify_sql", "pass", ""))
		default:
			checks = append(checks, check("verify_sql", "fail", "returned false"))
		}
	}

	status := audit.DrillVerified
	for _, c := range checks {
		if c.Result == "fail" {
			return audit.DrillRefused, checks
		}
		if c.Result == "warn" {
			status = audit.DrillVerifiedWithWarnings
		}
	}
	return status, checks
}

func notTheSource(in drillInput) audit.DrillCheck {
	s, c := in.Source, in.Clone
	if s.Endpoint != "" && strings.EqualFold(s.Endpoint, c.Endpoint) {
		return check("not_the_source", "fail", "the clone is reached at the source's address "+c.Endpoint)
	}
	if s.ServerAddr != "" && s.ServerAddr == c.ServerAddr {
		return check("not_the_source", "fail", "both connections end at the same server ("+c.ServerAddr+")")
	}
	if c.InRecovery && c.WalReceivers > 0 {
		return check("not_the_source", "fail", "the clone is a standby streaming from a primary, not a restored copy")
	}
	if host, port, ok := strings.Cut(s.Endpoint, ":"); ok && c.PrimaryConninfo != "" &&
		strings.Contains(c.PrimaryConninfo, "host="+host) && strings.Contains(c.PrimaryConninfo, "port="+port) {
		return check("not_the_source", "fail", "the clone's primary_conninfo points at the source")
	}
	return check("not_the_source", "pass", "")
}

// archiveGuard refuses a clone that could push its own timeline's WAL and
// history files into the source's pgBackRest repository. Same system
// identifier, so pgBackRest would accept them, and a later PITR of the source
// could follow or collide with them (design section 5).
func archiveGuard(in drillInput) audit.DrillCheck {
	c := in.Clone
	mode := strings.ToLower(strings.TrimSpace(c.ArchiveMode))
	switch mode {
	case "off":
		return check("archive_guard", "pass", "archive_mode is off")
	case "on", "always":
	default:
		return check("archive_guard", "fail", fmt.Sprintf("archive_mode could not be read (%q)", c.ArchiveMode))
	}
	cmd := strings.TrimSpace(c.ArchiveCommand)
	lib := strings.TrimSpace(c.ArchiveLibrary)
	if cmd == "" && lib == "" {
		return check("archive_guard", "warn", "archive_mode is on but there is no archive command; nothing is pushed, but WAL will pile up")
	}
	cs, ss := stanzaOf(cmd), stanzaOf(in.Source.ArchiveCommand)
	if cs != "" && ss != "" && cs != ss {
		return check("archive_guard", "warn", fmt.Sprintf("archive_mode is on, but the command names stanza %q, not the source's %q", cs, ss))
	}
	what := "an archive command"
	if cmd == "" {
		what = "an archive library"
	}
	return check("archive_guard", "fail", fmt.Sprintf("archive_mode is %s and the clone has %s that may push into the source's archive; set archive_mode=off on the clone (pgBackRest restore --archive-mode=off) and record the drill again", mode, what))
}

func sizeClass(in drillInput) audit.DrillCheck {
	s, c := in.Source.DBBytes, in.Clone.DBBytes
	if s <= 0 || c <= 0 {
		return check("size_class", "warn", "a database size could not be read; the drill is not counted in the estimate")
	}
	diff := float64(c-s) / float64(s)
	if diff < 0 {
		diff = -diff
	}
	detail := fmt.Sprintf("clone %s, source %s", humanBytes(c), humanBytes(s))
	if diff > drillSizeTolerance {
		return check("size_class", "warn", fmt.Sprintf("%s: outside the %.0f%% that counts as comparable; recorded as proof of recovery, not counted in the estimate", detail, drillSizeTolerance*100))
	}
	return check("size_class", "pass", detail)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// drillFactsQuery is run, read-only, on both servers. current_setting(.., true)
// returns NULL for a setting this version does not have. primary_conninfo is
// read from pg_settings: current_setting() raises "permission denied" for a role
// without pg_read_all_settings, pg_settings just shows it empty. Neither the
// conninfo nor the wal receiver may be relied on to prove a clone is not a
// standby, so being in recovery is itself a refusal (see assessDrill).
const drillFactsQuery = `SELECT
	(SELECT system_identifier::text FROM pg_control_system()) AS system_identifier,
	pg_is_in_recovery() AS in_recovery,
	(SELECT count(*) FROM pg_stat_wal_receiver) AS wal_receivers,
	coalesce((SELECT setting FROM pg_settings WHERE name = 'primary_conninfo'), '') AS primary_conninfo,
	current_setting('archive_mode') AS archive_mode,
	coalesce(current_setting('archive_command', true), '') AS archive_command,
	coalesce(current_setting('archive_library', true), '') AS archive_library,
	(SELECT coalesce(sum(pg_database_size(oid)), 0) FROM pg_database)::bigint AS db_bytes,
	coalesce(inet_server_addr()::text, 'socket') || ':' || coalesce(inet_server_port()::text, '') AS server_addr;`

func parseDrillFacts(output string) (drillFacts, error) {
	statements := psqlx.ParseExpanded(output)
	if len(statements) == 0 || len(statements[0]) == 0 {
		return drillFacts{}, fmt.Errorf("no rows returned")
	}
	row := statements[0][0]
	f := drillFacts{
		Reached:         true,
		SystemID:        row["system_identifier"],
		InRecovery:      row["in_recovery"] == "t",
		PrimaryConninfo: row["primary_conninfo"],
		ArchiveMode:     row["archive_mode"],
		ArchiveCommand:  row["archive_command"],
		ArchiveLibrary:  row["archive_library"],
		ServerAddr:      row["server_addr"],
	}
	f.WalReceivers, _ = strconv.Atoi(row["wal_receivers"])
	f.DBBytes = parseInt64Field(row["db_bytes"])
	if f.SystemID == "" {
		return drillFacts{}, fmt.Errorf("the server did not report a system identifier")
	}
	return f, nil
}

// endpointOf is host:port from a (resolved) connection string, never the
// credentials. It is for the record, so a person can see which servers it was.
func endpointOf(connStr string) string {
	f := connStrCoreFields(connStr)
	host, port := f["host"], f["port"]
	if host == "" {
		host = "localhost"
	}
	if port == "" {
		port = "5432"
	}
	return host + ":" + port
}

// verifySQLAllowed is deliberately narrow: one SELECT, no statement separator.
// The SQL runs on the clone, wrapped as a scalar subquery, and must be true.
func verifySQLAllowed(sql string) error {
	s := strings.TrimSpace(sql)
	switch {
	case s == "":
		return fmt.Errorf("verify_sql is empty")
	case len(s) > 500:
		return fmt.Errorf("verify_sql is longer than 500 characters")
	case strings.Contains(s, ";"):
		return fmt.Errorf("verify_sql must be a single statement (no ';')")
	case !strings.HasPrefix(strings.ToLower(s), "select"):
		return fmt.Errorf("verify_sql must be a SELECT that returns true or false")
	}
	return nil
}

// VerifyRestoreDrillArgs defines arguments for the verify_restore_drill tool.
type VerifyRestoreDrillArgs struct {
	Source    string `json:"source" jsonschema:"The source database: its name in the infrastructure config, or its connection string."`
	Clone     string `json:"clone" jsonschema:"The clone the restore drill produced: its name in the infrastructure config, or its connection string. Must not be the source."`
	VerifySQL string `json:"verify_sql,omitempty" jsonschema:"Optional single SELECT, run on the clone, that must return true (for example a row count you expect)."`
}

// VerifyRestoreDrillResult is the structured result of verify_restore_drill.
// It is defined in internal/audit because the gateway reads it too.
type VerifyRestoreDrillResult = audit.DrillVerification

func readDrillFacts(ctx context.Context, connStr, key string) drillFacts {
	out, err := runPsqlWithToolName(ctx, key, drillFactsQuery, "verify_restore_drill")
	if err != nil {
		return drillFacts{Err: err.Error()}
	}
	f, err := parseDrillFacts(out)
	if err != nil {
		return drillFacts{Err: err.Error()}
	}
	f.Endpoint = endpointOf(connStr)
	return f
}

func verifyRestoreDrillImpl(ctx context.Context, args VerifyRestoreDrillArgs) (VerifyRestoreDrillResult, error) {
	if strings.TrimSpace(args.Source) == "" || strings.TrimSpace(args.Clone) == "" {
		return VerifyRestoreDrillResult{Output: "verify_restore_drill needs both source and clone."}, nil
	}
	srcInfo, err := resolveDatabaseInfo(args.Source)
	if err != nil {
		return VerifyRestoreDrillResult{Output: "The source cannot be used: " + err.Error()}, nil
	}
	cloneInfo, err := resolveDatabaseInfo(args.Clone)
	if err != nil {
		// Both servers must be registered: the clone is a new infra entry, and
		// adding it is the customer's step, like bringing the clone itself.
		return VerifyRestoreDrillResult{Output: "The clone cannot be used: " + err.Error() +
			"\nThe clone has to be registered in the infrastructure config before a drill can be recorded against it."}, nil
	}
	res := VerifyRestoreDrillResult{
		SourceKey: srcInfo.Name, CloneKey: cloneInfo.Name,
		SourceEndpoint: endpointOf(srcInfo.ConnectionStr), CloneEndpoint: endpointOf(cloneInfo.ConnectionStr),
	}
	// The clone is never the source: refuse before touching either server.
	// Name is only an identity when the infra config gave it; for a bare
	// connection string it is the dbname, which two servers share.
	sameEntry := srcInfo.IsFromInfraConfig && cloneInfo.IsFromInfraConfig && srcInfo.Name == cloneInfo.Name
	if sameEntry || strings.EqualFold(res.SourceEndpoint, res.CloneEndpoint) {
		res.Status = audit.DrillRefused
		res.Checks = []audit.DrillCheck{check("not_the_source", "fail", "the clone is the source ("+res.CloneEndpoint+")")}
		res.Output = formatDrillVerdict(res)
		return res, nil
	}
	if args.VerifySQL != "" {
		if err := verifySQLAllowed(args.VerifySQL); err != nil {
			return VerifyRestoreDrillResult{Output: err.Error()}, nil
		}
	}

	in := drillInput{
		Source: readDrillFacts(ctx, srcInfo.ConnectionStr, args.Source),
		Clone:  readDrillFacts(ctx, cloneInfo.ConnectionStr, args.Clone),
	}
	if args.VerifySQL != "" && in.Clone.Reached {
		in.VerifySQL = &args.VerifySQL
		out, err := runPsqlWithToolName(ctx, args.Clone, "SELECT ("+strings.TrimSpace(args.VerifySQL)+") AS verify_ok;", "verify_restore_drill")
		switch {
		case err != nil:
			in.VerifyErr = err.Error()
		default:
			if st := psqlx.ParseExpanded(out); len(st) > 0 && len(st[0]) > 0 {
				in.VerifyOK = st[0][0]["verify_ok"] == "t"
			}
		}
	}
	res.Status, res.Checks = assessDrill(in)
	res.SourceSystemID, res.CloneSystemID = in.Source.SystemID, in.Clone.SystemID
	res.SourceBytes, res.CloneBytes = in.Source.DBBytes, in.Clone.DBBytes
	res.Output = formatDrillVerdict(res)
	return res, nil
}

func formatDrillVerdict(r VerifyRestoreDrillResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Restore drill of %s on %s: %s\n", r.SourceKey, r.CloneKey, strings.ToUpper(strings.ReplaceAll(r.Status, "_", " ")))
	for _, c := range r.Checks {
		line := fmt.Sprintf("  %-4s %s", strings.ToUpper(c.Result), c.Name)
		if c.Detail != "" {
			line += ": " + c.Detail
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func verifyRestoreDrillTool(ctx agent.ToolContext, args VerifyRestoreDrillArgs) (VerifyRestoreDrillResult, error) {
	return verifyRestoreDrillImpl(ctx, args)
}
