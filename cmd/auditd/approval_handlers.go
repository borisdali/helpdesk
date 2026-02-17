package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"helpdesk/internal/audit"
)

// approvalServer handles approval-related HTTP endpoints.
type approvalServer struct {
	store    *audit.ApprovalStore
	notifier *ApprovalNotifier
}

// CreateApprovalRequest is the JSON body for creating an approval request.
type CreateApprovalRequest struct {
	EventID      string         `json:"event_id,omitempty"`
	TraceID      string         `json:"trace_id,omitempty"`
	ActionClass  string         `json:"action_class"`
	ToolName     string         `json:"tool_name,omitempty"`
	AgentName    string         `json:"agent_name,omitempty"`
	ResourceType string         `json:"resource_type,omitempty"`
	ResourceName string         `json:"resource_name,omitempty"`
	RequestedBy  string         `json:"requested_by"`
	Context      map[string]any `json:"request_context,omitempty"`
	PolicyName   string         `json:"policy_name,omitempty"`
	ApproverRole string         `json:"approver_role,omitempty"`
	ExpiresInMin int            `json:"expires_in_minutes,omitempty"`
	CallbackURL  string         `json:"callback_url,omitempty"`
}

// ApproveRequest is the JSON body for approving a request.
type ApproveRequest struct {
	ApprovedBy    string `json:"approved_by"`
	Reason        string `json:"reason,omitempty"`
	ValidForMin   int    `json:"valid_for_minutes,omitempty"`
}

// DenyRequest is the JSON body for denying a request.
type DenyRequest struct {
	DeniedBy string `json:"denied_by"`
	Reason   string `json:"reason,omitempty"`
}

func (s *approvalServer) handleCreateApproval(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var req CreateApprovalRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.ActionClass == "" {
		http.Error(w, "action_class is required", http.StatusBadRequest)
		return
	}
	if req.RequestedBy == "" {
		http.Error(w, "requested_by is required", http.StatusBadRequest)
		return
	}

	approval := &audit.StoredApproval{
		EventID:        req.EventID,
		TraceID:        req.TraceID,
		Status:         "pending",
		ActionClass:    req.ActionClass,
		ToolName:       req.ToolName,
		AgentName:      req.AgentName,
		ResourceType:   req.ResourceType,
		ResourceName:   req.ResourceName,
		RequestedBy:    req.RequestedBy,
		RequestContext: req.Context,
		PolicyName:     req.PolicyName,
		ApproverRole:   req.ApproverRole,
		CallbackURL:    req.CallbackURL,
	}

	if req.ExpiresInMin > 0 {
		approval.ExpiresAt = time.Now().UTC().Add(time.Duration(req.ExpiresInMin) * time.Minute)
	} else {
		// Default expiration: 60 minutes
		approval.ExpiresAt = time.Now().UTC().Add(60 * time.Minute)
	}

	if err := s.store.CreateRequest(r.Context(), approval); err != nil {
		slog.Error("failed to create approval request", "err", err)
		http.Error(w, "failed to create approval request", http.StatusInternalServerError)
		return
	}

	slog.Info("approval request created",
		"approval_id", approval.ApprovalID,
		"action_class", approval.ActionClass,
		"tool", approval.ToolName,
		"agent", approval.AgentName,
		"requested_by", approval.RequestedBy)

	// Send notification
	if s.notifier != nil {
		s.notifier.NotifyCreated(r.Context(), approval)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"approval_id": approval.ApprovalID,
		"status":      approval.Status,
		"expires_at":  approval.ExpiresAt.Format(time.RFC3339),
	})
}

func (s *approvalServer) handleGetApproval(w http.ResponseWriter, r *http.Request) {
	approvalID := r.PathValue("approvalID")
	if approvalID == "" {
		http.Error(w, "missing approval ID", http.StatusBadRequest)
		return
	}

	approval, err := s.store.GetRequest(r.Context(), approvalID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(approval)
}

func (s *approvalServer) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	opts := audit.ApprovalQueryOptions{
		Limit: 100,
	}

	if v := r.URL.Query().Get("status"); v != "" {
		opts.Status = v
	}
	if v := r.URL.Query().Get("agent"); v != "" {
		opts.AgentName = v
	}
	if v := r.URL.Query().Get("trace_id"); v != "" {
		opts.TraceID = v
	}
	if v := r.URL.Query().Get("requested_by"); v != "" {
		opts.RequestedBy = v
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if limit, err := strconv.Atoi(v); err == nil && limit > 0 {
			opts.Limit = limit
		}
	}

	approvals, err := s.store.ListRequests(r.Context(), opts)
	if err != nil {
		slog.Error("failed to list approvals", "err", err)
		http.Error(w, "failed to list approvals", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(approvals)
}

func (s *approvalServer) handleApprove(w http.ResponseWriter, r *http.Request) {
	approvalID := r.PathValue("approvalID")
	if approvalID == "" {
		http.Error(w, "missing approval ID", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var req ApproveRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.ApprovedBy == "" {
		http.Error(w, "approved_by is required", http.StatusBadRequest)
		return
	}

	var validFor time.Duration
	if req.ValidForMin > 0 {
		validFor = time.Duration(req.ValidForMin) * time.Minute
	}

	if err := s.store.Approve(r.Context(), approvalID, req.ApprovedBy, req.Reason, validFor); err != nil {
		slog.Error("failed to approve request", "err", err, "approval_id", approvalID)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	slog.Info("approval granted",
		"approval_id", approvalID,
		"approved_by", req.ApprovedBy,
		"valid_for", validFor)

	// Get updated approval for response
	approval, _ := s.store.GetRequest(r.Context(), approvalID)

	// Send notification
	if s.notifier != nil && approval != nil {
		s.notifier.NotifyResolved(r.Context(), approval)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(approval)
}

func (s *approvalServer) handleDeny(w http.ResponseWriter, r *http.Request) {
	approvalID := r.PathValue("approvalID")
	if approvalID == "" {
		http.Error(w, "missing approval ID", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var req DenyRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.DeniedBy == "" {
		http.Error(w, "denied_by is required", http.StatusBadRequest)
		return
	}

	if err := s.store.Deny(r.Context(), approvalID, req.DeniedBy, req.Reason); err != nil {
		slog.Error("failed to deny request", "err", err, "approval_id", approvalID)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	slog.Info("approval denied",
		"approval_id", approvalID,
		"denied_by", req.DeniedBy)

	// Get updated approval for response
	approval, _ := s.store.GetRequest(r.Context(), approvalID)

	// Send notification
	if s.notifier != nil && approval != nil {
		s.notifier.NotifyResolved(r.Context(), approval)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(approval)
}

func (s *approvalServer) handleCancel(w http.ResponseWriter, r *http.Request) {
	approvalID := r.PathValue("approvalID")
	if approvalID == "" {
		http.Error(w, "missing approval ID", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var req struct {
		CancelledBy string `json:"cancelled_by"`
		Reason      string `json:"reason,omitempty"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	if req.CancelledBy == "" {
		req.CancelledBy = "system"
	}

	if err := s.store.Cancel(r.Context(), approvalID, req.CancelledBy, req.Reason); err != nil {
		slog.Error("failed to cancel request", "err", err, "approval_id", approvalID)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	slog.Info("approval cancelled", "approval_id", approvalID)

	// Send notification
	if s.notifier != nil {
		approval, _ := s.store.GetRequest(r.Context(), approvalID)
		if approval != nil {
			s.notifier.NotifyResolved(r.Context(), approval)
		}
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "cancelled"})
}

func (s *approvalServer) handleWaitForApproval(w http.ResponseWriter, r *http.Request) {
	approvalID := r.PathValue("approvalID")
	if approvalID == "" {
		http.Error(w, "missing approval ID", http.StatusBadRequest)
		return
	}

	// Parse timeout (default 30s, max 120s)
	timeout := 30 * time.Second
	if v := r.URL.Query().Get("timeout"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			timeout = d
			if timeout > 120*time.Second {
				timeout = 120 * time.Second
			}
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	approval, err := s.store.WaitForResolution(ctx, approvalID)
	if err != nil {
		if ctx.Err() != nil {
			// Timeout - return current status
			approval, _ = s.store.GetRequest(r.Context(), approvalID)
			if approval != nil {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(approval)
				return
			}
		}
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(approval)
}

func (s *approvalServer) handlePendingApprovals(w http.ResponseWriter, r *http.Request) {
	approvals, err := s.store.ListRequests(r.Context(), audit.ApprovalQueryOptions{
		Status: "pending",
		Limit:  100,
	})
	if err != nil {
		slog.Error("failed to list pending approvals", "err", err)
		http.Error(w, "failed to list approvals", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(approvals)
}

// startExpirationWorker starts a background worker to expire old approval requests.
func (s *approvalServer) startExpirationWorker(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			expired, err := s.store.ExpireRequests(context.Background())
			if err != nil {
				slog.Error("failed to expire approvals", "err", err)
			} else if expired > 0 {
				slog.Info("expired approval requests", "count", expired)
			}
		}
	}
}
