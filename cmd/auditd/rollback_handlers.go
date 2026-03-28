package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"helpdesk/internal/audit"
	"helpdesk/internal/authz"
)

// rollbackServer handles HTTP endpoints for the rollback & undo module.
type rollbackServer struct {
	store         *audit.RollbackStore
	auditStore    *audit.Store
	fleetStore    *audit.FleetStore
	approvalStore *audit.ApprovalStore
}

// handleDeriveRollbackPlan derives a rollback plan for an existing tool_execution
// event without persisting anything. This is a safe read-only dry-run.
//
// POST /v1/events/{eventID}/rollback-plan
func (s *rollbackServer) handleDeriveRollbackPlan(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("eventID")
	if eventID == "" {
		http.Error(w, "missing event ID", http.StatusBadRequest)
		return
	}

	events, err := s.auditStore.Query(r.Context(), audit.QueryOptions{
		EventID:   eventID,
		EventType: audit.EventTypeToolExecution,
	})
	if err != nil {
		slog.Error("rollback-plan: query event", "event_id", eventID, "err", err)
		http.Error(w, "failed to query event", http.StatusInternalServerError)
		return
	}
	if len(events) == 0 {
		http.Error(w, "event not found or not a tool_execution event", http.StatusNotFound)
		return
	}

	ev := &events[0]
	plan, err := audit.DeriveRollbackPlan(ev)
	if err != nil {
		http.Error(w, "failed to derive rollback plan: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(plan) //nolint:errcheck
}

// InitiateRollbackRequest is the request body for POST /v1/rollbacks.
type InitiateRollbackRequest struct {
	OriginalEventID string `json:"original_event_id"`
	Justification   string `json:"justification,omitempty"`
	DryRun          bool   `json:"dry_run,omitempty"`
}

// handleInitiateRollback creates a new rollback record for an existing tool_execution event.
//
// POST /v1/rollbacks
func (s *rollbackServer) handleInitiateRollback(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	var req InitiateRollbackRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.OriginalEventID == "" {
		http.Error(w, "original_event_id is required", http.StatusBadRequest)
		return
	}

	// Fetch and validate the original event.
	events, err := s.auditStore.Query(r.Context(), audit.QueryOptions{
		EventID:   req.OriginalEventID,
		EventType: audit.EventTypeToolExecution,
	})
	if err != nil {
		slog.Error("initiate rollback: query event", "event_id", req.OriginalEventID, "err", err)
		http.Error(w, "failed to query event", http.StatusInternalServerError)
		return
	}
	if len(events) == 0 {
		http.Error(w, "event not found or not a tool_execution event", http.StatusNotFound)
		return
	}

	ev := &events[0]
	plan, err := audit.DeriveRollbackPlan(ev)
	if err != nil {
		http.Error(w, "failed to derive rollback plan: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if plan.Reversibility == audit.ReversibilityNo {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"error":                 "operation is not reversible",
			"not_reversible_reason": plan.NotReversibleReason,
			"plan":                  plan,
		})
		return
	}

	// Dry-run: return the plan without persisting anything.
	if req.DryRun {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"dry_run": true,
			"plan":    plan,
		})
		return
	}

	// Duplicate check: reject if an active rollback already exists for this event.
	existing, err := s.store.GetRollbackByEventID(r.Context(), req.OriginalEventID)
	if err != nil {
		slog.Error("initiate rollback: duplicate check", "event_id", req.OriginalEventID, "err", err)
		http.Error(w, "failed to check for existing rollbacks", http.StatusInternalServerError)
		return
	}
	if existing != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"error":             "active rollback already exists for this event",
			"existing_rollback": existing,
		})
		return
	}

	// Resolve the caller identity from context.
	principal := authz.PrincipalFromContext(r.Context())
	initiatedBy := principal.EffectiveID()
	if initiatedBy == "" {
		initiatedBy = "unknown"
	}

	// Serialize the plan.
	planBytes, err := json.Marshal(plan)
	if err != nil {
		slog.Error("initiate rollback: marshal plan", "err", err)
		http.Error(w, "failed to serialize rollback plan", http.StatusInternalServerError)
		return
	}

	// Create rollback record — starts in pending_approval state.
	rbk := &audit.RollbackRecord{
		OriginalEventID: req.OriginalEventID,
		OriginalTraceID: ev.TraceID,
		Status:          "pending_approval",
		InitiatedBy:     initiatedBy,
		PlanJSON:        string(planBytes),
	}
	if err := s.store.CreateRollback(r.Context(), rbk); err != nil {
		slog.Error("initiate rollback: create record", "err", err)
		http.Error(w, "failed to create rollback record", http.StatusInternalServerError)
		return
	}

	// Create an approval request so the rollback appears in the approval queue.
	if s.approvalStore != nil {
		var inverseAgent string
		if plan.InverseOp != nil {
			inverseAgent = plan.InverseOp.Agent
		}
		approval := &audit.StoredApproval{
			TraceID:      rbk.RollbackTraceID,
			Status:       "pending",
			ActionClass:  string(audit.ActionDestructive),
			ToolName:     "rollback",
			AgentName:    "auditd",
			ResourceType: inverseAgent,
			ResourceName: plan.OriginalTool,
			RequestedBy:  initiatedBy,
			RequestedAt:  rbk.CreatedAt,
			PolicyName:   "rollback-requires-approval",
			ApproverRole: "operator",
			RequestContext: map[string]any{
				"rollback_id":       rbk.RollbackID,
				"original_event_id": req.OriginalEventID,
				"justification":     req.Justification,
			},
		}
		if createErr := s.approvalStore.CreateRequest(r.Context(), approval); createErr != nil {
			slog.Warn("initiate rollback: failed to create approval request", "err", createErr)
		} else {
			rbk.ApprovalID = approval.ApprovalID
			if updateErr := s.store.SetRollbackApprovalID(r.Context(), rbk.RollbackID, approval.ApprovalID); updateErr != nil {
				slog.Warn("initiate rollback: failed to link approval to rollback", "err", updateErr)
			}
		}
	}

	// Emit rollback_initiated audit event.
	rollbackEvent := &audit.Event{
		EventID:     "rbk_" + uuid.New().String()[:8],
		EventType:   audit.EventTypeRollbackInitiated,
		TraceID:     rbk.RollbackTraceID,
		ActionClass: audit.ActionDestructive,
		Session:     audit.Session{ID: rbk.RollbackID},
		Input:       audit.Input{UserQuery: req.Justification},
		RollbackExecution: &audit.RollbackExecution{
			RollbackID:      rbk.RollbackID,
			OriginalEventID: req.OriginalEventID,
			OriginalTraceID: ev.TraceID,
			Plan:            plan,
			Status:          "pending_approval",
		},
		Outcome: &audit.Outcome{Status: "pending_approval"},
	}
	if recordErr := s.auditStore.Record(r.Context(), rollbackEvent); recordErr != nil {
		slog.Warn("initiate rollback: failed to emit rollback_initiated event", "err", recordErr)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"rollback": rbk,
		"plan":     plan,
	})
}

// handleListRollbacks returns rollback records, newest first.
//
// GET /v1/rollbacks
func (s *rollbackServer) handleListRollbacks(w http.ResponseWriter, r *http.Request) {
	opts := audit.RollbackQueryOptions{Limit: 50}
	q := r.URL.Query()
	if v := q.Get("original_event_id"); v != "" {
		opts.OriginalEventID = v
	}
	if v := q.Get("original_trace_id"); v != "" {
		opts.OriginalTraceID = v
	}
	if v := q.Get("status"); v != "" {
		opts.Status = v
	}
	if v := q.Get("initiated_by"); v != "" {
		opts.InitiatedBy = v
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.Limit = n
		}
	}

	records, err := s.store.ListRollbacks(r.Context(), opts)
	if err != nil {
		slog.Error("list rollbacks", "err", err)
		http.Error(w, "failed to list rollbacks", http.StatusInternalServerError)
		return
	}
	if records == nil {
		records = []*audit.RollbackRecord{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(records) //nolint:errcheck
}

// handleGetRollback returns a single rollback record with its derived plan.
//
// GET /v1/rollbacks/{rollbackID}
func (s *rollbackServer) handleGetRollback(w http.ResponseWriter, r *http.Request) {
	rollbackID := r.PathValue("rollbackID")
	if rollbackID == "" {
		http.Error(w, "missing rollback ID", http.StatusBadRequest)
		return
	}

	rbk, err := s.store.GetRollback(r.Context(), rollbackID)
	if err != nil {
		if isNotFound(err) {
			http.Error(w, "rollback not found", http.StatusNotFound)
			return
		}
		slog.Error("get rollback", "rollback_id", rollbackID, "err", err)
		http.Error(w, "failed to get rollback", http.StatusInternalServerError)
		return
	}

	// Deserialize stored plan.
	var plan *audit.RollbackPlan
	if rbk.PlanJSON != "" {
		plan = &audit.RollbackPlan{}
		if err := json.Unmarshal([]byte(rbk.PlanJSON), plan); err != nil {
			slog.Warn("get rollback: unmarshal plan", "rollback_id", rollbackID, "err", err)
			plan = nil
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"rollback": rbk,
		"plan":     plan,
	})
}

// handleCancelRollback cancels a pending rollback record.
//
// POST /v1/rollbacks/{rollbackID}/cancel
func (s *rollbackServer) handleCancelRollback(w http.ResponseWriter, r *http.Request) {
	rollbackID := r.PathValue("rollbackID")
	if rollbackID == "" {
		http.Error(w, "missing rollback ID", http.StatusBadRequest)
		return
	}

	rbk, err := s.store.GetRollback(r.Context(), rollbackID)
	if err != nil {
		if isNotFound(err) {
			http.Error(w, "rollback not found", http.StatusNotFound)
			return
		}
		slog.Error("cancel rollback: get", "rollback_id", rollbackID, "err", err)
		http.Error(w, "failed to get rollback", http.StatusInternalServerError)
		return
	}

	if rbk.Status == "success" || rbk.Status == "failed" || rbk.Status == "cancelled" {
		http.Error(w, "rollback is already in terminal state: "+rbk.Status, http.StatusConflict)
		return
	}
	if rbk.Status == "executing" {
		http.Error(w, "rollback is currently executing and cannot be cancelled", http.StatusConflict)
		return
	}

	if err := s.store.UpdateRollbackStatus(r.Context(), rollbackID, "cancelled", ""); err != nil {
		slog.Error("cancel rollback: update status", "rollback_id", rollbackID, "err", err)
		http.Error(w, "failed to cancel rollback", http.StatusInternalServerError)
		return
	}

	rbk.Status = "cancelled"
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"rollback": rbk,
	})
}

// handleInitiateFleetRollback initiates a rollback of a fleet job by creating a
// FleetRollbackRecord. The actual reverse job construction is handled by the
// fleet-runner via BuildRollbackJobDef.
//
// POST /v1/fleet/jobs/{jobID}/rollback
func (s *rollbackServer) handleInitiateFleetRollback(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobID")
	if jobID == "" {
		http.Error(w, "missing job ID", http.StatusBadRequest)
		return
	}

	// Validate the fleet job exists.
	job, err := s.fleetStore.GetJob(r.Context(), jobID)
	if err != nil {
		if isNotFound(err) {
			http.Error(w, "fleet job not found", http.StatusNotFound)
			return
		}
		slog.Error("fleet rollback: get job", "job_id", jobID, "err", err)
		http.Error(w, "failed to get fleet job", http.StatusInternalServerError)
		return
	}
	if job == nil {
		http.Error(w, "fleet job not found", http.StatusNotFound)
		return
	}

	// Duplicate check.
	existing, err := s.store.GetFleetRollbackByJobID(r.Context(), jobID)
	if err != nil {
		slog.Error("fleet rollback: duplicate check", "job_id", jobID, "err", err)
		http.Error(w, "failed to check for existing rollbacks", http.StatusInternalServerError)
		return
	}
	if existing != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"error":             "active fleet rollback already exists for this job",
			"existing_rollback": existing,
		})
		return
	}

	var req struct {
		Scope string `json:"scope"` // "all" | "canary_only" | "failed_only"
		DryRun bool  `json:"dry_run,omitempty"`
	}
	if body, bodyErr := io.ReadAll(r.Body); bodyErr == nil && len(body) > 0 {
		json.Unmarshal(body, &req) //nolint:errcheck
	}

	scope := req.Scope
	if scope == "" {
		scope = "all"
	}

	if req.DryRun {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"dry_run":      true,
			"job_id":       jobID,
			"scope":        scope,
			"job_status":   job.Status,
			"job_name":     job.Name,
		})
		return
	}

	principal := authz.PrincipalFromContext(r.Context())
	initiatedBy := principal.EffectiveID()
	if initiatedBy == "" {
		initiatedBy = "unknown"
	}

	frb := &audit.FleetRollbackRecord{
		OriginalJobID: jobID,
		InitiatedBy:   initiatedBy,
		Scope:         scope,
	}
	if err := s.store.CreateFleetRollback(r.Context(), frb); err != nil {
		slog.Error("fleet rollback: create record", "job_id", jobID, "err", err)
		http.Error(w, "failed to create fleet rollback record", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(frb) //nolint:errcheck
}

// handleGetFleetRollback returns the rollback status for a fleet job.
//
// GET /v1/fleet/jobs/{jobID}/rollback
func (s *rollbackServer) handleGetFleetRollback(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobID")
	if jobID == "" {
		http.Error(w, "missing job ID", http.StatusBadRequest)
		return
	}

	frb, err := s.store.GetFleetRollbackByJobID(r.Context(), jobID)
	if err != nil {
		slog.Error("get fleet rollback", "job_id", jobID, "err", err)
		http.Error(w, "failed to get fleet rollback", http.StatusInternalServerError)
		return
	}
	if frb == nil {
		http.Error(w, "no active rollback for this fleet job", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(frb) //nolint:errcheck
}

// isNotFound returns true for "not found" errors returned by scan helpers.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return len(s) >= 9 && s[len(s)-9:] == "not found" ||
		containsStr(s, "not found")
}

func containsStr(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
