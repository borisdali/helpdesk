// Package main implements the central audit service daemon.
// All helpdesk components send audit events here via HTTP.
// This service owns the SQLite database and maintains hash chain integrity.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/authz"
	"helpdesk/internal/buildinfo"
	"helpdesk/internal/identity"
	"helpdesk/internal/logging"
	"helpdesk/playbooks"
)

type config struct {
	listenAddr string
	dbPath     string
	socketPath string
	usersFile  string // optional; enables role-based auth on approve/deny/cancel

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
	flag.StringVar(&cfg.usersFile, "users-file", envOrDefault("HELPDESK_USERS_FILE", ""), "Path to users.yaml for role-based auth on approve/deny endpoints (optional)")

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
	approvalStore, err := audit.NewApprovalStore(store.DB(), store.IsPostgres())
	if err != nil {
		slog.Error("failed to create approval store", "err", err)
		os.Exit(1)
	}

	// Create govbot store (shares the same database connection)
	govbotStore, err := audit.NewGovbotStore(store.DB(), store.IsPostgres())
	if err != nil {
		slog.Error("failed to create govbot store", "err", err)
		os.Exit(1)
	}

	// Create fleet store (shares the same database connection)
	fleetStore, err := audit.NewFleetStore(store.DB(), store.IsPostgres())
	if err != nil {
		slog.Error("failed to create fleet store", "err", err)
		os.Exit(1)
	}

	// Create playbook store (shares the same database connection)
	playbookStore, err := audit.NewPlaybookStore(store.DB(), store.IsPostgres())
	if err != nil {
		slog.Error("failed to create playbook store", "err", err)
		os.Exit(1)
	}

	// Seed system playbooks (idempotent; non-fatal if it fails).
	if err := playbooks.SeedSystemPlaybooks(context.Background(), playbookStore); err != nil {
		slog.Warn("failed to seed system playbooks", "err", err)
	}

	// Create upload store (shares the same database connection)
	uploadStore, err := audit.NewUploadStore(store.DB())
	if err != nil {
		slog.Error("failed to create upload store", "err", err)
		os.Exit(1)
	}

	// Create tool result store (shares the same database connection)
	toolResultStore, err := audit.NewToolResultStore(store.DB(), store.IsPostgres())
	if err != nil {
		slog.Error("failed to create tool result store", "err", err)
		os.Exit(1)
	}

	// Create rollback store (shares the same database connection)
	rollbackStore, err := audit.NewRollbackStore(store.DB(), store.IsPostgres())
	if err != nil {
		slog.Error("failed to create rollback store", "err", err)
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

	// Build identity provider. Defaults to NoAuthProvider (dev mode) when no
	// users file is configured. StaticProvider enables role-based auth.
	var idProvider identity.Provider = &identity.NoAuthProvider{}
	enforcing := cfg.usersFile != ""
	if cfg.usersFile != "" {
		p, err := identity.NewStaticProvider(cfg.usersFile)
		if err != nil {
			slog.Error("failed to load users file", "path", cfg.usersFile, "err", err)
			os.Exit(1)
		}
		idProvider = p
		slog.Info("role-based authorization enabled", "users_file", cfg.usersFile)
	}

	// Build central authorizer.
	authzr := authz.NewAuthorizer(authz.DefaultAuditdPermissions, enforcing)
	if enforcing {
		slog.Info("authorization enforcing: approval and rollback endpoints require authentication")
	} else {
		slog.Warn("authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control")
	}

	// auth wraps a handler with per-pattern identity resolution and authorization.
	// The pattern is captured at registration time so r.Pattern need not be set.
	auth := func(pattern string, h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			principal, err := idProvider.Resolve(r)
			if err != nil {
				// Bad or unrecognized credential: fall through as anonymous and
				// let Authorize decide. AllowAnonymous routes pass; protected
				// routes still get 401 from the Authorize block below.
				slog.Debug("auth: unrecognized credential, treating as anonymous",
					"pattern", pattern, "err", err)
				principal = identity.ResolvedPrincipal{AuthMethod: "header"}
			}
			if authErr := authzr.Authorize(pattern, principal); authErr != nil {
				status := http.StatusForbidden
				if errors.Is(authErr, authz.ErrUnauthorized) {
					status = http.StatusUnauthorized
				}
				slog.Info("authz: request denied",
					"pattern", pattern,
					"principal", principal.EffectiveID(),
					"anonymous", principal.IsAnonymous(),
					"err", authErr)
				http.Error(w, authErr.Error(), status)
				return
			}
			h(w, r.WithContext(authz.WithPrincipal(r.Context(), principal)))
		}
	}

	srv := &server{store: store}
	approvalSrv := &approvalServer{store: approvalStore, notifier: approvalNotifier, authorizer: authzr}
	govSrv := newGovernanceServer(store, approvalStore, approvalNotifier)
	govbotSrv := &govbotServer{store: govbotStore}
	fleetSrv := &fleetServer{store: fleetStore, approvalStore: approvalStore}
	playbookSrv := &playbookServer{store: playbookStore}
	uploadSrv := &uploadServer{store: uploadStore}
	toolResultSrv := &toolResultServer{store: toolResultStore}
	rollbackSrv := &rollbackServer{store: rollbackStore, auditStore: store, fleetStore: fleetStore, approvalStore: approvalStore}

	mux := http.NewServeMux()

	// Audit event endpoints
	mux.HandleFunc("POST /v1/events", auth("POST /v1/events", srv.handleRecordEvent))
	mux.HandleFunc("POST /v1/events/{eventID}/outcome", auth("POST /v1/events/{eventID}/outcome", srv.handleRecordOutcome))
	mux.HandleFunc("GET /v1/events", auth("GET /v1/events", srv.handleQueryEvents))
	mux.HandleFunc("GET /v1/verify", auth("GET /v1/verify", srv.handleVerifyChain))

	// Approval endpoints
	mux.HandleFunc("POST /v1/approvals", auth("POST /v1/approvals", approvalSrv.handleCreateApproval))
	mux.HandleFunc("GET /v1/approvals", auth("GET /v1/approvals", approvalSrv.handleListApprovals))
	mux.HandleFunc("GET /v1/approvals/pending", auth("GET /v1/approvals/pending", approvalSrv.handlePendingApprovals))
	mux.HandleFunc("GET /v1/approvals/{approvalID}", auth("GET /v1/approvals/{approvalID}", approvalSrv.handleGetApproval))
	mux.HandleFunc("GET /v1/approvals/{approvalID}/wait", auth("GET /v1/approvals/{approvalID}/wait", approvalSrv.handleWaitForApproval))
	mux.HandleFunc("POST /v1/approvals/{approvalID}/approve", auth("POST /v1/approvals/{approvalID}/approve", approvalSrv.handleApprove))
	mux.HandleFunc("POST /v1/approvals/{approvalID}/deny", auth("POST /v1/approvals/{approvalID}/deny", approvalSrv.handleDeny))
	mux.HandleFunc("POST /v1/approvals/{approvalID}/cancel", auth("POST /v1/approvals/{approvalID}/cancel", approvalSrv.handleCancel))

	// Governance endpoints
	mux.HandleFunc("GET /v1/governance/info", auth("GET /v1/governance/info", govSrv.handleGetInfo))
	mux.HandleFunc("GET /v1/governance/policies", auth("GET /v1/governance/policies", govSrv.handleGetPolicySummary))
	mux.HandleFunc("GET /v1/governance/explain", auth("GET /v1/governance/explain", govSrv.handleExplain))
	mux.HandleFunc("POST /v1/governance/check", auth("POST /v1/governance/check", govSrv.handlePolicyCheck))
	mux.HandleFunc("GET /v1/events/{eventID}", auth("GET /v1/events/{eventID}", govSrv.handleGetEvent))

	// Journey endpoint
	mux.HandleFunc("GET /v1/journeys", auth("GET /v1/journeys", srv.handleQueryJourneys))

	// Govbot compliance history endpoints
	mux.HandleFunc("POST /v1/govbot/runs", auth("POST /v1/govbot/runs", govbotSrv.handleSaveRun))
	mux.HandleFunc("GET /v1/govbot/runs", auth("GET /v1/govbot/runs", govbotSrv.handleGetRuns))

	// Fleet runner job tracking endpoints
	mux.HandleFunc("POST /v1/fleet/jobs", auth("POST /v1/fleet/jobs", fleetSrv.handleCreateJob))
	mux.HandleFunc("GET /v1/fleet/jobs", auth("GET /v1/fleet/jobs", fleetSrv.handleListJobs))
	mux.HandleFunc("GET /v1/fleet/jobs/{jobID}", auth("GET /v1/fleet/jobs/{jobID}", fleetSrv.handleGetJob))
	mux.HandleFunc("PATCH /v1/fleet/jobs/{jobID}/status", auth("PATCH /v1/fleet/jobs/{jobID}/status", fleetSrv.handleUpdateStatus))
	mux.HandleFunc("POST /v1/fleet/jobs/{jobID}/servers", auth("POST /v1/fleet/jobs/{jobID}/servers", fleetSrv.handleAddServer))
	mux.HandleFunc("PATCH /v1/fleet/jobs/{jobID}/servers/{serverName}", auth("PATCH /v1/fleet/jobs/{jobID}/servers/{serverName}", fleetSrv.handleUpdateServer))
	mux.HandleFunc("GET /v1/fleet/jobs/{jobID}/servers", auth("GET /v1/fleet/jobs/{jobID}/servers", fleetSrv.handleGetServers))
	mux.HandleFunc("GET /v1/fleet/jobs/{jobID}/servers/{serverName}", auth("GET /v1/fleet/jobs/{jobID}/servers/{serverName}", fleetSrv.handleGetServer))
	mux.HandleFunc("POST /v1/fleet/jobs/{jobID}/servers/{serverName}/steps", auth("POST /v1/fleet/jobs/{jobID}/servers/{serverName}/steps", fleetSrv.handleAddServerStep))
	mux.HandleFunc("PATCH /v1/fleet/jobs/{jobID}/servers/{serverName}/steps/{stepIndex}", auth("PATCH /v1/fleet/jobs/{jobID}/servers/{serverName}/steps/{stepIndex}", fleetSrv.handleUpdateServerStep))
	mux.HandleFunc("GET /v1/fleet/jobs/{jobID}/servers/{serverName}/steps", auth("GET /v1/fleet/jobs/{jobID}/servers/{serverName}/steps", fleetSrv.handleGetServerSteps))

	// Fleet playbook endpoints
	mux.HandleFunc("POST /v1/fleet/playbooks", auth("POST /v1/fleet/playbooks", playbookSrv.handleCreate))
	mux.HandleFunc("GET /v1/fleet/playbooks", auth("GET /v1/fleet/playbooks", playbookSrv.handleList))
	mux.HandleFunc("GET /v1/fleet/playbooks/{playbookID}", auth("GET /v1/fleet/playbooks/{playbookID}", playbookSrv.handleGet))
	mux.HandleFunc("PUT /v1/fleet/playbooks/{playbookID}", auth("PUT /v1/fleet/playbooks/{playbookID}", playbookSrv.handleUpdate))
	mux.HandleFunc("DELETE /v1/fleet/playbooks/{playbookID}", auth("DELETE /v1/fleet/playbooks/{playbookID}", playbookSrv.handleDelete))
	mux.HandleFunc("POST /v1/fleet/playbooks/{playbookID}/activate", auth("POST /v1/fleet/playbooks/{playbookID}/activate", playbookSrv.handleActivate))

	// Tool result endpoints
	// Upload endpoints
	mux.HandleFunc("POST /v1/uploads", auth("POST /v1/uploads", uploadSrv.handleCreate))
	mux.HandleFunc("GET /v1/uploads/{uploadID}", auth("GET /v1/uploads/{uploadID}", uploadSrv.handleGet))
	mux.HandleFunc("GET /v1/uploads/{uploadID}/content", auth("GET /v1/uploads/{uploadID}/content", uploadSrv.handleGetContent))

	mux.HandleFunc("POST /v1/tool-results", auth("POST /v1/tool-results", toolResultSrv.handleRecord))
	mux.HandleFunc("GET /v1/tool-results", auth("GET /v1/tool-results", toolResultSrv.handleList))

	// Fleet job approval endpoints
	mux.HandleFunc("POST /v1/fleet/jobs/{jobID}/approval", auth("POST /v1/fleet/jobs/{jobID}/approval", fleetSrv.handleCreateJobApproval))
	mux.HandleFunc("GET /v1/fleet/jobs/{jobID}/approval/{approvalID}", auth("GET /v1/fleet/jobs/{jobID}/approval/{approvalID}", fleetSrv.handleGetJobApproval))

	// Rollback & Undo endpoints
	mux.HandleFunc("POST /v1/rollbacks", auth("POST /v1/rollbacks", rollbackSrv.handleInitiateRollback))
	mux.HandleFunc("GET /v1/rollbacks", auth("GET /v1/rollbacks", rollbackSrv.handleListRollbacks))
	mux.HandleFunc("GET /v1/rollbacks/{rollbackID}", auth("GET /v1/rollbacks/{rollbackID}", rollbackSrv.handleGetRollback))
	mux.HandleFunc("POST /v1/rollbacks/{rollbackID}/cancel", auth("POST /v1/rollbacks/{rollbackID}/cancel", rollbackSrv.handleCancelRollback))
	mux.HandleFunc("POST /v1/events/{eventID}/rollback-plan", auth("POST /v1/events/{eventID}/rollback-plan", rollbackSrv.handleDeriveRollbackPlan))
	mux.HandleFunc("POST /v1/fleet/jobs/{jobID}/rollback", auth("POST /v1/fleet/jobs/{jobID}/rollback", rollbackSrv.handleInitiateFleetRollback))
	mux.HandleFunc("GET /v1/fleet/jobs/{jobID}/rollback", auth("GET /v1/fleet/jobs/{jobID}/rollback", rollbackSrv.handleGetFleetRollback))

	// Health endpoint
	mux.HandleFunc("GET /health", auth("GET /health", srv.handleHealth))

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

	backend := "sqlite"
	if store.IsPostgres() {
		backend = "postgres"
	}
	slog.Info("audit service starting",
		"version", buildinfo.Version,
		"listen", cfg.listenAddr,
		"db", cfg.dbPath,
		"backend", backend,
		"socket", cfg.socketPath,
		"authz_enforcing", enforcing)

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

	// Log policy decisions at an appropriate level so denials are visible in the
	// auditd log alongside the explain-endpoint decisions.
	if event.PolicyDecision != nil {
		pd := event.PolicyDecision
		attrs := []any{
			"event_id", event.EventID,
			"action", pd.Action,
			"resource_type", pd.ResourceType,
			"resource_name", pd.ResourceName,
			"effect", pd.Effect,
			"policy", pd.PolicyName,
		}
		if pd.Message != "" {
			attrs = append(attrs, "message", pd.Message)
		}
		switch pd.Effect {
		case "deny":
			slog.Warn("policy decision recorded: DENY", attrs...)
		case "require_approval":
			slog.Info("policy decision recorded: REQUIRE_APPROVAL", attrs...)
		default:
			slog.Debug("policy decision recorded: ALLOW", attrs...)
		}
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

	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.Limit = n
		}
	}

	if v := r.URL.Query().Get("session_id"); v != "" {
		opts.SessionID = v
	}
	if v := r.URL.Query().Get("trace_id"); v != "" {
		opts.TraceID = v
	}
	if v := r.URL.Query().Get("trace_id_prefix"); v != "" {
		opts.TraceIDPrefix = v
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
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			opts.Since = t
		} else if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.Since = t
		}
	}
	if v := r.URL.Query().Get("outcome_status"); v != "" {
		opts.OutcomeStatus = v
	}
	if v := r.URL.Query().Get("origin"); v != "" {
		opts.Origin = v
	}
	if v := r.URL.Query().Get("tool_name"); v != "" {
		opts.ToolName = v
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

func (s *server) handleQueryJourneys(w http.ResponseWriter, r *http.Request) {
	opts := audit.JourneyOptions{Limit: 50}

	q := r.URL.Query()
	if v := q.Get("user"); v != "" {
		opts.UserID = v
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.From = t
		}
	}
	if v := q.Get("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.Until = t
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.Limit = n
		}
	}
	if v := q.Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			opts.Since = d
		}
	}
	if v := q.Get("purpose"); v != "" {
		opts.Purpose = v
	}
	if v := q.Get("category"); v != "" {
		opts.Category = v
	}
	if v := q.Get("outcome"); v != "" {
		opts.Outcome = v
	}
	if q.Get("has_retries") == "true" {
		opts.HasRetries = true
	}
	if v := q.Get("trace_id"); v != "" {
		opts.TraceID = v
	}
	if v := q.Get("trace_id_prefix"); v != "" {
		opts.TraceIDPrefix = v
	}
	if v := q.Get("origin"); v != "" {
		opts.Origin = v
	}

	journeys, err := s.store.QueryJourneys(r.Context(), opts)
	if err != nil {
		slog.Error("failed to query journeys", "err", err)
		http.Error(w, "failed to query journeys", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(journeys)
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
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": buildinfo.Version})
}

// envOrDefault returns the value of the environment variable named by key,
// or def if the variable is not set or empty.
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
