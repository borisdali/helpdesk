package main

// `faulttest vault drills [drl_…]`: the restore drills recorded for a database
// (helpdesk-client --drill-record), through the gateway's GET /api/v1/drills.
//
//	vault drills                    the recent drills, newest first
//	vault drills --target KEY       one database's (a key, a name, or host:port)
//	vault drills drl_ab12cd34       one drill in full: what was checked, and what replaced it
//
// A drill is a restore of the source's backup on a clone the operator brought; it
// is on record whatever the verdict, but only a verified one counts. The listing
// hides a drill that a later record supersedes (a refusal put right) unless asked
// to show it. Formatting is pure, so it is checked without a gateway.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"helpdesk/internal/audit"
)

// drillsOptions are the flags of `vault drills` beyond the shared harness ones.
type drillsOptions struct {
	target string
	status string
	all    bool
	days   int
	limit  int
}

// parseDrillsArgs registers the vault drills flags next to the shared harness
// flags (loadConfig) and parses them. It is its own function so a test can parse
// the real flag set: a name the harness already owns (--source is the catalog
// filter) panics at registration, which only parsing shows.
func parseDrillsArgs(args []string) (*HarnessConfig, drillsOptions, []string) {
	fs := flag.NewFlagSet("vault drills", flag.ExitOnError)
	var o drillsOptions
	fs.StringVar(&o.target, "target", "", "Only this database's drills (its infrastructure key, name or host:port)")
	fs.StringVar(&o.status, "status", "", "Only drills with this status: verified, verified_with_warnings, refused, unverifiable")
	fs.BoolVar(&o.all, "all", false, "Include drills that a later record supersedes")
	fs.IntVar(&o.days, "days", 0, "Only drills from the last N days (0 for all)")
	fs.IntVar(&o.limit, "limit", 30, "Maximum number of drills to show")
	cfg := loadConfig(fs, args)
	return cfg, o, fs.Args()
}

func vaultDrills(args []string) {
	cfg, o, rest := parseDrillsArgs(args)
	if cfg.GatewayURL == "" {
		fmt.Fprintln(os.Stderr, "Error: --gateway URL is required for vault drills")
		os.Exit(1)
	}
	if len(rest) > 1 {
		fmt.Fprintln(os.Stderr, "Usage: faulttest vault drills [--target KEY] [--status S] [--all] [--days N] [drl_…]")
		os.Exit(1)
	}
	id := ""
	if len(rest) == 1 {
		id = rest[0]
	}
	out, err := drillsReport(cfg.GatewayURL, cfg.GatewayAPIKey, id, o.target, o.status, o.all, o.days, o.limit, time.Now(), drillWidth())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(out)
}

// drillsReport is what `vault drills` prints: the listing, or, with an id, that
// one drill in full. It does the reading and the formatting and leaves printing
// and exiting to the caller, so the whole command can be checked against a fake
// gateway.
func drillsReport(gatewayURL, apiKey, id, source, status string, all bool, days, limit int, now time.Time, width int) (string, error) {
	if id == "" {
		drills, err := fetchDrills(gatewayURL, apiKey, drillsQuery(source, status, all, days, limit, now))
		if err != nil {
			return "", err
		}
		return formatDrillTable(drills, now), nil
	}
	d, err := fetchDrill(gatewayURL, apiKey, id)
	if err != nil {
		return "", err
	}
	// What replaced it, when something did: the database's drills are read for it,
	// and a failure to read them leaves the drill shown without that line.
	var later []audit.DrillRecord
	if mine, err := fetchDrills(gatewayURL, apiKey, neturl.Values{"source": {d.SourceKey}, "limit": {"200"}}); err == nil {
		later = mine
	}
	return formatDrillDetail(*d, later, width), nil
}

// drillsQuery is what the listing asks the gateway for. A drill that a later
// record supersedes is left out unless all is set; days limits how far back.
func drillsQuery(source, status string, all bool, days, limit int, now time.Time) neturl.Values {
	q := neturl.Values{}
	if s := strings.TrimSpace(source); s != "" {
		q.Set("source", s)
	}
	if status != "" {
		q.Set("status", status)
	}
	if !all {
		q.Set("current", "true")
	}
	if days > 0 {
		q.Set("since", now.UTC().Add(-time.Duration(days)*24*time.Hour).Format(time.RFC3339))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return q
}

// drillWidth is the width the detail view wraps to: the terminal's, or 100 when
// stdout is not one.
func drillWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w >= 60 {
		return w - 1
	}
	return 100
}

func gatewayGet(gatewayURL, apiKey, path string, q neturl.Values) ([]byte, int, error) {
	u := strings.TrimSuffix(gatewayURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return body, resp.StatusCode, nil
}

func fetchDrills(gatewayURL, apiKey string, q neturl.Values) ([]audit.DrillRecord, error) {
	body, code, err := gatewayGet(gatewayURL, apiKey, "/api/v1/drills", q)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("gateway returned %d: %s", code, strings.TrimSpace(string(body)))
	}
	var out []audit.DrillRecord
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return out, nil
}

func fetchDrill(gatewayURL, apiKey, id string) (*audit.DrillRecord, error) {
	body, code, err := gatewayGet(gatewayURL, apiKey, "/api/v1/drills/"+neturl.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	switch code {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("no restore drill %q is recorded", id)
	default:
		return nil, fmt.Errorf("gateway returned %d: %s", code, strings.TrimSpace(string(body)))
	}
	var d audit.DrillRecord
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &d, nil
}

// cleanDrill makes every text of a drill safe to print to a terminal. The gateway
// refuses control characters when a drill is recorded, but a record can come from
// another writer, and what is read back is printed to whoever reads it: an escape
// sequence in a note could rewrite their screen. Characters that act on a terminal
// become "?"; a note keeps its tabs and newlines, which lay it out and do nothing
// else. The caller's record is not changed.
func cleanDrill(d audit.DrillRecord) audit.DrillRecord {
	p := func(s string) string { return audit.PrintableText(s, false) }
	d.DrillID, d.SourceKey, d.CloneKey = p(d.DrillID), p(d.SourceKey), p(d.CloneKey)
	d.SourceSystemID, d.CloneSystemID = p(d.SourceSystemID), p(d.CloneSystemID)
	d.SourceEndpoint, d.CloneEndpoint, d.SourceHostType, d.Stanza = p(d.SourceEndpoint), p(d.CloneEndpoint), p(d.SourceHostType), p(d.Stanza)
	d.InfraDigest, d.Method, d.DurationSource = p(d.InfraDigest), p(d.Method), p(d.DurationSource)
	d.BackupLabel, d.RecoveryTarget, d.Status, d.Supersedes = p(d.BackupLabel), p(d.RecoveryTarget), p(d.Status), p(d.Supersedes)
	d.RecordedBy, d.Ticket, d.AuditEventID = p(d.RecordedBy), p(d.Ticket), p(d.AuditEventID)
	d.Note = audit.PrintableText(d.Note, true)
	checks := make([]audit.DrillCheck, len(d.Checks))
	for i, c := range d.Checks {
		checks[i] = audit.DrillCheck{Name: p(c.Name), Result: p(c.Result), Detail: audit.PrintableText(c.Detail, true)}
	}
	d.Checks = checks
	return d
}

func drillWord(status string) string { return strings.ReplaceAll(status, "_", " ") }

func drillDuration(sec int64) string {
	if sec <= 0 {
		return "-"
	}
	return rdSpan(sec)
}

func drillSizeText(n int64) string {
	const unit = 1024
	if n <= 0 {
		return "-"
	}
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

func drillAgo(d time.Duration) string {
	switch {
	case d < time.Hour:
		return "just now"
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func drillWhenOf(d audit.DrillRecord) time.Time {
	if !d.FinishedAt.IsZero() {
		return d.FinishedAt
	}
	return d.RecordedAt
}

// formatDrillTable lists drills, one line each, in the order given (newest first).
func formatDrillTable(drills []audit.DrillRecord, now time.Time) string {
	if len(drills) == 0 {
		return "No restore drills recorded.\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-13s %-9s %-30s %-10s %-9s %-10s %s\n", "DRILL", "WHEN", "SOURCE <- CLONE", "METHOD", "TOOK", "SIZE", "STATUS")
	notCounted := 0
	for _, d := range drills {
		d = cleanDrill(d)
		line := fmt.Sprintf("%-13s %-9s %-30s %-10s %-9s %-10s %s", d.DrillID, drillAgo(now.Sub(drillWhenOf(d))),
			d.SourceKey+" <- "+d.CloneKey, d.Method, drillDuration(d.DurationSeconds), drillSizeText(d.RestoreBytes), drillWord(d.Status))
		if d.Ticket != "" {
			line += "  " + d.Ticket
		}
		if d.Supersedes != "" {
			line += "  (replaces " + d.Supersedes + ")"
		}
		b.WriteString(line + "\n")
		if !d.Counted() {
			notCounted++
		}
	}
	fmt.Fprintf(&b, "\n%d drill(s)", len(drills))
	if notCounted > 0 {
		fmt.Fprintf(&b, ", %d of them refused or unverifiable and not counted", notCounted)
	}
	b.WriteString(".\n")
	b.WriteString("A drill resets the reminder when verified. It also plans a restore only when it is a pgBackRest drill\n" +
		"of comparable size; see `helpdesk-client --pitr-plan` for what the plan rests on.\n" +
		"Open one with `faulttest vault drills <drill id>`.\n")
	return b.String()
}

// formatDrillDetail is one drill in full. later is the database's drills, to find
// the one that replaced this (when one did).
func formatDrillDetail(d audit.DrillRecord, later []audit.DrillRecord, width int) string {
	d = cleanDrill(d)
	siblings := make([]audit.DrillRecord, len(later))
	for i, l := range later {
		siblings[i] = cleanDrill(l)
	}
	later = siblings
	var b strings.Builder
	row := func(label, text string) {
		const indent = "  " + "           "
		fmt.Fprintf(&b, "  %-10s %s\n", label, rdWrap(text, max(40, width-len(indent)), indent))
	}
	fmt.Fprintf(&b, "Restore drill %s  %s\n", d.DrillID, strings.ToUpper(drillWord(d.Status)))
	endpoint := func(key, ep, sys string) string {
		s := key
		var in []string
		if ep != "" {
			in = append(in, ep)
		}
		if sys != "" {
			in = append(in, "system "+sys)
		}
		if len(in) > 0 {
			s += " (" + strings.Join(in, ", ") + ")"
		}
		return s
	}
	row("Source", endpoint(d.SourceKey, d.SourceEndpoint, d.SourceSystemID))
	row("Clone", endpoint(d.CloneKey, d.CloneEndpoint, d.CloneSystemID))
	row("Method", d.Method)
	when := drillWhenOf(d).UTC().Format("2006-01-02 15:04 UTC")
	if !d.StartedAt.IsZero() && !d.FinishedAt.IsZero() {
		when = d.StartedAt.UTC().Format("2006-01-02 15:04") + " to " + d.FinishedAt.UTC().Format("15:04 UTC, 2006-01-02")
	}
	took := ""
	if d.DurationSeconds > 0 {
		src := d.DurationSource
		if src == "" {
			src = "reported"
		}
		took = fmt.Sprintf("; took %s (%s)", rdSpan(d.DurationSeconds), src)
	}
	row("When", when+took)
	if d.RestoreBytes > 0 {
		row("Size", drillSizeText(d.RestoreBytes)+" measured on the clone")
	}
	if d.BackupLabel != "" {
		row("Backup", d.BackupLabel)
	}
	if d.RecoveryTarget != "" {
		row("Target", d.RecoveryTarget)
	}
	rec := "recorded " + d.RecordedAt.UTC().Format("2006-01-02 15:04 UTC")
	if d.RecordedBy != "" {
		rec += " by " + d.RecordedBy
	}
	if d.Ticket != "" {
		rec += ", ticket " + d.Ticket
	}
	row("Recorded", rec)
	if d.Note != "" {
		row("Note", d.Note)
	}
	if d.Supersedes != "" {
		row("Replaces", d.Supersedes)
	}
	for _, l := range later {
		if l.Supersedes == d.DrillID {
			row("Replaced by", fmt.Sprintf("%s (%s)", l.DrillID, drillWord(l.Status)))
		}
	}
	if len(d.Checks) > 0 {
		b.WriteString("\n  Checks\n")
		for _, c := range d.Checks {
			line := fmt.Sprintf("    %-4s %s", strings.ToUpper(c.Result), c.Name)
			if c.Detail != "" {
				line += ": " + c.Detail
			}
			b.WriteString(wrapHangingLine(line, width, 11) + "\n")
		}
	}
	b.WriteString("\n")
	switch {
	case d.Counted():
		b.WriteString("  This drill is verified: it resets the drill reminder.\n")
	default:
		b.WriteString(wrapHangingLine("  This drill does not count: fix what the checks above name and record it again with", width, 2) + "\n" +
			"  helpdesk-client --drill-record ... --drill-supersedes " + d.DrillID + "\n")
	}
	if d.AuditEventID != "" || d.InfraDigest != "" {
		var trail []string
		if d.AuditEventID != "" {
			trail = append(trail, "audit event "+d.AuditEventID)
		}
		if d.InfraDigest != "" {
			trail = append(trail, "infra "+d.InfraDigest)
		}
		fmt.Fprintf(&b, "  Trail: %s\n", strings.Join(trail, "; "))
	}
	return b.String()
}

// wrapHangingLine wraps one line at word boundaries to width, continuing at
// column hang, and keeps runs of spaces as they are.
func wrapHangingLine(line string, width, hang int) string {
	if width <= 0 || len([]rune(line)) <= width {
		return line
	}
	var b strings.Builder
	col, gap := 0, ""
	for i := 0; i < len(line); {
		j := i
		isSpace := line[i] == ' '
		for j < len(line) && (line[j] == ' ') == isSpace {
			j++
		}
		run := line[i:j]
		i = j
		if isSpace {
			if col == 0 {
				b.WriteString(run)
				col = len(run)
				continue
			}
			gap = run
			continue
		}
		if gap != "" && col+len(gap)+len([]rune(run)) > width {
			b.WriteString("\n" + strings.Repeat(" ", hang))
			col = hang
		} else {
			b.WriteString(gap)
			col += len(gap)
		}
		gap = ""
		b.WriteString(run)
		col += len([]rune(run))
	}
	return b.String()
}
