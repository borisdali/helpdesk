// Package main implements the real-time audit agent that monitors delegation
// decisions and alerts on suspicious patterns.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"log/syslog"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/logging"
)

// Config holds auditor configuration from flags.
type Config struct {
	SocketPath string
	LogAll     bool
	OutputJSON bool

	// Webhook configuration
	WebhookURL  string
	WebhookAll  bool // Send all events, not just alerts
	WebhookTest bool // Send test alert on startup

	// Prometheus configuration
	PrometheusAddr string

	// Syslog configuration
	SyslogEnabled bool
	SyslogNetwork string // "udp", "tcp", or "" for local
	SyslogAddr    string // e.g., "localhost:514"
	SyslogTag     string
	SyslogTest    bool // Send test message on startup

	// Email configuration
	SMTPHost     string
	SMTPPort     string
	SMTPUser     string
	SMTPPassword string
	EmailFrom    string
	EmailTo      string // comma-separated list
	EmailTest    bool   // Send test email on startup
}

func main() {
	cfg := Config{}

	flag.StringVar(&cfg.SocketPath, "socket", "audit.sock", "Path to audit Unix socket")
	flag.BoolVar(&cfg.LogAll, "log-all", false, "Log all events, not just alerts")
	flag.BoolVar(&cfg.OutputJSON, "json", false, "Output events as JSON lines")

	// Webhook
	flag.StringVar(&cfg.WebhookURL, "webhook", "", "Webhook URL for alerts (Slack, PagerDuty, etc.)")
	flag.BoolVar(&cfg.WebhookAll, "webhook-all", false, "Send all events to webhook, not just alerts")
	flag.BoolVar(&cfg.WebhookTest, "webhook-test", false, "Send a test alert on startup to verify webhook")

	// Prometheus
	flag.StringVar(&cfg.PrometheusAddr, "prometheus", "", "Address to expose Prometheus metrics (e.g., :9090)")

	// Syslog (Linux only - macOS uses unified logging which doesn't support traditional syslog)
	flag.BoolVar(&cfg.SyslogEnabled, "syslog", false, "Send alerts to syslog (Linux only)")
	flag.StringVar(&cfg.SyslogNetwork, "syslog-network", "", "Syslog network: udp, tcp, or empty for local")
	flag.StringVar(&cfg.SyslogAddr, "syslog-addr", "", "Syslog address (e.g., localhost:514)")
	flag.StringVar(&cfg.SyslogTag, "syslog-tag", "helpdesk-auditor", "Syslog tag")
	flag.BoolVar(&cfg.SyslogTest, "syslog-test", false, "Send test message to syslog on startup")

	// Email
	flag.StringVar(&cfg.SMTPHost, "smtp-host", "", "SMTP server host for email alerts")
	flag.StringVar(&cfg.SMTPPort, "smtp-port", "587", "SMTP server port")
	flag.StringVar(&cfg.SMTPUser, "smtp-user", "", "SMTP username")
	flag.StringVar(&cfg.SMTPPassword, "smtp-password", "", "SMTP password (or use SMTP_PASSWORD env)")
	flag.StringVar(&cfg.EmailFrom, "email-from", "", "Email sender address")
	flag.StringVar(&cfg.EmailTo, "email-to", "", "Email recipients (comma-separated)")
	flag.BoolVar(&cfg.EmailTest, "email-test", false, "Send test email on startup")

	// Initialize logging first (strips --log-level from args)
	args := logging.InitLogging(os.Args[1:])

	// Parse remaining flags
	flag.CommandLine.Parse(args)

	// Allow SMTP password from environment
	if cfg.SMTPPassword == "" {
		cfg.SMTPPassword = os.Getenv("SMTP_PASSWORD")
	}

	slog.Info("starting auditor", "socket", cfg.SocketPath, "log_all", cfg.LogAll)

	// Initialize notifiers
	notifiers := buildNotifiers(cfg)
	if len(notifiers) > 0 {
		slog.Info("notifiers configured", "count", len(notifiers))
	}

	// Send test webhook if requested
	if cfg.WebhookTest && cfg.WebhookURL != "" {
		testAlert := Alert{
			Level:     AlertInfo,
			Message:   "Auditor startup test - webhook is working",
			EventID:   "test_startup",
			SessionID: "test",
			UserID:    "auditor",
			Agent:     "none",
			Details:   map[string]any{"test": true},
			Timestamp: time.Now(),
		}
		for _, n := range notifiers {
			if wh, ok := n.(*WebhookNotifier); ok {
				if err := wh.Send(testAlert); err != nil {
					slog.Error("webhook test failed", "err", err)
				} else {
					slog.Info("webhook test successful")
				}
			}
		}
	}

	// Send test syslog message if requested
	if cfg.SyslogTest && cfg.SyslogEnabled {
		testAlert := Alert{
			Level:     AlertWarning,
			Message:   "Auditor startup test - syslog is working",
			EventID:   "test_startup",
			SessionID: "test",
			UserID:    "auditor",
			Agent:     "none",
			Timestamp: time.Now(),
		}
		for _, n := range notifiers {
			if sl, ok := n.(*SyslogNotifier); ok {
				if err := sl.Send(testAlert); err != nil {
					slog.Error("syslog test failed", "err", err)
				} else {
					slog.Info("syslog test successful")
				}
			}
		}
	}

	// Send test email if requested
	if cfg.EmailTest && cfg.SMTPHost != "" && cfg.EmailTo != "" {
		testAlert := Alert{
			Level:     AlertCritical, // Email only sends on CRITICAL, so use that for test
			Message:   "Auditor startup test - email is working",
			EventID:   "test_startup",
			SessionID: "test",
			UserID:    "auditor",
			Agent:     "none",
			Details:   map[string]any{"test": true},
			Timestamp: time.Now(),
		}
		for _, n := range notifiers {
			if em, ok := n.(*EmailNotifier); ok {
				if err := em.Send(testAlert); err != nil {
					slog.Error("email test failed", "err", err)
				} else {
					slog.Info("email test successful", "to", cfg.EmailTo)
				}
			}
		}
	}

	// Start Prometheus metrics server if configured
	var metrics *Metrics
	if cfg.PrometheusAddr != "" {
		metrics = NewMetrics()
		go func() {
			http.Handle("/metrics", metrics)
			slog.Info("starting Prometheus metrics server", "addr", cfg.PrometheusAddr)
			if err := http.ListenAndServe(cfg.PrometheusAddr, nil); err != nil {
				slog.Error("metrics server failed", "err", err)
			}
		}()
	}

	// Connect to the audit socket
	conn, err := net.Dial("unix", cfg.SocketPath)
	if err != nil {
		slog.Error("failed to connect to audit socket", "path", cfg.SocketPath, "err", err)
		slog.Info("hint: ensure the orchestrator is running with HELPDESK_AUDIT_ENABLED=true")
		os.Exit(1)
	}
	defer conn.Close()

	slog.Info("connected to audit socket, monitoring events...")

	auditor := NewAuditor(cfg, notifiers, metrics)
	scanner := bufio.NewScanner(conn)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event audit.Event
		if err := json.Unmarshal(line, &event); err != nil {
			slog.Warn("failed to parse event", "err", err, "line", string(line))
			continue
		}

		auditor.Analyze(&event)
	}

	if err := scanner.Err(); err != nil {
		slog.Error("socket read error", "err", err)
		os.Exit(1)
	}

	slog.Info("auditor stopped (socket closed)")
}

// --- Notifiers ---

// Notifier sends alerts to external systems.
type Notifier interface {
	Name() string
	Send(alert Alert) error
}

// Alert represents an alert to be sent.
type Alert struct {
	Level     AlertLevel
	Message   string
	EventID   string
	SessionID string
	UserID    string
	Agent     string
	Details   map[string]any
	Timestamp time.Time
}

func buildNotifiers(cfg Config) []Notifier {
	var notifiers []Notifier

	if cfg.WebhookURL != "" {
		notifiers = append(notifiers, &WebhookNotifier{URL: cfg.WebhookURL})
		slog.Info("webhook notifier enabled", "url", cfg.WebhookURL)
	}

	if cfg.SyslogEnabled {
		n, err := NewSyslogNotifier(cfg.SyslogNetwork, cfg.SyslogAddr, cfg.SyslogTag)
		if err != nil {
			slog.Error("failed to create syslog notifier", "err", err)
		} else {
			notifiers = append(notifiers, n)
			slog.Info("syslog notifier enabled")
			// Warn on macOS where traditional syslog doesn't work
			if runtime.GOOS == "darwin" {
				slog.Warn("syslog on macOS may not work - macOS uses unified logging instead of traditional syslog")
			}
		}
	}

	if cfg.SMTPHost != "" && cfg.EmailTo != "" {
		notifiers = append(notifiers, &EmailNotifier{
			Host:     cfg.SMTPHost,
			Port:     cfg.SMTPPort,
			User:     cfg.SMTPUser,
			Password: cfg.SMTPPassword,
			From:     cfg.EmailFrom,
			To:       strings.Split(cfg.EmailTo, ","),
		})
		slog.Info("email notifier enabled", "to", cfg.EmailTo)
	}

	return notifiers
}

// WebhookNotifier sends alerts via HTTP POST.
type WebhookNotifier struct {
	URL string
}

func (w *WebhookNotifier) Name() string { return "webhook" }

func (w *WebhookNotifier) Send(alert Alert) error {
	payload := map[string]any{
		"level":      string(alert.Level),
		"message":    alert.Message,
		"event_id":   alert.EventID,
		"session_id": alert.SessionID,
		"user_id":    alert.UserID,
		"agent":      alert.Agent,
		"timestamp":  alert.Timestamp.Format(time.RFC3339),
		"details":    alert.Details,
	}

	// Slack-compatible format
	if strings.Contains(w.URL, "slack.com") {
		emoji := ":warning:"
		if alert.Level == AlertCritical {
			emoji = ":rotating_light:"
		}
		payload = map[string]any{
			"text": fmt.Sprintf("%s *[%s]* %s\n>Event: %s | Agent: %s | User: %s",
				emoji, alert.Level, alert.Message, alert.EventID, alert.Agent, alert.UserID),
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	resp, err := http.Post(w.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}

// SyslogNotifier sends alerts to syslog.
type SyslogNotifier struct {
	writer *syslog.Writer
}

func NewSyslogNotifier(network, addr, tag string) (*SyslogNotifier, error) {
	var w *syslog.Writer
	var err error

	if network == "" && addr == "" {
		w, err = syslog.New(syslog.LOG_ALERT|syslog.LOG_DAEMON, tag)
	} else {
		w, err = syslog.Dial(network, addr, syslog.LOG_ALERT|syslog.LOG_DAEMON, tag)
	}
	if err != nil {
		return nil, err
	}
	return &SyslogNotifier{writer: w}, nil
}

func (s *SyslogNotifier) Name() string { return "syslog" }

func (s *SyslogNotifier) Send(alert Alert) error {
	msg := fmt.Sprintf("[%s] %s (event=%s agent=%s user=%s)",
		alert.Level, alert.Message, alert.EventID, alert.Agent, alert.UserID)

	switch alert.Level {
	case AlertCritical:
		return s.writer.Crit(msg)
	case AlertWarning:
		return s.writer.Warning(msg)
	default:
		return s.writer.Info(msg)
	}
}

// EmailNotifier sends alerts via SMTP.
type EmailNotifier struct {
	Host     string
	Port     string
	User     string
	Password string
	From     string
	To       []string
}

func (e *EmailNotifier) Name() string { return "email" }

func (e *EmailNotifier) Send(alert Alert) error {
	// Only send email for critical alerts to avoid spam
	if alert.Level != AlertCritical {
		slog.Debug("skipping email - not critical", "level", alert.Level)
		return nil
	}

	slog.Debug("sending email", "to", e.To, "host", e.Host, "port", e.Port)
	subject := fmt.Sprintf("[AUDIT %s] %s", alert.Level, alert.Message)
	body := fmt.Sprintf(`Helpdesk Audit Alert

Level: %s
Message: %s

Event ID: %s
Session: %s
User: %s
Agent: %s
Time: %s

Details:
%v
`,
		alert.Level, alert.Message,
		alert.EventID, alert.SessionID, alert.UserID, alert.Agent,
		alert.Timestamp.Format(time.RFC3339),
		formatDetails(alert.Details))

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		e.From, strings.Join(e.To, ","), subject, body)

	addr := e.Host + ":" + e.Port

	var auth smtp.Auth
	if e.User != "" && e.Password != "" {
		auth = smtp.PlainAuth("", e.User, e.Password, e.Host)
	}

	err := smtp.SendMail(addr, auth, e.From, e.To, []byte(msg))
	if err != nil {
		slog.Debug("smtp.SendMail failed", "err", err)
	} else {
		slog.Debug("smtp.SendMail succeeded")
	}
	return err
}

func formatDetails(details map[string]any) string {
	if len(details) == 0 {
		return "(none)"
	}
	var lines []string
	for k, v := range details {
		lines = append(lines, fmt.Sprintf("  %s: %v", k, v))
	}
	return strings.Join(lines, "\n")
}

// --- Prometheus Metrics ---

// Metrics exposes Prometheus metrics.
type Metrics struct {
	mu              sync.Mutex
	eventsTotal     int64
	alertsTotal     map[AlertLevel]int64
	delegationsByAgent map[string]int64
	errorsByAgent   map[string]int64
}

func NewMetrics() *Metrics {
	return &Metrics{
		alertsTotal:        make(map[AlertLevel]int64),
		delegationsByAgent: make(map[string]int64),
		errorsByAgent:      make(map[string]int64),
	}
}

func (m *Metrics) RecordEvent(event *audit.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.eventsTotal++

	if event.Decision != nil {
		m.delegationsByAgent[event.Decision.Agent]++
	}

	if event.Outcome != nil && event.Outcome.Status == "error" {
		agent := ""
		if event.Decision != nil {
			agent = event.Decision.Agent
		}
		m.errorsByAgent[agent]++
	}
}

func (m *Metrics) RecordAlert(level AlertLevel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alertsTotal[level]++
}

func (m *Metrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")

	fmt.Fprintf(w, "# HELP auditor_events_total Total number of audit events processed\n")
	fmt.Fprintf(w, "# TYPE auditor_events_total counter\n")
	fmt.Fprintf(w, "auditor_events_total %d\n\n", m.eventsTotal)

	fmt.Fprintf(w, "# HELP auditor_alerts_total Total number of alerts by level\n")
	fmt.Fprintf(w, "# TYPE auditor_alerts_total counter\n")
	for level, count := range m.alertsTotal {
		fmt.Fprintf(w, "auditor_alerts_total{level=%q} %d\n", level, count)
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "# HELP auditor_delegations_total Total delegations by agent\n")
	fmt.Fprintf(w, "# TYPE auditor_delegations_total counter\n")
	for agent, count := range m.delegationsByAgent {
		fmt.Fprintf(w, "auditor_delegations_total{agent=%q} %d\n", agent, count)
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "# HELP auditor_errors_total Total errors by agent\n")
	fmt.Fprintf(w, "# TYPE auditor_errors_total counter\n")
	for agent, count := range m.errorsByAgent {
		fmt.Fprintf(w, "auditor_errors_total{agent=%q} %d\n", agent, count)
	}
}

// --- Auditor ---

// Auditor analyzes events and detects suspicious patterns.
type Auditor struct {
	cfg             Config
	notifiers       []Notifier
	metrics         *Metrics
	recentEvents    []audit.Event
	agentErrorCount map[string]int
	agentCallCount  map[string]int
	sessionQueries  map[string][]string
}

// NewAuditor creates a new auditor with initialized state.
func NewAuditor(cfg Config, notifiers []Notifier, metrics *Metrics) *Auditor {
	return &Auditor{
		cfg:             cfg,
		notifiers:       notifiers,
		metrics:         metrics,
		recentEvents:    make([]audit.Event, 0, 100),
		agentErrorCount: make(map[string]int),
		agentCallCount:  make(map[string]int),
		sessionQueries:  make(map[string][]string),
	}
}

// Analyze checks an event against detection rules.
func (a *Auditor) Analyze(event *audit.Event) {
	slog.Debug("analyzing event", "event_id", event.EventID, "type", event.EventType)

	// Record metrics
	if a.metrics != nil {
		a.metrics.RecordEvent(event)
	}

	// Output event
	if a.cfg.OutputJSON {
		a.outputJSON(event)
	} else if a.cfg.LogAll {
		a.logEvent(event)
	}

	// Send all events to webhook if configured
	if a.cfg.WebhookAll {
		a.sendEventToWebhook(event)
	}

	// Track for pattern analysis
	a.trackEvent(event)

	// Run detection rules
	a.checkLowConfidence(event)
	a.checkCategoryMismatch(event)
	a.checkHighErrorRate(event)
	a.checkLongDuration(event)
	a.checkRepeatedQueries(event)
	a.checkEmptyReasoning(event)
}

// outputJSON prints the event as a JSON line.
func (a *Auditor) outputJSON(event *audit.Event) {
	data, err := json.Marshal(event)
	if err != nil {
		slog.Warn("failed to marshal event", "err", err)
		return
	}
	fmt.Println(string(data))
}

// sendEventToWebhook sends every event to webhook (not just alerts).
func (a *Auditor) sendEventToWebhook(event *audit.Event) {
	agent := ""
	confidence := 0.0
	intent := ""
	if event.Decision != nil {
		agent = event.Decision.Agent
		confidence = event.Decision.Confidence
		intent = event.Decision.UserIntent
	}

	outcome := ""
	if event.Outcome != nil {
		outcome = event.Outcome.Status
	}

	alert := Alert{
		Level:     AlertInfo,
		Message:   fmt.Sprintf("Delegation to %s (%.0f%% confidence)", agent, confidence*100),
		EventID:   event.EventID,
		SessionID: event.Session.ID,
		UserID:    event.Session.UserID,
		Agent:     agent,
		Details: map[string]any{
			"intent":     intent,
			"outcome":    outcome,
			"confidence": confidence,
		},
		Timestamp: event.Timestamp,
	}

	for _, n := range a.notifiers {
		if wh, ok := n.(*WebhookNotifier); ok {
			if err := wh.Send(alert); err != nil {
				slog.Warn("webhook event send failed", "err", err)
			}
		}
	}
}

// logEvent prints a summary of every event.
func (a *Auditor) logEvent(event *audit.Event) {
	agent := ""
	confidence := 0.0
	intent := ""
	if event.Decision != nil {
		agent = event.Decision.Agent
		confidence = event.Decision.Confidence
		intent = event.Decision.UserIntent
	}

	outcome := ""
	duration := ""
	if event.Outcome != nil {
		outcome = event.Outcome.Status
		duration = event.Outcome.Duration.String()
	}

	fmt.Printf("\n[EVENT] %s\n", event.EventID)
	fmt.Printf("  Time:       %s\n", event.Timestamp.Format("15:04:05"))
	fmt.Printf("  Session:    %s (user: %s)\n", event.Session.ID, event.Session.UserID)
	fmt.Printf("  Agent:      %s (confidence: %.0f%%)\n", agent, confidence*100)
	fmt.Printf("  Intent:     %s\n", intent)
	if outcome != "" {
		fmt.Printf("  Outcome:    %s (%s)\n", outcome, duration)
	}
	if event.Decision != nil && len(event.Decision.ReasoningChain) > 0 {
		fmt.Printf("  Reasoning:  %s\n", strings.Join(event.Decision.ReasoningChain, " -> "))
	}
}

func (a *Auditor) trackEvent(event *audit.Event) {
	// Add to recent events (keep last 100)
	a.recentEvents = append(a.recentEvents, *event)
	if len(a.recentEvents) > 100 {
		a.recentEvents = a.recentEvents[1:]
	}

	// Track per-agent stats
	if event.Decision != nil {
		agent := event.Decision.Agent
		a.agentCallCount[agent]++

		if event.Outcome != nil && event.Outcome.Status == "error" {
			a.agentErrorCount[agent]++
		}
	}

	// Track queries per session
	if event.Input.UserQuery != "" {
		a.sessionQueries[event.Session.ID] = append(
			a.sessionQueries[event.Session.ID],
			event.Input.UserQuery,
		)
	}
}

// checkLowConfidence alerts on delegations with low confidence scores.
func (a *Auditor) checkLowConfidence(event *audit.Event) {
	if event.Decision == nil {
		return
	}

	confidence := event.Decision.Confidence

	if confidence < 0.5 {
		a.alert(AlertCritical, "very low confidence delegation", event,
			"confidence", confidence,
			"agent", event.Decision.Agent)
	} else if confidence < 0.7 {
		a.alert(AlertWarning, "low confidence delegation", event,
			"confidence", confidence,
			"agent", event.Decision.Agent)
	}
}

// checkCategoryMismatch alerts when the category doesn't match the agent.
func (a *Auditor) checkCategoryMismatch(event *audit.Event) {
	if event.Decision == nil {
		return
	}

	agent := event.Decision.Agent
	category := string(event.Decision.RequestCategory)

	expectedCategories := map[string][]string{
		"postgres_database_agent": {"database"},
		"k8s_agent":               {"kubernetes"},
		"incident_agent":          {"incident"},
		"research_agent":          {"research"},
	}

	expected, ok := expectedCategories[agent]
	if !ok {
		a.alert(AlertWarning, "unknown agent", event, "agent", agent)
		return
	}

	match := false
	for _, cat := range expected {
		if cat == category {
			match = true
			break
		}
	}

	if !match {
		a.alert(AlertWarning, "category/agent mismatch", event,
			"agent", agent,
			"category", category,
			"expected", expected)
	}
}

// checkHighErrorRate alerts when an agent has a high error rate.
func (a *Auditor) checkHighErrorRate(event *audit.Event) {
	if event.Decision == nil || event.Outcome == nil {
		return
	}

	agent := event.Decision.Agent
	calls := a.agentCallCount[agent]
	errors := a.agentErrorCount[agent]

	// Only check after sufficient samples
	if calls < 5 {
		return
	}

	errorRate := float64(errors) / float64(calls)
	if errorRate > 0.5 {
		a.alert(AlertCritical, "high error rate for agent", event,
			"agent", agent,
			"error_rate", fmt.Sprintf("%.0f%%", errorRate*100),
			"errors", errors,
			"calls", calls)
	} else if errorRate > 0.3 {
		a.alert(AlertWarning, "elevated error rate for agent", event,
			"agent", agent,
			"error_rate", fmt.Sprintf("%.0f%%", errorRate*100))
	}
}

// checkLongDuration alerts on unusually long delegation times.
func (a *Auditor) checkLongDuration(event *audit.Event) {
	if event.Outcome == nil {
		return
	}

	duration := event.Outcome.Duration

	if duration > 30*time.Second {
		a.alert(AlertCritical, "very long delegation duration", event,
			"duration", duration.String(),
			"agent", event.Decision.Agent)
	} else if duration > 15*time.Second {
		a.alert(AlertWarning, "long delegation duration", event,
			"duration", duration.String())
	}
}

// checkRepeatedQueries alerts on potential loops or stuck behavior.
func (a *Auditor) checkRepeatedQueries(event *audit.Event) {
	queries := a.sessionQueries[event.Session.ID]
	if len(queries) < 3 {
		return
	}

	// Check for repeated identical queries
	lastQuery := queries[len(queries)-1]
	repeatCount := 0
	for i := len(queries) - 2; i >= 0 && i >= len(queries)-5; i-- {
		if queries[i] == lastQuery {
			repeatCount++
		}
	}

	if repeatCount >= 3 {
		a.alert(AlertCritical, "repeated identical queries detected (possible loop)", event,
			"query", truncate(lastQuery, 50),
			"repeat_count", repeatCount)
	}
}

// checkEmptyReasoning alerts when no reasoning chain is provided.
func (a *Auditor) checkEmptyReasoning(event *audit.Event) {
	if event.Decision == nil {
		return
	}

	if len(event.Decision.ReasoningChain) == 0 {
		a.alert(AlertWarning, "delegation without reasoning chain", event,
			"agent", event.Decision.Agent)
	}
}

// AlertLevel represents the severity of an alert.
type AlertLevel string

const (
	AlertInfo     AlertLevel = "INFO"
	AlertWarning  AlertLevel = "WARNING"
	AlertCritical AlertLevel = "CRITICAL"
)

func (a *Auditor) alert(level AlertLevel, message string, event *audit.Event, keyvals ...any) {
	// Record metric
	if a.metrics != nil {
		a.metrics.RecordAlert(level)
	}

	// Build details map
	details := make(map[string]any)
	for i := 0; i < len(keyvals)-1; i += 2 {
		if key, ok := keyvals[i].(string); ok {
			details[key] = keyvals[i+1]
		}
	}

	agent := ""
	if event.Decision != nil {
		agent = event.Decision.Agent
	}

	alert := Alert{
		Level:     level,
		Message:   message,
		EventID:   event.EventID,
		SessionID: event.Session.ID,
		UserID:    event.Session.UserID,
		Agent:     agent,
		Details:   details,
		Timestamp: event.Timestamp,
	}

	// Console output
	attrs := []any{
		"event_id", event.EventID,
		"session_id", event.Session.ID,
		"user_id", event.Session.UserID,
	}
	attrs = append(attrs, keyvals...)

	alertLine := fmt.Sprintf("[AUDIT %s] %s", level, message)

	switch level {
	case AlertCritical:
		fmt.Fprintf(os.Stderr, "\n🚨 %s\n", alertLine)
		slog.Error(alertLine, attrs...)
	case AlertWarning:
		fmt.Fprintf(os.Stderr, "\n⚠️  %s\n", alertLine)
		slog.Warn(alertLine, attrs...)
	default:
		slog.Info(alertLine, attrs...)
	}

	// Print reasoning chain for context on warnings/criticals
	if level != AlertInfo && event.Decision != nil && len(event.Decision.ReasoningChain) > 0 {
		fmt.Fprintf(os.Stderr, "    Reasoning: %s\n", strings.Join(event.Decision.ReasoningChain, " → "))
	}

	// Send to notifiers
	for _, n := range a.notifiers {
		if err := n.Send(alert); err != nil {
			slog.Warn("notifier failed", "notifier", n.Name(), "err", err)
		}
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
