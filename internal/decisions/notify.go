package decisions

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// NotifierConfig configures the DecisionNotifier.
// All fields are optional; any omitted channel is silently skipped.
type NotifierConfig struct {
	// WebhookURL is an HTTP endpoint that receives decision events.
	// Slack incoming-webhook URLs are detected automatically and receive
	// a Slack-formatted attachments payload instead of the raw JSON.
	WebhookURL string
	// WebhookSecret, when non-empty, adds an X-Helpdesk-Signature: sha256=<hex>
	// header to every webhook POST so recipients can verify authenticity.
	WebhookSecret string
	// BaseURL is the gateway's public base URL used to build ResolveURL links
	// when a Decision is constructed without one (e.g. "https://helpdesk.corp").
	BaseURL string

	// Email (SMTP) — all five fields must be set to enable email notifications.
	SMTPHost     string
	SMTPPort     string
	SMTPUser     string
	SMTPPassword string
	EmailFrom    string
	EmailTo      []string
}

// DecisionNotifier sends push notifications when decisions are created or resolved.
// A nil *DecisionNotifier is safe to call — all methods are no-ops.
type DecisionNotifier struct {
	cfg        NotifierConfig
	httpClient *http.Client
}

// NewDecisionNotifier creates a notifier from cfg.
func NewDecisionNotifier(cfg NotifierConfig) *DecisionNotifier {
	cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	return &DecisionNotifier{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// NotifyPending fires when a decision is created and awaits operator action.
// The call is non-blocking: notifications are sent in a background goroutine.
func (n *DecisionNotifier) NotifyPending(ctx context.Context, d Decision) {
	if n == nil {
		return
	}
	go n.send(context.WithoutCancel(ctx), "decision_pending", d)
}

// NotifyResolved fires when a decision is approved, denied, or expires.
// The call is non-blocking.
func (n *DecisionNotifier) NotifyResolved(ctx context.Context, d Decision) {
	if n == nil {
		return
	}
	go n.send(context.WithoutCancel(ctx), "decision_resolved", d)
}

// webhookPayload is the normalised JSON body sent to any webhook endpoint.
type webhookPayload struct {
	Event       string         `json:"event"`
	DecisionID  string         `json:"decision_id"`
	Type        DecisionType   `json:"type"`
	Status      string         `json:"status"`
	Summary     string         `json:"summary"`
	RequestedBy string         `json:"requested_by,omitempty"`
	ResolveURL  string         `json:"resolve_url,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
	Timestamp   string         `json:"timestamp"`
}

func (n *DecisionNotifier) send(ctx context.Context, event string, d Decision) {
	payload := webhookPayload{
		Event:       event,
		DecisionID:  d.ID,
		Type:        d.Type,
		Status:      d.Status,
		Summary:     d.Summary,
		RequestedBy: d.RequestedBy,
		ResolveURL:  d.ResolveURL,
		Extra:       d.Extra,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}

	if n.cfg.WebhookURL != "" {
		n.sendWebhook(ctx, event, payload)
	}
	if n.cfg.SMTPHost != "" && n.cfg.EmailFrom != "" && len(n.cfg.EmailTo) > 0 {
		n.sendEmail(event, payload)
	}
}

func (n *DecisionNotifier) sendWebhook(ctx context.Context, event string, payload webhookPayload) {
	var body []byte
	var err error

	if strings.Contains(n.cfg.WebhookURL, "slack.com") {
		body, err = json.Marshal(n.slackPayload(event, payload))
	} else {
		body, err = json.Marshal(payload)
	}
	if err != nil {
		slog.Warn("decisions: failed to marshal webhook payload", "err", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		slog.Warn("decisions: failed to build webhook request", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	if n.cfg.WebhookSecret != "" {
		mac := hmac.New(sha256.New, []byte(n.cfg.WebhookSecret))
		mac.Write(body)
		req.Header.Set("X-Helpdesk-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := n.httpClient.Do(req)
	if err != nil {
		slog.Warn("decisions: webhook delivery failed", "url", n.cfg.WebhookURL, "err", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		slog.Warn("decisions: webhook returned error status", "url", n.cfg.WebhookURL, "status", resp.StatusCode)
	}
}

type slackAttachment struct {
	Color  string `json:"color"`
	Title  string `json:"title"`
	Text   string `json:"text"`
	Footer string `json:"footer"`
	Ts     int64  `json:"ts"`
}

type slackMessage struct {
	Attachments []slackAttachment `json:"attachments"`
}

func (n *DecisionNotifier) slackPayload(event string, p webhookPayload) slackMessage {
	color := "#f0ad4e" // yellow — pending
	if event == "decision_resolved" {
		switch p.Status {
		case "approved":
			color = "#36a64f" // green
		case "denied", "abandoned", "expired":
			color = "#d00000" // red
		}
	}

	title := fmt.Sprintf("[%s] %s", strings.ToUpper(string(p.Type)), p.Summary)

	var lines []string
	if p.RequestedBy != "" {
		lines = append(lines, "Requested by: "+p.RequestedBy)
	}
	if p.ResolveURL != "" && event == "decision_pending" {
		lines = append(lines, "Resolve: "+p.ResolveURL)
	}
	if cw, ok := p.Extra["confidence_warning"].(string); ok && cw != "" {
		lines = append(lines, "⚠ "+cw)
	}

	return slackMessage{Attachments: []slackAttachment{{
		Color:  color,
		Title:  title,
		Text:   strings.Join(lines, "\n"),
		Footer: "aiHelpDesk Decision Hub",
		Ts:     time.Now().Unix(),
	}}}
}

func (n *DecisionNotifier) sendEmail(event string, p webhookPayload) {
	subject := fmt.Sprintf("[aiHelpDesk] Decision %s: %s", event, p.Summary)

	var body strings.Builder
	body.WriteString("Decision ID: " + p.DecisionID + "\n")
	body.WriteString("Type:        " + string(p.Type) + "\n")
	body.WriteString("Status:      " + p.Status + "\n")
	if p.RequestedBy != "" {
		body.WriteString("Requested by: " + p.RequestedBy + "\n")
	}
	if p.ResolveURL != "" {
		body.WriteString("\nTo resolve:\n  " + p.ResolveURL + "\n")
	}

	port := n.cfg.SMTPPort
	if port == "" {
		port = "587"
	}
	addr := n.cfg.SMTPHost + ":" + port

	var auth smtp.Auth
	if n.cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", n.cfg.SMTPUser, n.cfg.SMTPPassword, n.cfg.SMTPHost)
	}

	msg := []byte("To: " + strings.Join(n.cfg.EmailTo, ", ") + "\r\n" +
		"From: " + n.cfg.EmailFrom + "\r\n" +
		"Subject: " + subject + "\r\n\r\n" +
		body.String())

	if err := smtp.SendMail(addr, auth, n.cfg.EmailFrom, n.cfg.EmailTo, msg); err != nil {
		slog.Warn("decisions: email delivery failed", "err", err)
	}
}
