// Package main implements the real-time audit agent that monitors delegation
// decisions and alerts on suspicious patterns.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/logging"
)

func main() {
	socketPath := flag.String("socket", "audit.sock", "Path to audit Unix socket")
	logAll := flag.Bool("log-all", false, "Log all events, not just alerts")
	flag.Parse()

	logging.InitLogging(os.Args[1:])

	slog.Info("starting auditor", "socket", *socketPath, "log_all", *logAll)

	// Connect to the audit socket
	conn, err := net.Dial("unix", *socketPath)
	if err != nil {
		slog.Error("failed to connect to audit socket", "path", *socketPath, "err", err)
		slog.Info("hint: ensure the orchestrator is running with HELPDESK_AUDIT_ENABLED=true")
		os.Exit(1)
	}
	defer conn.Close()

	slog.Info("connected to audit socket, monitoring events...")

	auditor := NewAuditor(*logAll)
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

// Auditor analyzes events and detects suspicious patterns.
type Auditor struct {
	// Track recent events for pattern analysis
	recentEvents    []audit.Event
	agentErrorCount map[string]int
	agentCallCount  map[string]int
	sessionQueries  map[string][]string
	logAll          bool
}

// NewAuditor creates a new auditor with initialized state.
func NewAuditor(logAll bool) *Auditor {
	return &Auditor{
		recentEvents:    make([]audit.Event, 0, 100),
		agentErrorCount: make(map[string]int),
		agentCallCount:  make(map[string]int),
		sessionQueries:  make(map[string][]string),
		logAll:          logAll,
	}
}

// Analyze checks an event against detection rules.
func (a *Auditor) Analyze(event *audit.Event) {
	slog.Debug("analyzing event", "event_id", event.EventID, "type", event.EventType)

	// Log all events if requested
	if a.logAll {
		a.logEvent(event)
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
	attrs := []any{
		"event_id", event.EventID,
		"session_id", event.Session.ID,
		"user_id", event.Session.UserID,
	}
	attrs = append(attrs, keyvals...)

	// Format as visible alert
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
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
