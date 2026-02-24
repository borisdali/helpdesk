// Command govbot is a governance compliance reporter that queries the Helpdesk
// gateway's governance API endpoints to produce a structured compliance
// snapshot. It is designed to run on-demand or on a schedule (e.g. daily cron)
// and optionally post a summary to a Slack webhook.
//
// Flow:
//
//	Gateway /api/v1/governance/* → govbot → compliance report + optional alert
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"helpdesk/internal/audit"
)

// ── Response types mirroring the gateway/auditd JSON shapes ──────────────────

type governanceInfo struct {
	Policy    *policyInfo    `json:"policy"`
	Approvals approvalConfig `json:"approvals"`
	Audit     auditStatus    `json:"audit"`
	Timestamp string         `json:"timestamp"`
}

type policyInfo struct {
	Enabled       bool            `json:"enabled"`
	File          string          `json:"file"`
	PoliciesCount int             `json:"policies_count"`
	RulesCount    int             `json:"rules_count"`
	Policies      []policySummary `json:"policies"`
}

type policySummary struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Enabled     bool          `json:"enabled"`
	Resources   []string      `json:"resources"`
	Rules       []ruleSummary `json:"rules"`
}

type ruleSummary struct {
	Actions    []string `json:"actions"`
	Effect     string   `json:"effect"`
	Message    string   `json:"message"`
	Conditions []string `json:"conditions"`
}

type approvalConfig struct {
	Enabled           bool   `json:"enabled"`
	WebhookConfigured bool   `json:"webhook_configured"`
	EmailConfigured   bool   `json:"email_configured"`
	DefaultTimeout    string `json:"default_timeout"`
	PendingCount      int    `json:"pending_count"`
}

type auditStatus struct {
	Enabled     bool   `json:"enabled"`
	EventsTotal int    `json:"events_total"`
	ChainValid  bool   `json:"chain_valid"`
	LastEventAt string `json:"last_event_at"`
}

type integrityStatus struct {
	Valid          bool   `json:"valid"`
	TotalEvents    int    `json:"total_events"`
	InvalidEventID string `json:"invalid_event_id,omitempty"`
	Message        string `json:"message,omitempty"`
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	gateway := flag.String("gateway", "http://localhost:8080", "Gateway base URL")
	sinceStr := flag.String("since", "24h", "Look-back window for audit events (e.g. 24h, 7d, 2w)")
	webhook := flag.String("webhook", "", "Slack webhook URL for posting report summary")
	dryRun := flag.Bool("dry-run", false, "Collect and print report but do not post to webhook")
	flag.Parse()

	since, err := parseLookback(*sinceStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid -since value %q: %v\n", *sinceStr, err)
		os.Exit(1)
	}

	logf("Gateway:   %s", *gateway)
	logf("Since:     last %s", *sinceStr)
	logf("Webhook:   %v", *webhook != "")
	logf("Dry run:   %v", *dryRun)
	fmt.Println()

	var alerts []string
	var warnings []string

	// ── Phase 1: Governance Status ────────────────────────────────────────────
	logPhase(1, "Governance Status")

	info, err := getGovernanceInfo(*gateway)
	if err != nil {
		logf("FATAL: %v", err)
		os.Exit(1)
	}

	logf("Audit enabled:    %v  (%d total events)", info.Audit.Enabled, info.Audit.EventsTotal)
	logf("Chain valid:      %v", info.Audit.ChainValid)
	if info.Audit.LastEventAt != "" {
		logf("Last event:       %s", info.Audit.LastEventAt)
	}
	if info.Policy != nil {
		logf("Policy enabled:   %v  (%d policies, %d rules)", info.Policy.Enabled, info.Policy.PoliciesCount, info.Policy.RulesCount)
	}
	logf("Pending approvals: %d", info.Approvals.PendingCount)
	logf("Approval notify:  webhook=%v  email=%v", info.Approvals.WebhookConfigured, info.Approvals.EmailConfigured)

	if !info.Audit.ChainValid {
		alerts = append(alerts, "Audit hash chain integrity failure — log tampering may have occurred")
	}
	fmt.Println()

	// ── Phase 2: Policy Overview ──────────────────────────────────────────────
	logPhase(2, "Policy Overview")

	if info.Policy == nil || !info.Policy.Enabled {
		logf("Policy enforcement is disabled")
		warnings = append(warnings, "Policy enforcement is disabled — all agent actions are uncontrolled")
	} else {
		for _, pol := range info.Policy.Policies {
			status := "enabled"
			if !pol.Enabled {
				status = "disabled"
			}
			logf("  [%s] %s", status, pol.Name)
			if pol.Description != "" {
				logf("       %s", pol.Description)
			}
			logf("       Resources: %s", strings.Join(pol.Resources, ", "))
			for _, r := range pol.Rules {
				cond := ""
				if len(r.Conditions) > 0 {
					cond = "  [" + strings.Join(r.Conditions, ", ") + "]"
				}
				logf("       %-30s → %s%s", strings.Join(r.Actions, ", "), r.Effect, cond)
			}
		}
	}
	fmt.Println()

	// ── Phase 3: Audit Activity ───────────────────────────────────────────────
	logPhase(3, fmt.Sprintf("Audit Activity (last %s)", *sinceStr))

	sinceTime := time.Now().Add(-since)
	events, err := getEvents(*gateway, sinceTime, 1000)
	if err != nil {
		logf("WARNING: Could not fetch events: %v", err)
		warnings = append(warnings, fmt.Sprintf("Failed to fetch audit events: %v", err))
	} else {
		logf("Events fetched:   %d", len(events))

		// Count by event type
		typeCounts := make(map[string]int)
		for _, e := range events {
			typeCounts[string(e.EventType)]++
		}
		types := make([]string, 0, len(typeCounts))
		for t := range typeCounts {
			types = append(types, t)
		}
		sort.Strings(types)
		for _, t := range types {
			logf("  %-30s %d", t, typeCounts[t])
		}
	}
	fmt.Println()

	// ── Phase 4: Policy Decision Analysis ────────────────────────────────────
	logPhase(4, "Policy Decision Analysis")

	if len(events) > 0 {
		type resourceStats struct {
			allow          int
			deny           int
			requireApproval int
			noMatch        int // policy_name is "" or "default" — misconfiguration
		}
		byResource := make(map[string]*resourceStats)
		totalAllow, totalDeny, totalReqApproval, totalNoMatch := 0, 0, 0, 0

		for i := range events {
			e := &events[i]
			if e.EventType != audit.EventTypePolicyDecision || e.PolicyDecision == nil {
				continue
			}
			pd := e.PolicyDecision
			key := pd.ResourceType + "/" + pd.ResourceName
			if byResource[key] == nil {
				byResource[key] = &resourceStats{}
			}
			rs := byResource[key]

			noMatch := pd.PolicyName == "" || pd.PolicyName == "default"
			switch pd.Effect {
			case "allow":
				rs.allow++
				totalAllow++
				if noMatch {
					rs.noMatch++
					totalNoMatch++
				}
			case "deny":
				rs.deny++
				totalDeny++
				if noMatch {
					rs.noMatch++
					totalNoMatch++
				}
			case "require_approval":
				rs.requireApproval++
				totalReqApproval++
			}
		}

		if len(byResource) == 0 {
			logf("No policy decisions recorded in this window")
		} else {
			logf("%-40s  %6s  %6s  %6s  %6s", "Resource", "allow", "deny", "req_apr", "no_match")
			logf("%s", strings.Repeat("─", 72))

			// Sort resources for stable output
			resources := make([]string, 0, len(byResource))
			for r := range byResource {
				resources = append(resources, r)
			}
			sort.Strings(resources)

			for _, res := range resources {
				rs := byResource[res]
				noMatchStr := ""
				if rs.noMatch > 0 {
					noMatchStr = fmt.Sprintf("%6d ⚠", rs.noMatch)
				} else {
					noMatchStr = fmt.Sprintf("%6d", rs.noMatch)
				}
				logf("%-40s  %6d  %6d  %6d  %s", truncate(res, 40), rs.allow, rs.deny, rs.requireApproval, noMatchStr)
			}

			logf("%s", strings.Repeat("─", 72))
			logf("%-40s  %6d  %6d  %6d  %6d", "TOTAL", totalAllow, totalDeny, totalReqApproval, totalNoMatch)
		}

		if totalNoMatch > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"%d policy decisions matched no rule (policy_name=default) — likely missing tags in infrastructure config",
				totalNoMatch,
			))
		}
	} else {
		logf("No events available for analysis")
	}
	fmt.Println()

	// ── Phase 5: Pending Approvals ────────────────────────────────────────────
	logPhase(5, "Pending Approvals")

	pending, err := getPendingApprovals(*gateway)
	if err != nil {
		logf("WARNING: Could not fetch pending approvals: %v", err)
		warnings = append(warnings, fmt.Sprintf("Failed to fetch pending approvals: %v", err))
	} else if len(pending) == 0 {
		logf("No pending approvals")
	} else {
		logf("%d pending approval(s):", len(pending))
		staleThreshold := 30 * time.Minute
		staleCount := 0
		for _, req := range pending {
			age := time.Since(req.RequestedAt).Round(time.Second)
			stale := ""
			if age > staleThreshold {
				stale = "  ⚠ STALE"
				staleCount++
			}
			logf("  [%s] %s  action=%s  age=%s%s",
				req.ApprovalID, req.ResourceName, req.ActionClass, age, stale)
			if req.RequestedBy != "" {
				logf("       requested_by: %s", req.RequestedBy)
			}
		}
		if staleCount > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"%d approval request(s) have been pending for over %s — approvers may not have been notified",
				staleCount, staleThreshold,
			))
		}
	}
	fmt.Println()

	// ── Phase 6: Chain Integrity ──────────────────────────────────────────────
	logPhase(6, "Chain Integrity")

	integrity, err := getChainIntegrity(*gateway)
	if err != nil {
		logf("WARNING: Could not verify chain: %v", err)
		warnings = append(warnings, fmt.Sprintf("Failed to verify audit chain: %v", err))
	} else {
		status := "✓ VALID"
		if !integrity.Valid {
			status = "✗ INVALID"
		}
		logf("Chain status:  %s", status)
		logf("Total events:  %d", integrity.TotalEvents)
		if integrity.InvalidEventID != "" {
			logf("First invalid: %s", integrity.InvalidEventID)
		}
		if integrity.Message != "" {
			logf("Message:       %s", integrity.Message)
		}
		if !integrity.Valid {
			alerts = append(alerts, fmt.Sprintf(
				"Audit chain integrity FAILED (first invalid event: %s) — possible log tampering",
				integrity.InvalidEventID,
			))
		}
	}
	fmt.Println()

	// ── Phase 7: Summary ──────────────────────────────────────────────────────
	logPhase(7, "Compliance Summary")

	overall := "✓ HEALTHY"
	if len(alerts) > 0 {
		overall = "✗ ALERTS"
	} else if len(warnings) > 0 {
		overall = "⚠ WARNINGS"
	}
	logf("Overall status: %s", overall)

	if len(alerts) > 0 {
		logf("ALERTS (%d):", len(alerts))
		for _, a := range alerts {
			logf("  [ALERT] %s", a)
		}
	}
	if len(warnings) > 0 {
		logf("Warnings (%d):", len(warnings))
		for _, w := range warnings {
			logf("  [WARN]  %s", w)
		}
	}
	if len(alerts) == 0 && len(warnings) == 0 {
		logf("No issues detected")
	}

	// Post to webhook if configured
	if *webhook != "" && !*dryRun {
		fmt.Println()
		logf("Posting summary to webhook...")
		if err := postWebhook(*webhook, overall, alerts, warnings, info, *sinceStr); err != nil {
			logf("WARNING: Failed to post webhook: %v", err)
		} else {
			logf("Webhook posted")
		}
	} else if *webhook != "" && *dryRun {
		logf("[DRY RUN] Would post webhook")
	}

	fmt.Println()
	logf("Done.")

	if len(alerts) > 0 {
		os.Exit(2) // Distinct exit code for alerts — useful in CI/cron
	}
}

// ── API helpers ───────────────────────────────────────────────────────────────

func getGovernanceInfo(gateway string) (*governanceInfo, error) {
	body, err := gatewayGET(gateway, "/api/v1/governance")
	if err != nil {
		return nil, err
	}
	var info governanceInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("decode governance info: %w", err)
	}
	return &info, nil
}

func getEvents(gateway string, since time.Time, limit int) ([]audit.Event, error) {
	path := fmt.Sprintf("/api/v1/governance/events?since=%s&limit=%d",
		url.QueryEscape(since.UTC().Format(time.RFC3339)), limit)
	body, err := gatewayGET(gateway, path)
	if err != nil {
		return nil, err
	}
	var events []audit.Event
	if err := json.Unmarshal(body, &events); err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}
	return events, nil
}

func getPendingApprovals(gateway string) ([]*audit.StoredApproval, error) {
	body, err := gatewayGET(gateway, "/api/v1/governance/approvals/pending")
	if err != nil {
		return nil, err
	}
	var approvals []*audit.StoredApproval
	if err := json.Unmarshal(body, &approvals); err != nil {
		return nil, fmt.Errorf("decode pending approvals: %w", err)
	}
	return approvals, nil
}

func getChainIntegrity(gateway string) (*integrityStatus, error) {
	body, err := gatewayGET(gateway, "/api/v1/governance/verify")
	if err != nil {
		return nil, err
	}
	var status integrityStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, fmt.Errorf("decode integrity status: %w", err)
	}
	return &status, nil
}

func gatewayGET(baseURL, path string) ([]byte, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(baseURL + path)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, string(body))
	}
	return body, nil
}

// ── Webhook ───────────────────────────────────────────────────────────────────

func postWebhook(webhookURL, overall string, alerts, warnings []string, info *governanceInfo, window string) error {
	icon := ":white_check_mark:"
	if len(alerts) > 0 {
		icon = ":rotating_light:"
	} else if len(warnings) > 0 {
		icon = ":warning:"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s *AI Governance Report* — %s\n", icon, overall))
	sb.WriteString(fmt.Sprintf("Window: last %s  |  Total events: %d  |  Pending approvals: %d  |  Chain: ",
		window, info.Audit.EventsTotal, info.Approvals.PendingCount))
	if info.Audit.ChainValid {
		sb.WriteString("✓ valid\n")
	} else {
		sb.WriteString("✗ *INVALID*\n")
	}

	if len(alerts) > 0 {
		sb.WriteString("\n*Alerts:*\n")
		for _, a := range alerts {
			sb.WriteString(fmt.Sprintf("• :red_circle: %s\n", a))
		}
	}
	if len(warnings) > 0 {
		sb.WriteString("\n*Warnings:*\n")
		for _, w := range warnings {
			sb.WriteString(fmt.Sprintf("• :large_yellow_circle: %s\n", w))
		}
	}

	payload, _ := json.Marshal(map[string]string{"text": sb.String()})
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(webhookURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webhook HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ── Output formatting ─────────────────────────────────────────────────────────

func logf(format string, args ...any) {
	ts := time.Now().Format("15:04:05")
	fmt.Printf("[%s] %s\n", ts, fmt.Sprintf(format, args...))
}

func logPhase(num int, name string) {
	line := fmt.Sprintf("Phase %d: %s", num, name)
	pad := 52 - len(line)
	if pad < 4 {
		pad = 4
	}
	fmt.Println()
	logf("%s %s %s", strings.Repeat("─", 2), line, strings.Repeat("─", pad))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// parseLookback extends time.ParseDuration to support d (days) and w (weeks),
// which are natural units for compliance look-back windows but not part of
// Go's built-in duration syntax.
func parseLookback(s string) (time.Duration, error) {
	switch {
	case strings.HasSuffix(s, "w"):
		n, err := strconv.Atoi(strings.TrimSuffix(s, "w"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid duration %q: weeks must be a positive integer", s)
		}
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	case strings.HasSuffix(s, "d"):
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid duration %q: days must be a positive integer", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	default:
		return time.ParseDuration(s)
	}
}
