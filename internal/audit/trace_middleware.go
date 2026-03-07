package audit

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// TraceMiddleware wraps an HTTP handler to extract trace_id from A2A message metadata.
// The trace_id is stored in the provided CurrentTraceStore for tools to access.
// Only sets the store when the incoming request carries a trace_id; direct agent calls
// without metadata will have an empty trace store. Use TraceMiddlewareWithAudit instead
// to also generate trace IDs for unannotated requests and emit journey anchor events.
func TraceMiddleware(store *CurrentTraceStore, next http.Handler) http.Handler {
	return TraceMiddlewareWithAudit(store, nil, "", next)
}

// TraceMiddlewareWithAudit is like TraceMiddleware but additionally:
//  1. Generates a trace_id when the incoming A2A request does not carry one,
//     so that every request — including direct curl calls — gets a trace_id.
//  2. Emits a gateway_request anchor event before dispatching to the next handler,
//     making the request visible as a journey in the audit log without requiring
//     an upstream orchestrator or gateway.
//
// agentName is used as the decision_agent on the anchor event.
// auditor may be nil, in which case anchor events are not emitted (step 2 is skipped).
func TraceMiddlewareWithAudit(store *CurrentTraceStore, auditor Auditor, agentName string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			slog.Debug("trace middleware: failed to read body", "err", err)
			next.ServeHTTP(w, r)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		parsed := parseA2ARequest(body)

		// Use the trace_id from the message metadata when present (gateway/orchestrator
		// forwarded the request). Otherwise generate one so direct calls are traceable.
		// Empty trace_id must never reach the audit store — ungrouped events are
		// invisible to journey aggregation.
		// Generated IDs use the "ar_" prefix (agent request) to distinguish them from
		// orchestrator-injected IDs ("tr_") in the audit log.
		traceID := parsed.traceID
		if traceID == "" {
			traceID = NewTraceIDWithPrefix("ar_")
		}
		store.Set(traceID)
		defer store.Clear()

		slog.Debug("trace middleware: trace_id set", "trace_id", traceID, "generated", parsed.traceID == "")

		// Emit the gateway_request anchor event. This is what makes the request
		// visible as a journey: QueryJourneys Q1 anchors on gateway_request events
		// (with no tool_name) or delegation_decision events.
		if auditor != nil && parsed.userQuery != "" {
			sessionID := parsed.contextID
			if sessionID == "" {
				sessionID = "asess_" + uuid.New().String()[:8]
			}
			event := &Event{
				EventID:   "req_" + uuid.New().String()[:8],
				Timestamp: time.Now().UTC(),
				EventType: EventTypeGatewayRequest,
				TraceID:   traceID,
				Session:   Session{ID: sessionID},
				Input:     Input{UserQuery: parsed.userQuery},
				// Tool.Agent is stored as decision_agent so the journey summary
				// can show which agent handled the request.
				Tool: &ToolExecution{Name: "", Agent: agentName},
			}
			if err := auditor.Record(r.Context(), event); err != nil {
				slog.Warn("trace middleware: failed to record anchor event", "err", err)
			}
		}

		next.ServeHTTP(w, r)
	})
}

// a2aRequestData holds the fields extracted from an incoming A2A JSON-RPC request.
type a2aRequestData struct {
	traceID   string
	userQuery string
	contextID string
}

// parseA2ARequest extracts trace_id, user query text, and context ID from an
// A2A message/send JSON-RPC body.
func parseA2ARequest(body []byte) a2aRequestData {
	var req struct {
		Params struct {
			ContextID string `json:"contextId"`
			Message   struct {
				Metadata map[string]any `json:"metadata"`
				Parts    []struct {
					Kind string `json:"kind"`
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"message"`
		} `json:"params"`
	}

	var out a2aRequestData
	if err := json.Unmarshal(body, &req); err != nil {
		return out
	}

	if req.Params.Message.Metadata != nil {
		if id, ok := req.Params.Message.Metadata["trace_id"].(string); ok {
			out.traceID = id
		}
	}

	for _, p := range req.Params.Message.Parts {
		if p.Kind == "text" && p.Text != "" {
			out.userQuery = p.Text
			break
		}
	}

	out.contextID = req.Params.ContextID
	return out
}

// extractTraceID is kept for backward compatibility. Use parseA2ARequest for new code.
func extractTraceID(body []byte) string {
	return parseA2ARequest(body).traceID
}
