package main

// `vault incidents --ticket <ref>`: the approvals that were given under a ticket,
// found through the audit trail's ticket filter, each with the run to open for the
// full incident. The ticket is what the approver typed; nothing here checks it
// against a ticket system.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"helpdesk/internal/audit"
)

// fetchApprovalsByTicket reads the recovery decisions recorded under a ticket.
func fetchApprovalsByTicket(gatewayURL, apiKey, ticket string, limit int) ([]audit.Event, error) {
	q := neturl.Values{}
	q.Set("event_type", string(audit.EventTypeRecoveryDecision))
	q.Set("ticket", ticket)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(gatewayURL, "/")+"/api/v1/governance/events?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gateway returned %d: %s", resp.StatusCode, body)
	}
	var events []audit.Event
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return events, nil
}

// formatApprovalsByTicket lists the approvals given under a ticket, oldest first.
func formatApprovalsByTicket(ticket string, events []audit.Event) string {
	var rows []audit.Event
	for _, e := range events {
		if e.RecoveryDecision != nil && e.RecoveryDecision.Approval != nil && e.RecoveryDecision.Approval.Ticket == ticket {
			rows = append(rows, e)
		}
	}
	if len(rows) == 0 {
		return fmt.Sprintf("No approvals were given under ticket %s.\n", ticket)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Timestamp.Before(rows[j].Timestamp) })

	var b strings.Builder
	fmt.Fprintf(&b, "Approvals given under ticket %s (%d)\n\n", ticket, len(rows))
	fmt.Fprintf(&b, "%-17s  %-14s  %-24s  %s\n", "TIME (UTC)", "RUN", "APPROVED BY", "RECOVERY TARGET")
	b.WriteString(strings.Repeat("─", 78) + "\n")
	for _, e := range rows {
		d := e.RecoveryDecision
		who := d.DecidedBy
		if who == "" {
			who = d.Approval.ApprovedBy
		}
		if who == "" {
			who = "unknown"
		}
		run := d.RunID
		if run == "" {
			run = "–"
		}
		target := d.RecoveryTarget
		if target == "" {
			target = "–"
		}
		fmt.Fprintf(&b, "%-17s  %-14s  %-24s  %s\n", e.Timestamp.UTC().Format("2006-01-02 15:04"), run, who, target)
		if j := d.Approval.Justification; j != "" {
			fmt.Fprintf(&b, "%-17s  justification (the approver's words): %s\n", "", rdWrap(j, 60, strings.Repeat(" ", 59)))
		}
	}
	b.WriteString("\nThe ticket is what the approver typed; it is recorded, not checked against a ticket system.\n")
	b.WriteString("Open one with: faulttest vault incidents <run>\n")
	return b.String()
}

func vaultIncidentsByTicket(cfg *HarnessConfig, ticket string, limit int) {
	events, err := fetchApprovalsByTicket(cfg.GatewayURL, cfg.GatewayAPIKey, ticket, limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching approvals for ticket %s: %v\n", ticket, err)
		os.Exit(1)
	}
	fmt.Print(formatApprovalsByTicket(ticket, events))
}
