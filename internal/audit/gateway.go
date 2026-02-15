package audit

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// GatewayAuditor provides audit logging for gateway requests.
type GatewayAuditor struct {
	store *Store
}

// NewGatewayAuditor creates a new gateway auditor.
// If store is nil, auditing is disabled (no-op).
func NewGatewayAuditor(store *Store) *GatewayAuditor {
	return &GatewayAuditor{store: store}
}

// RecordRequest records a gateway request to the audit store.
func (a *GatewayAuditor) RecordRequest(ctx context.Context, req *GatewayRequest) error {
	if a.store == nil {
		return nil
	}

	event := &Event{
		EventID:   "gw_" + uuid.New().String()[:8],
		Timestamp: req.StartTime,
		EventType: EventTypeGatewayRequest,
		Session: Session{
			ID: req.RequestID,
		},
		Input: Input{
			UserQuery: req.Message,
		},
		Output: &Output{
			Response: req.Response,
		},
		Decision: &Decision{
			Agent:           req.Agent,
			RequestCategory: categorizeAgent(req.Agent),
			Confidence:      1.0, // Gateway routing is deterministic
			UserIntent:      req.Endpoint,
		},
		Outcome: &Outcome{
			Status:       req.Status,
			ErrorMessage: req.Error,
			Duration:     req.Duration,
		},
	}

	if err := a.store.Record(ctx, event); err != nil {
		slog.Warn("failed to record gateway audit event", "error", err)
		return err
	}

	return nil
}

// GatewayRequest contains the data for a gateway audit event.
type GatewayRequest struct {
	RequestID string
	Endpoint  string
	Method    string
	Agent     string
	Message   string
	Response  string // Agent's response text
	StartTime time.Time
	Duration  time.Duration
	Status    string // "success" or "error"
	Error     string
	HTTPCode  int
}

// categorizeAgent maps agent names to request categories.
func categorizeAgent(agent string) RequestCategory {
	switch agent {
	case "postgres_database_agent":
		return CategoryDatabase
	case "k8s_agent":
		return CategoryKubernetes
	case "incident_agent":
		return CategoryIncident
	case "research_agent":
		return CategoryResearch
	default:
		return CategoryUnknown
	}
}

// AuditMiddleware wraps an http.Handler to record audit events.
func (a *GatewayAuditor) AuditMiddleware(next http.Handler) http.Handler {
	if a.store == nil {
		return next // No-op if auditing disabled
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestID := uuid.New().String()[:8]

		// Wrap response writer to capture status code
		wrapped := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		// Call the actual handler
		next.ServeHTTP(wrapped, r)

		// Record the request (we don't have agent/message info here, just basic HTTP)
		duration := time.Since(start)
		status := "success"
		if wrapped.status >= 400 {
			status = "error"
		}

		event := &Event{
			EventID:   "gw_" + requestID,
			Timestamp: start,
			EventType: EventTypeGatewayRequest,
			Session: Session{
				ID: requestID,
			},
			Input: Input{
				UserQuery: r.URL.Path,
			},
			Decision: &Decision{
				UserIntent: r.Method + " " + r.URL.Path,
				Confidence: 1.0,
			},
			Outcome: &Outcome{
				Status:   status,
				Duration: duration,
			},
		}

		if err := a.store.Record(r.Context(), event); err != nil {
			slog.Debug("failed to record gateway audit", "error", err)
		}
	})
}

// statusRecorder wraps http.ResponseWriter to capture the status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
