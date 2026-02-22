// Package main implements the central audit service daemon.
// All helpdesk components send audit events here via HTTP.
// This service owns the SQLite database and maintains hash chain integrity.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/logging"
)

type config struct {
	listenAddr string
	dbPath     string
	socketPath string

	// Approval notification configuration
	approvalWebhook  string
	smtpHost         string
	smtpPort         string
	smtpUser         string
	smtpPassword     string
	emailFrom        string
	emailTo          string
}

func main() {
	var cfg config
	flag.StringVar(&cfg.listenAddr, "listen", envOrDefault("HELPDESK_AUDIT_ADDR", ":1199"), "HTTP listen address")
	flag.StringVar(&cfg.dbPath, "db", envOrDefault("HELPDESK_AUDIT_DB", "audit.db"), "Path to SQLite database")
	flag.StringVar(&cfg.socketPath, "socket", envOrDefault("HELPDESK_AUDIT_SOCKET", "/tmp/helpdesk-audit.sock"), "Unix socket for real-time notifications")

	// Approval notification flags
	flag.StringVar(&cfg.approvalWebhook, "approval-webhook", envOrDefault("HELPDESK_APPROVAL_WEBHOOK", ""), "Webhook URL for approval notifications (Slack, etc.)")
	approvalBaseURL := flag.String("approval-base-url", envOrDefault("HELPDESK_APPROVAL_BASE_URL", ""), "Base URL for approve/deny links in emails (e.g., http://localhost:1199)")
	flag.StringVar(&cfg.smtpHost, "smtp-host", envOrDefault("SMTP_HOST", ""), "SMTP server host for approval emails")
	flag.StringVar(&cfg.smtpPort, "smtp-port", envOrDefault("SMTP_PORT", "587"), "SMTP server port")
	flag.StringVar(&cfg.smtpUser, "smtp-user", envOrDefault("SMTP_USER", ""), "SMTP username")
	flag.StringVar(&cfg.smtpPassword, "smtp-password", "", "SMTP password (or use SMTP_PASSWORD env)")
	flag.StringVar(&cfg.emailFrom, "email-from", envOrDefault("HELPDESK_EMAIL_FROM", ""), "Email sender address for approvals")
	flag.StringVar(&cfg.emailTo, "email-to", envOrDefault("HELPDESK_EMAIL_TO", ""), "Email recipients for approvals (comma-separated)")

	// InitLogging must run before flag.Parse so it can strip --log-level before
	// the flag package sees it (mirroring auditor, approvals, gateway, helpdesk).
	remaining := logging.InitLogging(os.Args[1:])
	flag.CommandLine.Parse(remaining) //nolint:errcheck

	// Allow SMTP password from environment
	if cfg.smtpPassword == "" {
		cfg.smtpPassword = os.Getenv("SMTP_PASSWORD")
	}

	store, err := audit.NewStore(audit.StoreConfig{
		DBPath:     cfg.dbPath,
		SocketPath: cfg.socketPath,
	})
	if err != nil {
		slog.Error("failed to create audit store", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	// Create approval store (shares the same database connection)
	approvalStore, err := audit.NewApprovalStore(store.DB())
	if err != nil {
		slog.Error("failed to create approval store", "err", err)
		os.Exit(1)
	}

	// Create approval notifier if configured
	// Default baseURL to the listen address if not specified
	baseURL := *approvalBaseURL
	if baseURL == "" && cfg.smtpHost != "" {
		baseURL = "http://localhost" + cfg.listenAddr
	}

	approvalNotifier := NewApprovalNotifier(ApprovalNotifierConfig{
		WebhookURL:   cfg.approvalWebhook,
		BaseURL:      baseURL,
		SMTPHost:     cfg.smtpHost,
		SMTPPort:     cfg.smtpPort,
		SMTPUser:     cfg.smtpUser,
		SMTPPassword: cfg.smtpPassword,
		EmailFrom:    cfg.emailFrom,
		EmailTo:      cfg.emailTo,
	})
	if approvalNotifier.IsEnabled() {
		slog.Info("approval notifications enabled",
			"webhook", cfg.approvalWebhook != "",
			"email", cfg.smtpHost != "" && cfg.emailTo != "")
	}

	srv := &server{store: store}
	approvalSrv := &approvalServer{store: approvalStore, notifier: approvalNotifier}
	govSrv := newGovernanceServer(store, approvalStore, approvalNotifier)

	mux := http.NewServeMux()

	// Audit event endpoints
	mux.HandleFunc("POST /v1/events", srv.handleRecordEvent)
	mux.HandleFunc("POST /v1/events/{eventID}/outcome", srv.handleRecordOutcome)
	mux.HandleFunc("GET /v1/events", srv.handleQueryEvents)
	mux.HandleFunc("GET /v1/verify", srv.handleVerifyChain)

	// Approval endpoints
	mux.HandleFunc("POST /v1/approvals", approvalSrv.handleCreateApproval)
	mux.HandleFunc("GET /v1/approvals", approvalSrv.handleListApprovals)
	mux.HandleFunc("GET /v1/approvals/pending", approvalSrv.handlePendingApprovals)
	mux.HandleFunc("GET /v1/approvals/{approvalID}", approvalSrv.handleGetApproval)
	mux.HandleFunc("GET /v1/approvals/{approvalID}/wait", approvalSrv.handleWaitForApproval)
	mux.HandleFunc("POST /v1/approvals/{approvalID}/approve", approvalSrv.handleApprove)
	mux.HandleFunc("POST /v1/approvals/{approvalID}/deny", approvalSrv.handleDeny)
	mux.HandleFunc("POST /v1/approvals/{approvalID}/cancel", approvalSrv.handleCancel)

	// Governance endpoints
	mux.HandleFunc("GET /v1/governance/info", govSrv.handleGetInfo)
	mux.HandleFunc("GET /v1/governance/policies", govSrv.handleGetPolicySummary)
	mux.HandleFunc("GET /v1/governance/explain", govSrv.handleExplain)
	mux.HandleFunc("GET /v1/events/{eventID}", govSrv.handleGetEvent)

	// Health endpoint
	mux.HandleFunc("GET /health", srv.handleHealth)

	httpServer := &http.Server{
		Addr:         cfg.listenAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	// Graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start background workers
	go approvalSrv.startExpirationWorker(ctx)

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Info("shutting down audit service...")
		cancel()
		httpServer.Shutdown(context.Background())
	}()

	slog.Info("audit service starting",
		"listen", cfg.listenAddr,
		"db", cfg.dbPath,
		"socket", cfg.socketPath)

	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}

	_ = ctx // silence unused warning
	slog.Info("audit service stopped")
}

type server struct {
	store *audit.Store
}

func (s *server) handleRecordEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var event audit.Event
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.store.Record(r.Context(), &event); err != nil {
		slog.Error("failed to record event", "err", err)
		http.Error(w, "failed to record event", http.StatusInternalServerError)
		return
	}

	// Return the event with computed hashes
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"event_id":   event.EventID,
		"event_hash": event.EventHash,
		"prev_hash":  event.PrevHash,
	})
}

func (s *server) handleRecordOutcome(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("eventID")
	if eventID == "" {
		http.Error(w, "missing event ID", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var outcome audit.Outcome
	if err := json.Unmarshal(body, &outcome); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.store.RecordOutcome(r.Context(), eventID, &outcome); err != nil {
		slog.Error("failed to record outcome", "err", err, "event_id", eventID)
		http.Error(w, "failed to record outcome", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *server) handleQueryEvents(w http.ResponseWriter, r *http.Request) {
	opts := audit.QueryOptions{
		Limit: 100,
	}

	if v := r.URL.Query().Get("session_id"); v != "" {
		opts.SessionID = v
	}
	if v := r.URL.Query().Get("trace_id"); v != "" {
		opts.TraceID = v
	}
	if v := r.URL.Query().Get("event_type"); v != "" {
		opts.EventType = audit.EventType(v)
	}
	if v := r.URL.Query().Get("agent"); v != "" {
		opts.Agent = v
	}
	if v := r.URL.Query().Get("action_class"); v != "" {
		opts.ActionClass = audit.ActionClass(v)
	}
	if v := r.URL.Query().Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.Since = t
		}
	}

	events, err := s.store.Query(r.Context(), opts)
	if err != nil {
		slog.Error("failed to query events", "err", err)
		http.Error(w, "failed to query events", http.StatusInternalServerError)
		return
	}

	if events == nil {
		events = []audit.Event{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}

func (s *server) handleVerifyChain(w http.ResponseWriter, r *http.Request) {
	status, err := s.store.VerifyIntegrity(r.Context())
	if err != nil {
		slog.Error("failed to verify chain", "err", err)
		http.Error(w, "failed to verify chain", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// envOrDefault returns the value of the environment variable named by key,
// or def if the variable is not set or empty.
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
