// Package main implements the real-time audit agent that monitors delegation
// decisions and alerts on suspicious patterns.
package main

import (
	"bufio"
	"bytes"
	"context"
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

	// Verification mode
	Verify bool   // Run chain integrity verification
	DBPath string // Path to audit database (for verify mode)

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

	// Security monitoring
	AuditServiceURL    string        // URL of central audit service for periodic verification
	VerifyInterval     time.Duration // How often to verify chain integrity (0 = disabled)
	IncidentWebhookURL string        // URL to POST security incidents
	MaxEventsPerMinute int           // Alert threshold for high-volume activity (0 = disabled)
	AllowedHoursStart  int           // Start of allowed hours (0-23), -1 to disable
	AllowedHoursEnd    int           // End of allowed hours (0-23)

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

	// Verification mode
	flag.BoolVar(&cfg.Verify, "verify", false, "Verify audit chain integrity and exit")
	flag.StringVar(&cfg.DBPath, "db", "audit.db", "Path to audit database (for verify mode)")

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

	// Security monitoring
	flag.StringVar(&cfg.AuditServiceURL, "audit-service", "", "URL of central audit service for periodic verification (e.g., http://localhost:1199)")
	flag.DurationVar(&cfg.VerifyInterval, "verify-interval", 0, "How often to verify chain integrity (e.g., 5m, 1h). 0 = disabled")
	flag.StringVar(&cfg.IncidentWebhookURL, "incident-webhook", "", "URL to POST security incidents for automated response")
	flag.IntVar(&cfg.MaxEventsPerMinute, "max-events-per-minute", 0, "Alert on high event volume (0 = disabled)")
	flag.IntVar(&cfg.AllowedHoursStart, "allowed-hours-start", -1, "Start of allowed operating hours (0-23), -1 = disabled")
	flag.IntVar(&cfg.AllowedHoursEnd, "allowed-hours-end", -1, "End of allowed operating hours (0-23)")

	// Initialize logging first (strips --log-level from args)
	args := logging.InitLogging(os.Args[1:])

	// Parse remaining flags
	flag.CommandLine.Parse(args)

	// Allow SMTP password from environment
	if cfg.SMTPPassword == "" {
		cfg.SMTPPassword = os.Getenv("SMTP_PASSWORD")
	}

	// Handle verify mode
	if cfg.Verify {
		runVerifyMode(cfg)
		return
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
		if cfg.AuditServiceURL != "" {
			// Fall back to HTTP polling mode when the socket is not available.
			// This lets operators run  ./auditor -audit-service=http://localhost:1199
			// to inspect events without needing the Unix socket.
			slog.Info("audit socket not available; switching to HTTP polling mode",
				"socket", cfg.SocketPath, "url", cfg.AuditServiceURL)
			auditor := NewAuditor(cfg, notifiers, metrics)
			runHTTPPollingMode(cfg, auditor)
			return
		}
		slog.Error("failed to connect to audit socket", "path", cfg.SocketPath, "err", err)
		slog.Info("hint: ensure the orchestrator is running with HELPDESK_AUDIT_ENABLED=true")
		slog.Info("hint: or use -audit-service=<url> to poll events via HTTP when the socket is unavailable")
		os.Exit(1)
	}
	defer conn.Close()

	slog.Info("connected to audit socket, monitoring events...")

	auditor := NewAuditor(cfg, notifiers, metrics)

	// Start periodic chain verification if configured
	if cfg.VerifyInterval > 0 && cfg.AuditServiceURL != "" {
		go auditor.runPeriodicVerification(cfg.AuditServiceURL, cfg.VerifyInterval)
	}

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

// runVerifyMode verifies the integrity of the audit chain and exits.
func runVerifyMode(cfg Config) {
	fmt.Println("Audit Chain Verification")
	fmt.Println("========================")
	fmt.Printf("Database: %s\n\n", cfg.DBPath)

	// Open the audit store
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: cfg.DBPath,
	})
	if err != nil {
		fmt.Printf("ERROR: Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	// Verify the chain
	ctx := context.Background()
	status, err := store.VerifyIntegrity(ctx)
	if err != nil {
		fmt.Printf("ERROR: Verification failed: %v\n", err)
		os.Exit(1)
	}

	// Display results
	fmt.Printf("Total Events:   %d\n", status.TotalEvents)
	fmt.Printf("Hashed Events:  %d\n", status.HashedEvents)
	fmt.Printf("Legacy Events:  %d (no hash chain)\n", status.LegacyEvents)
	fmt.Println()

	if status.TotalEvents > 0 {
		fmt.Printf("First Event:    %s\n", status.FirstEventID)
		fmt.Printf("Last Event:     %s\n", status.LastEventID)
		if status.LastHash != "" {
			fmt.Printf("Last Hash:      %s...%s\n", status.LastHash[:16], status.LastHash[len(status.LastHash)-8:])
		}
		fmt.Println()
	}

	if status.Valid {
		fmt.Println("Status: ✓ VALID")
		fmt.Println("The audit chain has not been tampered with.")
		os.Exit(0)
	} else {
		fmt.Println("Status: ✗ INVALID")
		fmt.Printf("Chain broken at event index: %d\n", status.BrokenAt)
		fmt.Printf("Error: %s\n", status.Error)
		fmt.Println()
		fmt.Println("⚠️  The audit chain may have been tampered with!")
		fmt.Println("   Investigate the event at the broken link for potential issues.")
		os.Exit(1)
	}
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
	lastEventHash   string // For chain integrity verification
	lastEventTime   time.Time
	lastEventID     int64 // Sequence tracking

	// Security monitoring
	eventsThisMinute int
	minuteStart      time.Time
	securityAlerts   []SecurityAlert // Recent security alerts for incident creation
	mu               sync.Mutex
}

// SecurityAlert represents a security-related alert for incident creation.
type SecurityAlert struct {
	Type      string    `json:"type"`
	Severity  string    `json:"severity"`
	Message   string    `json:"message"`
	EventID   string    `json:"event_id"`
	TraceID   string    `json:"trace_id"`
	Details   map[string]any `json:"details"`
	Timestamp time.Time `json:"timestamp"`
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
		minuteStart:     time.Now(),
		securityAlerts:  make([]SecurityAlert, 0),
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
	a.checkDangerousAction(event)
	a.checkApprovalStatus(event)
	a.checkChainIntegrity(event)

	// Security-specific checks
	a.checkHighVolume(event)
	a.checkOffHours(event)
	a.checkUnauthorizedDestructive(event)
	a.checkTimestampGap(event)
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
	if event.TraceID != "" {
		fmt.Printf("  Trace:      %s\n", event.TraceID)
	}
	fmt.Printf("  Time:       %s\n", event.Timestamp.Format("15:04:05"))
	fmt.Printf("  Type:       %s\n", event.EventType)
	if event.ActionClass != "" {
		actionIcon := actionClassIcon(event.ActionClass)
		fmt.Printf("  Action:     %s %s\n", actionIcon, event.ActionClass)
	}
	fmt.Printf("  Session:    %s (user: %s)\n", event.Session.ID, event.Session.UserID)
	fmt.Printf("  Agent:      %s (confidence: %.0f%%)\n", agent, confidence*100)
	fmt.Printf("  Intent:     %s\n", intent)
	if outcome != "" {
		fmt.Printf("  Outcome:    %s (%s)\n", outcome, duration)
	}
	if event.Tool != nil {
		fmt.Printf("  Tool:       %s\n", event.Tool.Name)
		if len(event.Tool.Parameters) > 0 {
			fmt.Printf("  Params:     %v\n", formatParams(event.Tool.Parameters))
		}
		if event.Tool.RawCommand != "" {
			fmt.Printf("  Command:    %s\n", truncate(event.Tool.RawCommand, 100))
		}
	}
	if event.Approval != nil {
		approvalIcon := approvalStatusIcon(event.Approval.Status)
		fmt.Printf("  Approval:   %s %s", approvalIcon, event.Approval.Status)
		if event.Approval.ApprovedBy != "" {
			fmt.Printf(" (by %s)", event.Approval.ApprovedBy)
		}
		if event.Approval.PolicyName != "" {
			fmt.Printf(" [%s]", event.Approval.PolicyName)
		}
		fmt.Println()
	}
	if event.Decision != nil && len(event.Decision.ReasoningChain) > 0 {
		fmt.Printf("  Reasoning:  %s\n", strings.Join(event.Decision.ReasoningChain, " -> "))
	}
	// Display hash chain info if present
	if event.EventHash != "" {
		fmt.Printf("  Hash:       %s...%s\n", event.EventHash[:12], event.EventHash[len(event.EventHash)-6:])
		if event.PrevHash != "" && event.PrevHash != audit.GenesisHash {
			fmt.Printf("  PrevHash:   %s...\n", event.PrevHash[:12])
		}
	}
}

// approvalStatusIcon returns an icon for the approval status.
func approvalStatusIcon(status audit.ApprovalStatus) string {
	switch status {
	case audit.ApprovalApproved, audit.ApprovalAutoApproved:
		return "[OK]"
	case audit.ApprovalPending:
		return "[??]"
	case audit.ApprovalDenied:
		return "[NO]"
	default:
		return "[--]"
	}
}

// formatParams formats tool parameters for display.
func formatParams(params map[string]any) string {
	if len(params) == 0 {
		return "{}"
	}
	var parts []string
	for k, v := range params {
		// Mask sensitive parameters
		if isSensitiveParam(k) {
			parts = append(parts, fmt.Sprintf("%s=***", k))
		} else {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return strings.Join(parts, ", ")
}

// isSensitiveParam returns true if the parameter name suggests sensitive data.
func isSensitiveParam(name string) bool {
	lower := strings.ToLower(name)
	sensitive := []string{"password", "secret", "token", "key", "credential", "auth"}
	for _, s := range sensitive {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// actionClassIcon returns an icon for the action class.
func actionClassIcon(ac audit.ActionClass) string {
	switch ac {
	case audit.ActionRead:
		return "[R]"
	case audit.ActionWrite:
		return "[W]"
	case audit.ActionDestructive:
		return "[D]"
	default:
		return "[?]"
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
// Tool executions don't have confidence scores - they're not LLM decisions.
func (a *Auditor) checkLowConfidence(event *audit.Event) {
	// Tool executions don't have confidence scores
	if event.EventType == audit.EventTypeToolExecution {
		return
	}
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
// Only applies to orchestrator delegations, not gateway/tool events.
func (a *Auditor) checkEmptyReasoning(event *audit.Event) {
	// Gateway requests and tool executions don't have reasoning chains
	if event.EventType == audit.EventTypeGatewayRequest ||
		event.EventType == audit.EventTypeToolExecution {
		return
	}

	if event.Decision == nil {
		return
	}

	if len(event.Decision.ReasoningChain) == 0 {
		a.alert(AlertWarning, "delegation without reasoning chain", event,
			"agent", event.Decision.Agent)
	}
}

// checkDangerousAction alerts on write or destructive operations.
func (a *Auditor) checkDangerousAction(event *audit.Event) {
	switch event.ActionClass {
	case audit.ActionWrite:
		a.alert(AlertWarning, "write operation detected", event,
			"action_class", string(event.ActionClass),
			"trace_id", event.TraceID)
	case audit.ActionDestructive:
		a.alert(AlertCritical, "DESTRUCTIVE operation detected", event,
			"action_class", string(event.ActionClass),
			"trace_id", event.TraceID)
	}
}

// checkApprovalStatus alerts on approval issues.
func (a *Auditor) checkApprovalStatus(event *audit.Event) {
	if event.Approval == nil {
		return
	}

	switch event.Approval.Status {
	case audit.ApprovalPending:
		a.alert(AlertWarning, "action pending approval", event,
			"approval_status", string(event.Approval.Status),
			"requested_by", event.Approval.RequestedBy)
	case audit.ApprovalDenied:
		a.alert(AlertCritical, "action was DENIED", event,
			"approval_status", string(event.Approval.Status),
			"denied_by", event.Approval.ApprovedBy,
			"reason", event.Approval.Justification)
	}

	// Check for expired approvals
	if event.Approval.Status == audit.ApprovalApproved && !event.Approval.ExpiresAt.IsZero() {
		if time.Now().After(event.Approval.ExpiresAt) {
			a.alert(AlertWarning, "approval has expired", event,
				"expired_at", event.Approval.ExpiresAt.Format(time.RFC3339))
		}
	}
}

// checkChainIntegrity verifies the hash chain in real-time.
func (a *Auditor) checkChainIntegrity(event *audit.Event) {
	// Skip if event has no hash chain (legacy event)
	if event.EventHash == "" {
		return
	}

	// Verify the event's own hash
	if !audit.VerifyEventHash(event) {
		a.alert(AlertCritical, "EVENT HASH MISMATCH - possible tampering!", event,
			"event_hash", truncate(event.EventHash, 20),
			"trace_id", event.TraceID)
	}

	// Verify chain continuity
	if a.lastEventHash != "" && event.PrevHash != "" {
		// PrevHash should match the last event's hash we saw
		if event.PrevHash != a.lastEventHash {
			a.alert(AlertCritical, "CHAIN LINK BROKEN - possible tampering!", event,
				"prev_hash", truncate(event.PrevHash, 20),
				"expected", truncate(a.lastEventHash, 20),
				"trace_id", event.TraceID)
		}
	}

	// Update last hash for next event
	a.lastEventHash = event.EventHash
}

// checkHighVolume detects unusually high event rates (potential attack or data exfiltration).
func (a *Auditor) checkHighVolume(event *audit.Event) {
	if a.cfg.MaxEventsPerMinute <= 0 {
		return
	}

	var shouldAlert bool
	var eventCount int

	a.mu.Lock()
	now := time.Now()
	if now.Sub(a.minuteStart) >= time.Minute {
		// Reset counter for new minute
		a.eventsThisMinute = 0
		a.minuteStart = now
	}

	a.eventsThisMinute++
	eventCount = a.eventsThisMinute

	// Only alert once per minute (when we first exceed threshold)
	if a.eventsThisMinute == a.cfg.MaxEventsPerMinute+1 {
		shouldAlert = true
	}
	a.mu.Unlock()

	if shouldAlert {
		a.recordSecurityAlert("high_volume", AlertCritical, "High volume activity detected - possible attack or data exfiltration", event,
			"events_per_minute", eventCount,
			"threshold", a.cfg.MaxEventsPerMinute)
	}
}

// checkOffHours detects activity outside allowed operating hours.
func (a *Auditor) checkOffHours(event *audit.Event) {
	if a.cfg.AllowedHoursStart < 0 || a.cfg.AllowedHoursEnd < 0 {
		return
	}

	// Convert to local time for off-hours checking
	localTime := event.Timestamp.Local()
	hour := localTime.Hour()
	inAllowedHours := false

	if a.cfg.AllowedHoursStart <= a.cfg.AllowedHoursEnd {
		// Simple range (e.g., 9-17)
		inAllowedHours = hour >= a.cfg.AllowedHoursStart && hour < a.cfg.AllowedHoursEnd
	} else {
		// Overnight range (e.g., 22-6)
		inAllowedHours = hour >= a.cfg.AllowedHoursStart || hour < a.cfg.AllowedHoursEnd
	}

	if !inAllowedHours {
		a.recordSecurityAlert("off_hours", AlertWarning, "Activity detected outside allowed hours", event,
			"event_hour_local", hour,
			"allowed_start", a.cfg.AllowedHoursStart,
			"allowed_end", a.cfg.AllowedHoursEnd)
	}
}

// checkUnauthorizedDestructive detects destructive operations without proper approval.
func (a *Auditor) checkUnauthorizedDestructive(event *audit.Event) {
	if event.ActionClass != audit.ActionDestructive {
		return
	}

	// Check if approval exists and is valid
	if event.Approval == nil {
		a.recordSecurityAlert("unauthorized_destructive", AlertCritical,
			"DESTRUCTIVE operation without approval record", event,
			"action_class", string(event.ActionClass))
		return
	}

	if event.Approval.Status != audit.ApprovalApproved && event.Approval.Status != audit.ApprovalAutoApproved {
		a.recordSecurityAlert("unauthorized_destructive", AlertCritical,
			"DESTRUCTIVE operation not approved", event,
			"action_class", string(event.ActionClass),
			"approval_status", string(event.Approval.Status))
	}
}

// checkTimestampGap detects suspicious gaps in event timestamps (potential deletion).
func (a *Auditor) checkTimestampGap(event *audit.Event) {
	if a.lastEventTime.IsZero() {
		a.lastEventTime = event.Timestamp
		return
	}

	gap := event.Timestamp.Sub(a.lastEventTime)

	// Negative gap indicates time manipulation or out-of-order events
	if gap < -time.Second {
		a.recordSecurityAlert("timestamp_anomaly", AlertCritical,
			"Event timestamp is before previous event - possible manipulation", event,
			"gap", gap.String(),
			"previous_time", a.lastEventTime.Format(time.RFC3339))
	}

	// Large gap might indicate deleted events (only flag if > 1 hour and during business hours)
	if gap > time.Hour {
		hour := event.Timestamp.Hour()
		if hour >= 8 && hour <= 18 { // During typical working hours
			a.recordSecurityAlert("timestamp_gap", AlertWarning,
				"Large gap between events - possible event deletion", event,
				"gap", gap.String(),
				"previous_time", a.lastEventTime.Format(time.RFC3339))
		}
	}

	a.lastEventTime = event.Timestamp
}

// recordSecurityAlert records a security alert and optionally sends to incident webhook.
func (a *Auditor) recordSecurityAlert(alertType string, level AlertLevel, message string, event *audit.Event, keyvals ...any) {
	// Build details map
	details := make(map[string]any)
	for i := 0; i < len(keyvals)-1; i += 2 {
		if key, ok := keyvals[i].(string); ok {
			details[key] = keyvals[i+1]
		}
	}

	secAlert := SecurityAlert{
		Type:      alertType,
		Severity:  string(level),
		Message:   message,
		EventID:   event.EventID,
		TraceID:   event.TraceID,
		Details:   details,
		Timestamp: time.Now(),
	}

	// Store alert
	a.mu.Lock()
	a.securityAlerts = append(a.securityAlerts, secAlert)
	// Keep only last 100 alerts
	if len(a.securityAlerts) > 100 {
		a.securityAlerts = a.securityAlerts[1:]
	}
	a.mu.Unlock()

	// Send to incident webhook if configured
	if a.cfg.IncidentWebhookURL != "" && level == AlertCritical {
		go a.sendSecurityIncident(secAlert)
	}

	// Also send through normal alert mechanism
	a.alert(level, message, event, keyvals...)
}

// sendSecurityIncident POSTs a security incident to the configured webhook.
func (a *Auditor) sendSecurityIncident(alert SecurityAlert) {
	incident := map[string]any{
		"type":        "security_incident",
		"alert_type":  alert.Type,
		"severity":    alert.Severity,
		"message":     alert.Message,
		"event_id":    alert.EventID,
		"trace_id":    alert.TraceID,
		"details":     alert.Details,
		"timestamp":   alert.Timestamp.Format(time.RFC3339),
		"source":      "audit_monitor",
	}

	body, err := json.Marshal(incident)
	if err != nil {
		slog.Error("failed to marshal security incident", "err", err)
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(a.cfg.IncidentWebhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Error("failed to send security incident", "url", a.cfg.IncidentWebhookURL, "err", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		slog.Error("security incident webhook returned error", "status", resp.StatusCode)
	} else {
		slog.Info("security incident sent", "type", alert.Type, "severity", alert.Severity)
	}
}

// runPeriodicVerification periodically verifies the audit chain integrity.
func (a *Auditor) runPeriodicVerification(auditServiceURL string, interval time.Duration) {
	slog.Info("starting periodic chain verification", "interval", interval, "url", auditServiceURL)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run once at startup
	a.verifyChainFromService(auditServiceURL)

	for range ticker.C {
		a.verifyChainFromService(auditServiceURL)
	}
}

// verifyChainFromService calls the audit service's /v1/verify endpoint.
func (a *Auditor) verifyChainFromService(auditServiceURL string) {
	url := strings.TrimSuffix(auditServiceURL, "/") + "/v1/verify"

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		slog.Error("failed to verify chain", "url", url, "err", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("chain verification request failed", "status", resp.StatusCode)
		return
	}

	var status audit.ChainStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		slog.Error("failed to parse verification response", "err", err)
		return
	}

	if status.Valid {
		slog.Info("periodic chain verification passed",
			"total_events", status.TotalEvents,
			"hashed_events", status.HashedEvents,
			"legacy_events", status.LegacyEvents)
	} else {
		// Chain is broken - this is a critical security alert
		slog.Error("CHAIN INTEGRITY VIOLATION DETECTED",
			"broken_at", status.BrokenAt,
			"error", status.Error,
			"total_events", status.TotalEvents)

		// Create a synthetic event for alerting
		syntheticEvent := &audit.Event{
			EventID:   fmt.Sprintf("verify_%d", time.Now().Unix()),
			Timestamp: time.Now(),
			EventType: "security_alert",
			TraceID:   "periodic_verification",
		}

		a.recordSecurityAlert("chain_tampering", AlertCritical,
			"AUDIT CHAIN TAMPERING DETECTED - Periodic verification failed",
			syntheticEvent,
			"broken_at_index", status.BrokenAt,
			"error", status.Error,
			"total_events", status.TotalEvents,
			"hashed_events", status.HashedEvents)
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

// runHTTPPollingMode polls the audit service HTTP API for new events.
// It is used when the Unix socket is unavailable but -audit-service is set.
func runHTTPPollingMode(cfg Config, auditor *Auditor) {
	baseURL := strings.TrimSuffix(cfg.AuditServiceURL, "/")
	client := &http.Client{Timeout: 15 * time.Second}
	pollInterval := 5 * time.Second

	// Fetch an initial batch of recent events.
	events, err := fetchEventsHTTP(client, baseURL, time.Time{}, 50)
	if err != nil {
		slog.Warn("initial HTTP fetch failed", "err", err)
	} else {
		// API returns newest-first; reverse for chronological display.
		for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
			events[i], events[j] = events[j], events[i]
		}
		for i := range events {
			auditor.Analyze(&events[i])
		}
	}

	// Track the newest timestamp and IDs seen so far.
	var latestSeen time.Time
	seenIDs := make(map[string]bool, 200)
	for _, e := range events {
		seenIDs[e.EventID] = true
		if e.Timestamp.After(latestSeen) {
			latestSeen = e.Timestamp
		}
	}

	slog.Info("polling for new events", "interval", pollInterval, "url", baseURL)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for range ticker.C {
		since := latestSeen
		if since.IsZero() {
			since = time.Now().UTC().Add(-time.Minute)
		}

		newEvents, err := fetchEventsHTTP(client, baseURL, since, 200)
		if err != nil {
			slog.Warn("HTTP poll failed", "err", err)
			continue
		}

		// Filter out already-seen events (the API uses >= so the boundary event reappears).
		var toDisplay []audit.Event
		for _, e := range newEvents {
			if !seenIDs[e.EventID] {
				toDisplay = append(toDisplay, e)
			}
			seenIDs[e.EventID] = true
			if e.Timestamp.After(latestSeen) {
				latestSeen = e.Timestamp
			}
		}

		// Reverse to display oldest-first.
		for i, j := 0, len(toDisplay)-1; i < j; i, j = i+1, j-1 {
			toDisplay[i], toDisplay[j] = toDisplay[j], toDisplay[i]
		}
		for i := range toDisplay {
			auditor.Analyze(&toDisplay[i])
		}

		// Prevent unbounded growth of the dedup set.
		if len(seenIDs) > 2000 {
			seenIDs = make(map[string]bool, 200)
		}
	}
}

// fetchEventsHTTP retrieves audit events from the HTTP API.
// Pass a zero since to fetch the most recent events without a time filter.
func fetchEventsHTTP(client *http.Client, baseURL string, since time.Time, limit int) ([]audit.Event, error) {
	u := fmt.Sprintf("%s/v1/events?limit=%d", baseURL, limit)
	if !since.IsZero() {
		// Use UTC so the timestamp ends in "Z" — no "+" sign that needs URL-encoding.
		u += "&since=" + since.UTC().Format(time.RFC3339)
	}

	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("audit service returned HTTP %d", resp.StatusCode)
	}

	var events []audit.Event
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}
	return events, nil
}
