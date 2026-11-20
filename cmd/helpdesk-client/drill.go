package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"helpdesk/internal/audit"
	"helpdesk/internal/client"
)

// Restore drills from the operator's seat (v0.36).
//
//	--drill-record  record a drill: the gateway verifies the clone (read-only on
//	                both servers) and stores the record, whatever the verdict
//	--drill-list    list the recorded drills
//
// A drill is a restore of the source's backup onto a clone you brought; the
// clone must be registered in the infrastructure config. Recording one says
// what you did, and aiHelpDesk checks the clone is what you say it is. The
// duration is your word ("reported"); the size is measured on the clone.
//
// A refused or unverifiable drill is recorded too, so the trail shows it, but
// it does not count; the command exits non-zero for those so a script notices.

// drillOpts are the flag values of both commands.
type drillOpts struct {
	Source, Clone, Method string
	Started, Finished     string // RFC3339
	Duration              string // a Go duration: 90s, 1m35s, 2h
	BackupLabel           string
	RecoveryTarget        string
	VerifySQL             string
	Ticket, Note          string
	Supersedes            string
	// list
	Status  string
	Current bool
	Since   string
	Limit   int
}

// drillFlags are the drill flags as registered on a FlagSet. Registering them
// here, not inline in main, is what makes the wiring testable: a test parses a
// command line and reads the options back, so a flag tied to the wrong field, a
// wrong default or a duplicate name shows up as a failure and not as a client
// that records the wrong thing (or panics at startup).
type drillFlags struct {
	record, list                                 *bool
	source, clone, method                        *string
	started, finished, duration                  *string
	backupLabel, recoveryTarget, verifySQL, note *string
	supersedes, status                           *string
	current                                      *bool
	ticket                                       *string // shared with --pitr-approve, defined by the caller
}

func registerDrillFlags(fs *flag.FlagSet, ticket *string) *drillFlags {
	return &drillFlags{
		record:         fs.Bool("drill-record", false, "Record a restore drill: the gateway verifies the clone and stores the record (needs --drill-source, --drill-clone, --drill-method)"),
		list:           fs.Bool("drill-list", false, "List the recorded restore drills (filter with --drill-source, --drill-status, --drill-current)"),
		source:         fs.String("drill-source", "", "Database the drill is for: its infrastructure key or name (for --drill-record, --drill-list)"),
		clone:          fs.String("drill-clone", "", "The clone the drill produced, registered in the infrastructure config (for --drill-record)"),
		method:         fs.String("drill-method", "", "How the clone was made: pgbackrest, snapshot, cloud or other (for --drill-record)"),
		started:        fs.String("drill-started", "", "When the restore started, RFC3339 with a zone (for --drill-record)"),
		finished:       fs.String("drill-finished", "", "When the restore finished, RFC3339 with a zone (for --drill-record)"),
		duration:       fs.String("drill-duration", "", "How long the restore took, e.g. 1m35s; ignored when both times are given. Time the restore, the start and your check, not provisioning the clone or waiting on people (docs/RESTORE_OPS.md) (for --drill-record)"),
		backupLabel:    fs.String("drill-backup-label", "", "The pgBackRest backup restored from (for --drill-record)"),
		recoveryTarget: fs.String("drill-recovery-target", "", "The recovery target restored to, if a point-in-time drill (for --drill-record)"),
		verifySQL:      fs.String("drill-verify-sql", "", "A single SELECT run on the clone that must return true (for --drill-record)"),
		note:           fs.String("drill-note", "", "Free text recorded with the drill (for --drill-record)"),
		supersedes:     fs.String("drill-supersedes", "", "The earlier drill `ID` this one replaces, e.g. after fixing a refusal (for --drill-record)"),
		status:         fs.String("drill-status", "", "Only drills with this status: verified, verified_with_warnings, refused, unverifiable (for --drill-list)"),
		current:        fs.Bool("drill-current", false, "Leave out drills a later record supersedes (for --drill-list)"),
		ticket:         ticket,
	}
}

// requested reports whether a drill command was asked for.
func (f *drillFlags) requested() bool { return *f.record || *f.list }

func (f *drillFlags) opts() drillOpts {
	return drillOpts{
		Source: *f.source, Clone: *f.clone, Method: *f.method,
		Started: *f.started, Finished: *f.finished, Duration: *f.duration,
		BackupLabel: *f.backupLabel, RecoveryTarget: *f.recoveryTarget, VerifySQL: *f.verifySQL,
		Ticket: *f.ticket, Note: *f.note, Supersedes: *f.supersedes,
		Status: *f.status, Current: *f.current,
	}
}

// recordBody validates what can be checked here and builds the request body.
// The gateway checks it all again.
func (o drillOpts) recordBody() ([]byte, error) {
	switch {
	case strings.TrimSpace(o.Source) == "":
		return nil, fmt.Errorf("--drill-source is required: the database the drill is for")
	case strings.TrimSpace(o.Clone) == "":
		return nil, fmt.Errorf("--drill-clone is required: the clone, registered in the infrastructure config")
	case o.Method == "":
		return nil, fmt.Errorf("--drill-method is required: pgbackrest, snapshot, cloud or other")
	}
	body := map[string]any{"source": strings.TrimSpace(o.Source), "clone": strings.TrimSpace(o.Clone), "method": o.Method}
	for k, v := range map[string]string{
		"backup_label": o.BackupLabel, "recovery_target": o.RecoveryTarget, "verify_sql": o.VerifySQL,
		"ticket": strings.TrimSpace(o.Ticket), "note": o.Note, "supersedes": o.Supersedes,
	} {
		if v != "" {
			body[k] = v
		}
	}
	for flagName, k := range map[string]string{"--drill-started": "started_at", "--drill-finished": "finished_at"} {
		v := o.Started
		if k == "finished_at" {
			v = o.Finished
		}
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, fmt.Errorf("%s must be an RFC3339 time with a zone (2026-11-19T02:05:00Z): %v", flagName, err)
		}
		body[k] = t.UTC().Format(time.RFC3339)
	}
	if o.Duration != "" {
		d, err := time.ParseDuration(o.Duration)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("--drill-duration must be a positive duration such as 90s, 1m35s or 2h")
		}
		body["duration_seconds"] = int64(d.Seconds())
	}
	return json.Marshal(body)
}

type drillRecordResponse struct {
	Drill        audit.DrillRecord `json:"drill"`
	Verification string            `json:"verification"`
}

func runDrillRecord(ctx context.Context, cfg client.Config, o drillOpts) error {
	body, err := o.recordBody()
	if err != nil {
		return err
	}
	resp, err := doAuditRequest(ctx, cfg, http.MethodPost, strings.TrimRight(cfg.GatewayURL, "/")+"/api/v1/drills", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("the gateway did not record the drill (HTTP %d): %s", resp.StatusCode, gatewayError(raw))
	}
	var r drillRecordResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("unreadable answer from the gateway: %v", err)
	}
	fmt.Print(formatDrillRecorded(r, drillWrapWidth()))
	if !r.Drill.Counted() {
		return fmt.Errorf("drill %s was recorded as %s and does not count", r.Drill.DrillID, strings.ReplaceAll(r.Drill.Status, "_", " "))
	}
	return nil
}

// drillWrapWidth is the line width output is wrapped to: the terminal's, or 100
// when stdout is not one.
func drillWrapWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w >= 60 {
		return w - 1
	}
	return 100
}

// wrapHanging wraps one line at word boundaries to width, starting every
// continuation line at column hang. width 0 leaves the line alone, and a word
// longer than the room is never split.
func wrapHanging(line string, width, hang int) string {
	if width <= 0 || len(line) <= width {
		return line
	}
	// Runs of spaces are kept as they are (a label may be padded to align), except
	// the one at a break, which becomes the newline and the hanging indent.
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
			if col == 0 { // leading indentation
				b.WriteString(run)
				col = len(run)
				continue
			}
			gap = run
			continue
		}
		if gap != "" && col+len(gap)+len(run) > width {
			b.WriteString("\n" + strings.Repeat(" ", hang))
			col = hang
		} else {
			b.WriteString(gap)
			col += len(gap)
		}
		gap = ""
		b.WriteString(run)
		col += len(run)
	}
	return b.String()
}

// wrapVerification wraps the verdict lines the tool printed. A check line is
// "  FAIL name: detail"; its continuation lines start under the name, two columns in.
func wrapVerification(text string, width int) string {
	// The verdict is text that came from a tool reading servers, printed to the
	// operator's terminal: whatever acts on a terminal is made harmless first.
	text = audit.PrintableText(text, true)
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		hang := 4
		if f := strings.Fields(l); len(f) > 1 && strings.HasPrefix(l, "  ") {
			hang = len(l) - len(strings.TrimLeft(l, " ")) + len(f[0]) + 1 + 2
		}
		lines[i] = wrapHanging(l, width, hang)
	}
	return strings.Join(lines, "\n")
}

// formatDrillRecorded is what the operator reads after recording. width 0
// leaves lines unwrapped.
func formatDrillRecorded(r drillRecordResponse, width int) string {
	d := r.Drill
	var b strings.Builder
	b.WriteString(wrapVerification(r.Verification, width) + "\n\n")
	fmt.Fprintf(&b, "Recorded as %s (%s)\n", d.DrillID, strings.ReplaceAll(d.Status, "_", " "))
	if d.Ticket != "" {
		fmt.Fprintf(&b, "  ticket   %s\n", d.Ticket)
	}
	if d.DurationSeconds > 0 {
		fmt.Fprintf(&b, "  took     %s (%s)\n", drillDur(d.DurationSeconds), orDefault(d.DurationSource, "reported"))
	}
	if d.RestoreBytes > 0 {
		fmt.Fprintf(&b, "  size     %s measured on the clone\n", drillSize(d.RestoreBytes))
	}
	switch {
	case d.Counted():
		b.WriteString("  counts   yes: a verified drill\n")
	default:
		// The flag stays whole on a line of its own: it is meant to be copied.
		b.WriteString(wrapHanging("  counts   no: fix what the checks above name, then record the drill again with", width, 11) + "\n")
		b.WriteString("           --drill-supersedes " + d.DrillID + "\n")
	}
	return b.String()
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func drillDur(sec int64) string {
	d := time.Duration(sec) * time.Second
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %02dm %02ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func drillSize(n int64) string {
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

func runDrillList(ctx context.Context, cfg client.Config, o drillOpts) error {
	q := url.Values{}
	for k, v := range map[string]string{"source": o.Source, "status": o.Status, "since": o.Since} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if o.Current {
		q.Set("current", "true")
	}
	if o.Limit > 0 {
		q.Set("limit", fmt.Sprint(o.Limit))
	}
	u := strings.TrimRight(cfg.GatewayURL, "/") + "/api/v1/drills"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	resp, err := doAuditRequest(ctx, cfg, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the gateway could not list the drills (HTTP %d): %s", resp.StatusCode, gatewayError(raw))
	}
	var drills []audit.DrillRecord
	if err := json.Unmarshal(raw, &drills); err != nil {
		return fmt.Errorf("unreadable answer from the gateway: %v", err)
	}
	fmt.Print(formatDrillList(drills, time.Now()))
	return nil
}

// formatDrillList is one line per drill, newest first as the gateway sent them.
func formatDrillList(drills []audit.DrillRecord, now time.Time) string {
	if len(drills) == 0 {
		return "No restore drills recorded.\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-13s %-12s %-30s %-10s %-9s %-10s %s\n", "DRILL", "WHEN", "SOURCE <- CLONE", "METHOD", "TOOK", "SIZE", "STATUS")
	for _, d := range drills {
		when := d.FinishedAt
		if when.IsZero() {
			when = d.RecordedAt
		}
		took, size := "-", "-"
		if d.DurationSeconds > 0 {
			took = drillDur(d.DurationSeconds)
		}
		if d.RestoreBytes > 0 {
			size = drillSize(d.RestoreBytes)
		}
		line := fmt.Sprintf("%-13s %-12s %-30s %-10s %-9s %-10s %s", d.DrillID, drillAgo(now.Sub(when)),
			d.SourceKey+" <- "+d.CloneKey, d.Method, took, size, strings.ReplaceAll(d.Status, "_", " "))
		if d.Ticket != "" {
			line += "  " + d.Ticket
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func drillAgo(d time.Duration) string {
	switch {
	case d < 0:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
