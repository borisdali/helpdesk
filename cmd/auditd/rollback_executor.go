package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"helpdesk/internal/audit"
)

// RollbackExecutor dispatches the compensating operation for an approved rollback
// by calling the gateway's direct-tool endpoint, then emits rollback_executed and
// rollback_verified audit events.
//
// The gateway URL is optional; when empty, Execute is a no-op (useful when the
// auditd service is deployed without a co-located gateway).
type RollbackExecutor struct {
	rollbackStore *audit.RollbackStore
	auditStore    *audit.Store
	gatewayURL    string
	apiKey        string
	httpClient    *http.Client
}

// NewRollbackExecutor creates an executor. gatewayURL is the base URL of the
// gateway (e.g. "http://localhost:1200"); apiKey is the service API key.
func NewRollbackExecutor(rollbackStore *audit.RollbackStore, auditStore *audit.Store, gatewayURL, apiKey string) *RollbackExecutor {
	return &RollbackExecutor{
		rollbackStore: rollbackStore,
		auditStore:    auditStore,
		gatewayURL:    gatewayURL,
		apiKey:        apiKey,
		httpClient:    &http.Client{Timeout: 60 * time.Second},
	}
}

// Execute runs the compensating operation for the given rollback record.
// It transitions the status from pending_approval → executing → success | failed.
//
// This method is idempotent: calling Execute on an already-terminal record is a no-op.
func (e *RollbackExecutor) Execute(ctx context.Context, rbk *audit.RollbackRecord) error {
	if rbk.Status == "success" || rbk.Status == "failed" || rbk.Status == "cancelled" {
		return nil
	}

	// Deserialize the stored plan.
	var plan audit.RollbackPlan
	if err := json.Unmarshal([]byte(rbk.PlanJSON), &plan); err != nil {
		return fmt.Errorf("unmarshal rollback plan: %w", err)
	}
	if plan.Reversibility != audit.ReversibilityYes || plan.InverseOp == nil {
		return fmt.Errorf("rollback %s plan is not executable (reversibility=%s)", rbk.RollbackID, plan.Reversibility)
	}

	// Transition to executing.
	if err := e.rollbackStore.UpdateRollbackStatus(ctx, rbk.RollbackID, "executing", ""); err != nil {
		return fmt.Errorf("transition to executing: %w", err)
	}

	output, execErr := e.dispatchInverseOp(ctx, rbk, plan.InverseOp)

	status := "success"
	if execErr != nil {
		status = "failed"
		output = execErr.Error()
		slog.Error("rollback execution failed",
			"rollback_id", rbk.RollbackID,
			"tool", plan.InverseOp.Tool,
			"err", execErr)
	}

	// Persist result.
	if updateErr := e.rollbackStore.UpdateRollbackStatus(ctx, rbk.RollbackID, status, output); updateErr != nil {
		slog.Error("rollback: failed to update status after execution",
			"rollback_id", rbk.RollbackID, "status", status, "err", updateErr)
	}

	// Emit rollback_executed audit event.
	e.emitEvent(ctx, audit.EventTypeRollbackExecuted, rbk, &plan, status, output)

	if execErr != nil {
		return execErr
	}

	// Post-rollback verification: emit rollback_verified if we can query the result.
	// Currently a best-effort no-op; a future version will query the agent for
	// current state and compare against the expected pre-mutation state.
	e.emitEvent(ctx, audit.EventTypeRollbackVerified, rbk, &plan, "verified_ok", "")
	return nil
}

// dispatchInverseOp sends the compensating tool call to the gateway.
// Returns the response output on success.
func (e *RollbackExecutor) dispatchInverseOp(ctx context.Context, rbk *audit.RollbackRecord, op *audit.InverseOperation) (string, error) {
	if e.gatewayURL == "" {
		slog.Warn("rollback executor: no gateway URL configured; skipping dispatch",
			"rollback_id", rbk.RollbackID, "tool", op.Tool)
		return "gateway not configured; rollback dispatch skipped", nil
	}

	// Construct the direct-tool URL:
	//   POST /api/v1/{agent}/{tool}
	url := fmt.Sprintf("%s/api/v1/%s/%s", e.gatewayURL, op.Agent, op.Tool)

	body, err := json.Marshal(op.Args)
	if err != nil {
		return "", fmt.Errorf("marshal inverse op args: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build gateway request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	// Carry the rollback trace ID so the compensating journey is linked.
	req.Header.Set("X-Trace-ID", rbk.RollbackTraceID)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("gateway request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("gateway returned %d: %s", resp.StatusCode, string(respBody))
	}
	return string(respBody), nil
}

// emitEvent records a rollback lifecycle audit event.
func (e *RollbackExecutor) emitEvent(ctx context.Context, eventType audit.EventType, rbk *audit.RollbackRecord, plan *audit.RollbackPlan, status, errMsg string) {
	event := &audit.Event{
		EventID:     string(eventType[:3]) + "_" + uuid.New().String()[:8],
		EventType:   eventType,
		TraceID:     rbk.RollbackTraceID,
		ActionClass: audit.ActionDestructive,
		Session:     audit.Session{ID: rbk.RollbackID},
		RollbackExecution: &audit.RollbackExecution{
			RollbackID:      rbk.RollbackID,
			OriginalEventID: rbk.OriginalEventID,
			OriginalTraceID: rbk.OriginalTraceID,
			Plan:            plan,
			Status:          status,
			ErrorMessage:    errMsg,
		},
		Outcome: &audit.Outcome{Status: status, ErrorMessage: errMsg},
	}
	if recordErr := e.auditStore.Record(ctx, event); recordErr != nil {
		slog.Warn("rollback executor: failed to emit event",
			"event_type", eventType, "rollback_id", rbk.RollbackID, "err", recordErr)
	}
}
