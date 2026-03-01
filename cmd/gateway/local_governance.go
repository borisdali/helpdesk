package main

// local_governance.go provides an embedded HTTP governance server for the
// gateway's local audit mode (HELPDESK_AUDIT_DIR).
//
// When the gateway owns its own SQLite store (no external auditd), this
// handler is started on a loopback port and wired into the gateway via
// SetAuditURL so that proxyGovernanceRequest works identically to remote mode.

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"time"

	"helpdesk/internal/audit"
)

// startLocalGovernanceServer starts a minimal governance HTTP server backed by
// the given local store. It listens on a random loopback port and returns the
// base URL (e.g. "http://127.0.0.1:54321").
func startLocalGovernanceServer(store *audit.Store) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/governance/info", localHandleInfo(store))
	mux.HandleFunc("GET /v1/events", localHandleEvents(store))
	mux.HandleFunc("GET /v1/events/{eventID}", localHandleEvent(store))
	mux.HandleFunc("GET /v1/verify", localHandleVerify(store))
	// Approvals are not stored in local/legacy mode — return empty lists.
	mux.HandleFunc("GET /v1/approvals", localHandleEmpty)
	mux.HandleFunc("GET /v1/approvals/pending", localHandleEmpty)
	// Policy and explain endpoints require a policy engine — not available in local mode.
	mux.HandleFunc("GET /v1/governance/policies", localHandleUnavailable("policy engine not available in gateway local mode"))
	mux.HandleFunc("GET /v1/governance/explain", localHandleUnavailable("explain not available in gateway local mode"))
	mux.HandleFunc("GET /v1/journeys", localHandleJourneys(store))

	go http.Serve(ln, mux) //nolint:errcheck
	return "http://" + ln.Addr().String(), nil
}

func localHandleInfo(store *audit.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		backend := "sqlite"
		if store.IsPostgres() {
			backend = "postgres"
		}

		auditInfo := map[string]any{
			"enabled": true,
			"backend": backend,
		}

		if status, err := store.VerifyIntegrity(r.Context()); err == nil {
			auditInfo["events_total"] = status.TotalEvents
			auditInfo["chain_valid"] = status.Valid
		}

		if events, err := store.Query(r.Context(), audit.QueryOptions{Limit: 1}); err == nil && len(events) > 0 {
			auditInfo["last_event_at"] = events[0].Timestamp.Format(time.RFC3339)
		}

		writeLocalJSON(w, map[string]any{
			"audit":     auditInfo,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
	}
}

func localHandleEvents(store *audit.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		opts := audit.QueryOptions{Limit: 100}

		if v := q.Get("session_id"); v != "" {
			opts.SessionID = v
		}
		if v := q.Get("trace_id"); v != "" {
			opts.TraceID = v
		}
		if v := q.Get("trace_id_prefix"); v != "" {
			opts.TraceIDPrefix = v
		}
		if v := q.Get("event_type"); v != "" {
			opts.EventType = audit.EventType(v)
		}
		if v := q.Get("agent"); v != "" {
			opts.Agent = v
		}
		if v := q.Get("action_class"); v != "" {
			opts.ActionClass = audit.ActionClass(v)
		}
		if v := q.Get("since"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				opts.Since = t
			}
		}
		if v := q.Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				opts.Limit = n
			}
		}

		events, err := store.Query(r.Context(), opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if events == nil {
			events = []audit.Event{}
		}
		writeLocalJSON(w, events)
	}
}

func localHandleEvent(store *audit.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		eventID := r.PathValue("eventID")
		events, err := store.Query(r.Context(), audit.QueryOptions{EventID: eventID})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(events) == 0 {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}
		writeLocalJSON(w, events[0])
	}
}

func localHandleVerify(store *audit.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, err := store.VerifyIntegrity(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeLocalJSON(w, status)
	}
}

func localHandleJourneys(store *audit.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		opts := audit.JourneyOptions{Limit: 50}

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

		journeys, err := store.QueryJourneys(r.Context(), opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeLocalJSON(w, journeys)
	}
}

func localHandleEmpty(w http.ResponseWriter, _ *http.Request) {
	writeLocalJSON(w, []any{})
}

func localHandleUnavailable(msg string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
	}
}

func writeLocalJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
