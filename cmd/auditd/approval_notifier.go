package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	"helpdesk/internal/audit"
)

// ApprovalNotifier sends notifications for approval events.
type ApprovalNotifier struct {
	webhookURL   string
	callbackURLs map[string]string // approvalID -> callbackURL
	baseURL      string            // Base URL for approve/deny links in emails

	// Email configuration
	smtpHost     string
	smtpPort     string
	smtpUser     string
	smtpPassword string
	emailFrom    string
	emailTo      []string
}

// ApprovalNotifierConfig configures the approval notifier.
type ApprovalNotifierConfig struct {
	WebhookURL   string
	BaseURL      string // Base URL for approve/deny links (e.g., http://localhost:1199)
	SMTPHost     string
	SMTPPort     string
	SMTPUser     string
	SMTPPassword string
	EmailFrom    string
	EmailTo      string // comma-separated
}

// NewApprovalNotifier creates a new approval notifier.
func NewApprovalNotifier(cfg ApprovalNotifierConfig) *ApprovalNotifier {
	var emailTo []string
	if cfg.EmailTo != "" {
		for _, e := range strings.Split(cfg.EmailTo, ",") {
			emailTo = append(emailTo, strings.TrimSpace(e))
		}
	}

	return &ApprovalNotifier{
		webhookURL:   cfg.WebhookURL,
		baseURL:      strings.TrimSuffix(cfg.BaseURL, "/"),
		callbackURLs: make(map[string]string),
		smtpHost:     cfg.SMTPHost,
		smtpPort:     cfg.SMTPPort,
		smtpUser:     cfg.SMTPUser,
		smtpPassword: cfg.SMTPPassword,
		emailFrom:    cfg.EmailFrom,
		emailTo:      emailTo,
	}
}

// IsEnabled returns true if any notification method is configured.
func (n *ApprovalNotifier) IsEnabled() bool {
	return n.webhookURL != "" || (n.smtpHost != "" && len(n.emailTo) > 0)
}

// RegisterCallback registers a callback URL for an approval ID.
func (n *ApprovalNotifier) RegisterCallback(approvalID, callbackURL string) {
	if callbackURL != "" {
		n.callbackURLs[approvalID] = callbackURL
	}
}

// NotifyCreated sends notifications when a new approval request is created.
func (n *ApprovalNotifier) NotifyCreated(ctx context.Context, approval *audit.StoredApproval) {
	if !n.IsEnabled() {
		return
	}

	// Register callback URL if provided
	if approval.CallbackURL != "" {
		n.RegisterCallback(approval.ApprovalID, approval.CallbackURL)
	}

	// Send webhook notification
	if n.webhookURL != "" {
		go n.sendWebhook(approval, "created")
	}

	// Send email notification
	if n.smtpHost != "" && len(n.emailTo) > 0 {
		go n.sendEmail(approval, "created")
	}
}

// NotifyResolved sends notifications when an approval request is resolved.
func (n *ApprovalNotifier) NotifyResolved(ctx context.Context, approval *audit.StoredApproval) {
	// Send callback to registered URL
	if callbackURL, ok := n.callbackURLs[approval.ApprovalID]; ok {
		go n.sendCallback(callbackURL, approval)
		delete(n.callbackURLs, approval.ApprovalID)
	}

	// Send webhook notification
	if n.webhookURL != "" {
		go n.sendWebhook(approval, "resolved")
	}

	// Send email notification (only for denials)
	if n.smtpHost != "" && len(n.emailTo) > 0 && approval.Status == "denied" {
		go n.sendEmail(approval, "resolved")
	}
}

// sendWebhook sends a webhook notification.
func (n *ApprovalNotifier) sendWebhook(approval *audit.StoredApproval, eventType string) {
	payload := map[string]any{
		"event_type":  "approval_" + eventType,
		"approval_id": approval.ApprovalID,
		"status":      approval.Status,
		"action":      approval.ActionClass,
		"tool":        approval.ToolName,
		"agent":       approval.AgentName,
		"resource":    approval.ResourceType + "/" + approval.ResourceName,
		"requested_by": approval.RequestedBy,
		"requested_at": approval.RequestedAt.Format(time.RFC3339),
		"timestamp":   time.Now().Format(time.RFC3339),
	}

	if approval.ResolvedBy != "" {
		payload["resolved_by"] = approval.ResolvedBy
		payload["resolved_at"] = approval.ResolvedAt.Format(time.RFC3339)
		payload["reason"] = approval.ResolutionReason
	}

	if !approval.ExpiresAt.IsZero() {
		payload["expires_at"] = approval.ExpiresAt.Format(time.RFC3339)
	}

	// Slack-compatible format
	if strings.Contains(n.webhookURL, "slack.com") {
		emoji := ":hourglass:"
		color := "#FFA500" // orange for pending
		title := "Approval Request Created"

		switch approval.Status {
		case "approved":
			emoji = ":white_check_mark:"
			color = "#36A64F" // green
			title = "Approval Granted"
		case "denied":
			emoji = ":x:"
			color = "#FF0000" // red
			title = "Approval Denied"
		case "expired":
			emoji = ":alarm_clock:"
			color = "#808080" // gray
			title = "Approval Expired"
		case "cancelled":
			emoji = ":no_entry_sign:"
			color = "#808080"
			title = "Approval Cancelled"
		}

		text := fmt.Sprintf("%s *%s*\n", emoji, title)
		text += fmt.Sprintf("*ID:* `%s`\n", approval.ApprovalID)
		text += fmt.Sprintf("*Action:* %s\n", approval.ActionClass)
		if approval.ToolName != "" {
			text += fmt.Sprintf("*Tool:* %s\n", approval.ToolName)
		}
		if approval.AgentName != "" {
			text += fmt.Sprintf("*Agent:* %s\n", approval.AgentName)
		}
		text += fmt.Sprintf("*Requested by:* %s\n", approval.RequestedBy)

		if approval.ResolvedBy != "" {
			text += fmt.Sprintf("*Resolved by:* %s\n", approval.ResolvedBy)
			if approval.ResolutionReason != "" {
				text += fmt.Sprintf("*Reason:* %s\n", approval.ResolutionReason)
			}
		}

		payload = map[string]any{
			"attachments": []map[string]any{
				{
					"color": color,
					"text":  text,
					"ts":    time.Now().Unix(),
				},
			},
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("failed to marshal webhook payload", "err", err)
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(n.webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Error("failed to send approval webhook", "err", err, "approval_id", approval.ApprovalID)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		slog.Error("approval webhook returned error", "status", resp.StatusCode, "approval_id", approval.ApprovalID)
	} else {
		slog.Debug("approval webhook sent", "approval_id", approval.ApprovalID, "event", eventType)
	}
}

// sendCallback sends a callback to the registered URL when approval is resolved.
func (n *ApprovalNotifier) sendCallback(callbackURL string, approval *audit.StoredApproval) {
	payload := map[string]any{
		"approval_id":       approval.ApprovalID,
		"status":            approval.Status,
		"resolved_by":       approval.ResolvedBy,
		"resolved_at":       approval.ResolvedAt.Format(time.RFC3339),
		"resolution_reason": approval.ResolutionReason,
	}

	if !approval.ApprovalValidUntil.IsZero() {
		payload["valid_until"] = approval.ApprovalValidUntil.Format(time.RFC3339)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("failed to marshal callback payload", "err", err)
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(callbackURL, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Error("failed to send approval callback", "err", err, "approval_id", approval.ApprovalID, "url", callbackURL)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		slog.Error("approval callback returned error", "status", resp.StatusCode, "approval_id", approval.ApprovalID)
	} else {
		slog.Info("approval callback sent", "approval_id", approval.ApprovalID, "status", approval.Status)
	}
}

// sendEmail sends an email notification.
func (n *ApprovalNotifier) sendEmail(approval *audit.StoredApproval, eventType string) {
	var subject, body string

	if eventType == "created" {
		subject = fmt.Sprintf("[APPROVAL REQUIRED] %s - %s", approval.ActionClass, approval.ToolName)

		// Build approve/deny links if baseURL is configured
		var actionLinks string
		if n.baseURL != "" {
			actionLinks = fmt.Sprintf(`
Quick Actions (click to open in browser, then POST with curl):

  Approve: curl -X POST "%s/v1/approvals/%s/approve" -H "Content-Type: application/json" -d '{"approved_by":"email_user","reason":"Approved via email"}'

  Deny:    curl -X POST "%s/v1/approvals/%s/deny" -H "Content-Type: application/json" -d '{"denied_by":"email_user","reason":"Denied via email"}'

`,
				n.baseURL, approval.ApprovalID,
				n.baseURL, approval.ApprovalID,
			)
		}

		body = fmt.Sprintf(`Approval Request Pending

A new approval request requires your attention.

Approval ID: %s
Action:      %s
Tool:        %s
Agent:       %s
Requested:   %s by %s
Expires:     %s
%s
CLI Commands:

  approvals approve %s --reason "..."
  approvals deny %s --reason "..."

`,
			approval.ApprovalID,
			approval.ActionClass,
			approval.ToolName,
			approval.AgentName,
			approval.RequestedAt.Format(time.RFC3339),
			approval.RequestedBy,
			approval.ExpiresAt.Format(time.RFC3339),
			actionLinks,
			approval.ApprovalID,
			approval.ApprovalID,
		)
	} else {
		statusUpper := strings.ToUpper(approval.Status)
		subject = fmt.Sprintf("[APPROVAL %s] %s - %s", statusUpper, approval.ActionClass, approval.ToolName)
		body = fmt.Sprintf(`Approval Request %s

Approval ID: %s
Status:      %s
Action:      %s
Tool:        %s
Agent:       %s
Requested:   %s by %s
Resolved:    %s by %s
Reason:      %s
`,
			statusUpper,
			approval.ApprovalID,
			approval.Status,
			approval.ActionClass,
			approval.ToolName,
			approval.AgentName,
			approval.RequestedAt.Format(time.RFC3339),
			approval.RequestedBy,
			approval.ResolvedAt.Format(time.RFC3339),
			approval.ResolvedBy,
			approval.ResolutionReason,
		)
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		n.emailFrom, strings.Join(n.emailTo, ","), subject, body)

	addr := n.smtpHost + ":" + n.smtpPort

	var auth smtp.Auth
	if n.smtpUser != "" && n.smtpPassword != "" {
		auth = smtp.PlainAuth("", n.smtpUser, n.smtpPassword, n.smtpHost)
	}

	err := smtp.SendMail(addr, auth, n.emailFrom, n.emailTo, []byte(msg))
	if err != nil {
		slog.Error("failed to send approval email", "err", err, "approval_id", approval.ApprovalID)
	} else {
		slog.Info("approval email sent", "approval_id", approval.ApprovalID, "to", n.emailTo)
	}
}
