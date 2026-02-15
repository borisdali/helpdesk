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
	store           *Store
	approvalManager *ApprovalManager
}

// NewGatewayAuditor creates a new gateway auditor.
// If store is nil, auditing is disabled (no-op).
func NewGatewayAuditor(store *Store) *GatewayAuditor {
	return &GatewayAuditor{
		store:           store,
		approvalManager: NewApprovalManager(nil), // Use default policy
	}
}

// NewGatewayAuditorWithPolicy creates a gateway auditor with a custom approval policy.
func NewGatewayAuditorWithPolicy(store *Store, policy *ApprovalPolicy) *GatewayAuditor {
	return &GatewayAuditor{
		store:           store,
		approvalManager: NewApprovalManager(policy),
	}
}

// RecordRequest records a gateway request to the audit store.
func (a *GatewayAuditor) RecordRequest(ctx context.Context, req *GatewayRequest) error {
	if a.store == nil {
		return nil
	}

	// Generate trace ID if not provided
	traceID := req.TraceID
	if traceID == "" {
		traceID = NewTraceID()
	}

	// Determine action class
	actionClass := req.ActionClass
	if actionClass == "" {
		if req.ToolName != "" {
			actionClass = ClassifyTool(req.ToolName)
		} else {
			actionClass = ClassifyEndpoint(req.Method, req.Endpoint)
		}
	}

	// Build tool execution if we have tool info
	var toolExec *ToolExecution
	if req.ToolName != "" {
		toolExec = &ToolExecution{
			Name:       req.ToolName,
			Parameters: req.ToolParameters,
			Result:     truncateString(req.Response, 500), // Summary of result
			Duration:   req.Duration,
		}
		if req.Status == "error" {
			toolExec.Error = req.Error
		}
	}

	// Evaluate approval policy
	var approval *Approval
	if a.approvalManager != nil {
		approval = a.approvalManager.CheckApproval(ApprovalRequest{
			ActionClass: actionClass,
			Agent:       req.Agent,
			Tool:        req.ToolName,
			Environment: req.Environment,
			Principal:   req.Principal,
		})
	}

	event := &Event{
		EventID:     "gw_" + uuid.New().String()[:8],
		Timestamp:   req.StartTime,
		EventType:   EventTypeGatewayRequest,
		TraceID:     traceID,
		ParentID:    req.ParentID,
		ActionClass: actionClass,
		Session: Session{
			ID:     req.RequestID,
			UserID: req.Principal,
		},
		Input: Input{
			UserQuery: req.Message,
		},
		Output: &Output{
			Response: req.Response,
		},
		Tool:     toolExec,
		Approval: approval,
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
	RequestID      string
	TraceID        string         // end-to-end trace ID
	ParentID       string         // parent event ID (if this is a child event)
	Principal      string         // authenticated user or API key
	Environment    string         // environment context (e.g., "prod", "staging")
	Endpoint       string
	Method         string
	Agent          string
	ToolName       string         // specific tool being called (e.g., "check_connection")
	ToolParameters map[string]any // parameters passed to the tool
	ActionClass    ActionClass    // read, write, destructive
	Message        string
	Response       string // Agent's response text
	StartTime      time.Time
	Duration       time.Duration
	Status         string // "success" or "error"
	Error          string
	HTTPCode       int
}

// truncateString truncates a string to maxLen characters.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
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
