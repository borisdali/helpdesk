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
}

func main() {
	var cfg config
	flag.StringVar(&cfg.listenAddr, "listen", ":1199", "HTTP listen address")
	flag.StringVar(&cfg.dbPath, "db", "audit.db", "Path to SQLite database")
	flag.StringVar(&cfg.socketPath, "socket", "/tmp/helpdesk-audit.sock", "Unix socket for real-time notifications")
	flag.Parse()

	logging.InitLogging(os.Args[1:])

	store, err := audit.NewStore(audit.StoreConfig{
		DBPath:     cfg.dbPath,
		SocketPath: cfg.socketPath,
	})
	if err != nil {
		slog.Error("failed to create audit store", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	srv := &server{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/events", srv.handleRecordEvent)
	mux.HandleFunc("POST /v1/events/{eventID}/outcome", srv.handleRecordOutcome)
	mux.HandleFunc("GET /v1/events", srv.handleQueryEvents)
	mux.HandleFunc("GET /v1/verify", srv.handleVerifyChain)
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
