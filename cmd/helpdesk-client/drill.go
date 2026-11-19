package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

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
	fmt.Print(formatDrillRecorded(r))
	if !r.Drill.Counted() {
		return fmt.Errorf("drill %s was recorded as %s and does not count", r.Drill.DrillID, strings.ReplaceAll(r.Drill.Status, "_", " "))
	}
	return nil
}

// formatDrillRecorded is what the operator reads after recording.
func formatDrillRecorded(r drillRecordResponse) string {
	d := r.Drill
	var b strings.Builder
	b.WriteString(strings.TrimRight(r.Verification, "\n") + "\n\n")
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
		b.WriteString("  counts   no: fix what the checks above name and record the drill again (--drill-supersedes " + d.DrillID + ")\n")
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
