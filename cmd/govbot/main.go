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
	Backend     string `json:"backend"` // "sqlite" or "postgres"
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

	auditConfigured := info.Audit.Enabled // false only when HELPDESK_AUDIT_URL is unset on the gateway

	if auditConfigured {
		logf("Audit enabled:    true  (%d total events)", info.Audit.EventsTotal)
		logf("Audit backend:    %s", info.Audit.Backend)
		logf("Chain valid:      %v", info.Audit.ChainValid)
		if info.Audit.LastEventAt != "" {
			logf("Last event:       %s", info.Audit.LastEventAt)
		}
		if info.Audit.ChainValid == false {
			alerts = append(alerts, "Audit hash chain integrity failure — log tampering may have occurred")
		}
	} else {
		logf("Audit enabled:    unknown  (gateway cannot proxy governance queries — HELPDESK_AUDIT_URL not set)")
		warnings = append(warnings, "Gateway has no HELPDESK_AUDIT_URL — governance reporting is unavailable; "+
			"audit logging and policy enforcement may still be active on individual agents")
	}
	if info.Policy != nil {
		logf("Policy enabled:   %v  (%d policies, %d rules)", info.Policy.Enabled, info.Policy.PoliciesCount, info.Policy.RulesCount)
	}
	logf("Pending approvals: %d", info.Approvals.PendingCount)
	logf("Approval notify:  webhook=%v  email=%v", info.Approvals.WebhookConfigured, info.Approvals.EmailConfigured)
	fmt.Println()

	// ── Phase 2: Policy Overview ──────────────────────────────────────────────
	logPhase(2, "Policy Overview")

	if !auditConfigured {
		logf("Policy status unknown — requires governance data from auditd")
	} else if info.Policy == nil || !info.Policy.Enabled {
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
		if !auditConfigured {
			logf("Skipped — audit service not configured")
		} else {
			logf("WARNING: Could not fetch events: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to fetch audit events: %v", err))
		}
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
			allow           int
			deny            int
			requireApproval int
			noMatch         int // policy_name is "" or "default" — misconfiguration
		}
		type blockedEvent struct {
			resource  string
			action    string
			effect    string // "deny" or "require_approval"
			traceID   string
			origin    string
			policy    string
			message   string
			sessionID string
			timestamp string
		}
		byResource := make(map[string]*resourceStats)
		totalAllow, totalDeny, totalReqApproval, totalNoMatch := 0, 0, 0, 0
		unattributableDecisions := 0
		var blockedEvents []blockedEvent

		for i := range events {
			e := &events[i]
			if e.EventType != audit.EventTypePolicyDecision || e.PolicyDecision == nil {
				continue
			}
			pd := e.PolicyDecision
			key := pd.ResourceType + "/" + pd.ResourceName

			if e.TraceID == "" {
				unattributableDecisions++
			}

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
				blockedEvents = append(blockedEvents, blockedEvent{
					resource:  key,
					action:    pd.Action,
					effect:    "deny",
					traceID:   e.TraceID,
					origin:    traceOrigin(e.TraceID),
					policy:    pd.PolicyName,
					message:   pd.Message,
					sessionID: e.Session.ID,
					timestamp: e.Timestamp.Format("01-02 15:04:05"),
				})
			case "require_approval":
				rs.requireApproval++
				totalReqApproval++
				blockedEvents = append(blockedEvents, blockedEvent{
					resource:  key,
					action:    pd.Action,
					effect:    "require_approval",
					traceID:   e.TraceID,
					origin:    traceOrigin(e.TraceID),
					policy:    pd.PolicyName,
					message:   pd.Message,
					sessionID: e.Session.ID,
					timestamp: e.Timestamp.Format("01-02 15:04:05"),
				})
			}
		}

		if len(byResource) == 0 {
			logf("No policy decisions recorded in this window")
		} else {
			logf("%-40s  %6s  %6s  %6s  %6s", "Resource", "allow", "deny", "req_apr", "no_match")
			logf("%s", strings.Repeat("─", 72))

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

		// Denial / require-approval detail — security officer view
		if len(blockedEvents) > 0 {
			fmt.Println()
			logf("Blocked request details (%d):", len(blockedEvents))
			for _, b := range blockedEvents {
				logf("  [%s]  %s  action=%-12s  %s", strings.ToUpper(b.effect), b.timestamp, b.action, b.resource)
				if b.traceID != "" {
					logf("    trace:   %s  ← %s", b.traceID, b.origin)
				} else {
					logf("    trace:   (none — unattributable)")
				}
				if b.sessionID != "" {
					logf("    session: %s", b.sessionID)
				}
				if b.policy != "" {
					logf("    policy:  %s", b.policy)
				}
				if b.message != "" {
					logf("    message: %s", b.message)
				}
			}
		}

		// Unattributable decisions — no trace_id, cannot link to any session or origin
		if unattributableDecisions > 0 {
			fmt.Println()
			logf("Unattributable decisions: %d  ⚠", unattributableDecisions)
			logf("  These policy decisions have no trace_id — they cannot be linked to")
			logf("  any session, user, or call origin. Likely from agents using a local")
			logf("  policy engine without trace propagation wired up.")
			warnings = append(warnings, fmt.Sprintf(
				"%d policy decision(s) have no trace_id — cannot be attributed to any session or call origin",
				unattributableDecisions,
			))
		}

		if totalNoMatch > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"%d policy decisions matched no rule (policy_name=default) — likely missing tags in infrastructure config",
				totalNoMatch,
			))
		}
		if totalDeny > 0 {
			alerts = append(alerts, fmt.Sprintf(
				"%d request(s) were denied by policy — review blocked request details above",
				totalDeny,
			))
		}
	} else {
		logf("No events available for analysis")
	}
	fmt.Println()

	// ── Phase 5: Agent Enforcement Coverage ──────────────────────────────────
	logPhase(5, "Agent Enforcement Coverage")

	// Cross-reference tool_execution and policy_decision events by trace_id.
	// A trace that has tool executions but zero policy decisions means the agent
	// ran tools without going through policy enforcement — alert-level finding.
	//
	// Also track the ratio of policy_decision events with a "chk_*" trace_id
	// (direct/ops curl calls) vs agent-originated ones. A high chk_* ratio
	// means agents are not using centralized enforcement.
	if len(events) == 0 {
		logf("No events available for enforcement analysis")
	} else {
		traceHasToolExec := make(map[string]bool)
		traceHasPolicyDecision := make(map[string]bool)
		totalPolicyDecisions := 0
		chkPolicyDecisions := 0
		unattributablePolicyDecisions := 0

		for i := range events {
			e := &events[i]
			switch e.EventType {
			case audit.EventTypeToolExecution:
				if e.TraceID != "" {
					traceHasToolExec[e.TraceID] = true
				}
			case audit.EventTypePolicyDecision:
				if e.TraceID == "" {
					unattributablePolicyDecisions++
					continue
				}
				traceHasPolicyDecision[e.TraceID] = true
				totalPolicyDecisions++
				if strings.HasPrefix(e.TraceID, "chk_") {
					chkPolicyDecisions++
				}
			}
		}

		// Sub-check A: uncontrolled tool executions
		policyEnabled := info.Policy != nil && info.Policy.Enabled
		var uncontrolledTraces []string
		for traceID := range traceHasToolExec {
			if !traceHasPolicyDecision[traceID] {
				uncontrolledTraces = append(uncontrolledTraces, traceID)
			}
		}
		sort.Strings(uncontrolledTraces)

		controlled := len(traceHasToolExec) - len(uncontrolledTraces)
		logf("Traces with tool executions: %d", len(traceHasToolExec))
		logf("  Controlled (policy checked): %d", controlled)
		logf("  Uncontrolled (no policy):    %d", len(uncontrolledTraces))

		// Prefix breakdown of all traced tool executions
		if len(traceHasToolExec) > 0 {
			prefixCounts := make(map[string]int)
			for traceID := range traceHasToolExec {
				prefixCounts[traceIDPrefix(traceID)]++
			}
			prefixes := make([]string, 0, len(prefixCounts))
			for p := range prefixCounts {
				prefixes = append(prefixes, p)
			}
			sort.Strings(prefixes)
			logf("  Origin breakdown:")
			for _, p := range prefixes {
				logf("    %-6s  %d trace(s)  ← %s", p+"*", prefixCounts[p], traceOriginFromPrefix(p))
			}
		}

		if len(uncontrolledTraces) > 0 && policyEnabled {
			logf("  Uncontrolled traces:")
			for _, id := range uncontrolledTraces {
				logf("    %-20s  ← %s", id, traceOrigin(id))
			}
			alerts = append(alerts, fmt.Sprintf(
				"%d trace(s) contain tool executions with no policy decision — agent(s) may be bypassing enforcement: %s",
				len(uncontrolledTraces), strings.Join(uncontrolledTraces, ", "),
			))
		} else if len(uncontrolledTraces) > 0 {
			// Policy disabled — uncontrolled executions are expected, just note them
			logf("  (policy enforcement disabled — uncontrolled executions are expected)")
		}

		// Sub-check B: chk_* ratio
		fmt.Println()
		logf("Policy decisions in window:  %d  (+ %d unattributable)", totalPolicyDecisions, unattributablePolicyDecisions)
		if totalPolicyDecisions > 0 {
			agentDecisions := totalPolicyDecisions - chkPolicyDecisions
			chkPct := 100 * chkPolicyDecisions / totalPolicyDecisions
			logf("  via agents:                  %d  (%d%%)", agentDecisions, 100-chkPct)
			logf("  via direct (chk_*):          %d  (%d%%)", chkPolicyDecisions, chkPct)
			if chkPct > 50 {
				warnings = append(warnings, fmt.Sprintf(
					"%d%% of policy decisions originate from direct (chk_*) calls, not agents — "+
						"agents may not be using centralized enforcement (HELPDESK_AUDIT_URL)",
					chkPct,
				))
			}
		} else if policyEnabled {
			logf("  No policy decisions recorded — agents may not be calling /v1/governance/check")
			warnings = append(warnings, "Policy enforcement is enabled but no policy decisions were recorded — "+
				"check that agents have HELPDESK_AUDIT_URL configured")
		}
		if unattributablePolicyDecisions > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"%d policy decision(s) in Phase 5 have no trace_id — cannot be correlated to any session or call origin",
				unattributablePolicyDecisions,
			))
		}
	}
	fmt.Println()

	// ── Phase 6: Pending Approvals ────────────────────────────────────────────
	logPhase(6, "Pending Approvals")

	pending, err := getPendingApprovals(*gateway)
	if err != nil {
		if !auditConfigured {
			logf("Skipped — audit service not configured")
		} else {
			logf("WARNING: Could not fetch pending approvals: %v", err)
			warnings = append(warnings, fmt.Sprintf("Failed to fetch pending approvals: %v", err))
		}
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

	// ── Phase 7: Chain Integrity ──────────────────────────────────────────────
	logPhase(7, "Chain Integrity")

	if !auditConfigured {
		logf("Skipped — audit service not configured")
	} else {
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
	}
	fmt.Println()

	// ── Phase 8: Summary ──────────────────────────────────────────────────────
	logPhase(8, "Compliance Summary")

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

// traceIDPrefix extracts the prefix portion of a trace ID (up to and including "_").
func traceIDPrefix(traceID string) string {
	if i := strings.Index(traceID, "_"); i >= 0 {
		return traceID[:i+1]
	}
	return "?"
}

// traceOriginFromPrefix returns a human-readable label for a trace ID prefix.
func traceOriginFromPrefix(prefix string) string {
	switch prefix {
	case "tr_":
		return "natural-language query (POST /api/v1/query)"
	case "dt_":
		return "direct tool call (POST /api/v1/db|k8s/{tool})"
	case "chk_":
		return "direct governance check (POST /v1/governance/check)"
	default:
		return "unknown origin (external or pre-dating prefix scheme)"
	}
}

// traceOrigin returns a human-readable label for a full trace ID.
func traceOrigin(traceID string) string {
	if traceID == "" {
		return "unattributable (no trace_id)"
	}
	return traceOriginFromPrefix(traceIDPrefix(traceID))
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
