package audit

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
)

// TraceMiddleware wraps an HTTP handler to extract trace_id from A2A message metadata.
// The trace_id is stored in the provided CurrentTraceStore for tools to access.
func TraceMiddleware(store *CurrentTraceStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the request body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			slog.Debug("trace middleware: failed to read body", "err", err)
			next.ServeHTTP(w, r)
			return
		}
		// Restore the body for the next handler
		r.Body = io.NopCloser(bytes.NewReader(body))

		// Try to extract trace_id from JSON-RPC request
		traceID := extractTraceID(body)
		if traceID != "" {
			store.Set(traceID)
			slog.Debug("trace middleware: extracted trace_id", "trace_id", traceID)
		}

		// Call the next handler
		next.ServeHTTP(w, r)

		// Clear trace_id after request completes
		if traceID != "" {
			store.Clear()
		}
	})
}

// extractTraceID attempts to extract trace_id from an A2A JSON-RPC request.
// The trace_id is expected in message.metadata.trace_id.
func extractTraceID(body []byte) string {
	// JSON-RPC request structure for message/send
	var req struct {
		Params struct {
			Message struct {
				Metadata map[string]any `json:"metadata"`
			} `json:"message"`
		} `json:"params"`
	}

	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}

	if req.Params.Message.Metadata == nil {
		return ""
	}

	if traceID, ok := req.Params.Message.Metadata["trace_id"].(string); ok {
		return traceID
	}

	return ""
}
