package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/decisions"
)

// handleGetDecisions handles GET /api/v1/decisions.
//
// Query params:
//
//	status=pending (default) | approved | denied | all
//	type=gate | fleet_approval | step_approval  (empty = all types)
//	limit=50 (default)
//
// Fans out to auditd approvals + playbook runs, merges, and sorts by
// requested_at descending.
func (g *Gateway) handleGetDecisions(w http.ResponseWriter, r *http.Request) {
	if g.auditURL == "" {
		writeError(w, http.StatusServiceUnavailable, "auditd URL not configured")
		return
	}

	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	filterType := r.URL.Query().Get("type")
	limit := 50

	ctx := r.Context()
	var all []decisions.Decision

	// Fleet and step approvals from the auditd approval store.
	if filterType == "" || filterType == string(decisions.DecisionTypeFleetApproval) || filterType == string(decisions.DecisionTypeStepApproval) {
		approvals, err := g.fetchPendingApprovals(ctx, status, limit)
		if err != nil {
			slog.Warn("decisions: failed to fetch approvals from auditd", "err", err)
		}
		all = append(all, approvals...)
	}

	// Playbook gate decisions from the playbook_runs table.
	if filterType == "" || filterType == string(decisions.DecisionTypeGate) {
		if status == "pending" || status == "all" {
			gates, err := g.fetchPendingGates(ctx, limit)
			if err != nil {
				slog.Warn("decisions: failed to fetch gate runs from auditd", "err", err)
			}
			all = append(all, gates...)
		}
	}

	// Sort by requested_at descending (most recent first).
	sort.Slice(all, func(i, j int) bool {
		return all[i].RequestedAt.After(all[j].RequestedAt)
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"decisions": all,
		"total":     len(all),
	})
}

// handleResolveDecision handles POST /api/v1/decisions/{id}/resolve.
//
// Routes to the appropriate backend based on the ID prefix:
//
//	gate:{runID}     → proceed-escalation (playbook gate)
//	fleet:{id}       → auditd approval approve/deny
//	step:{id}        → auditd approval approve/deny
func (g *Gateway) handleResolveDecision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "decision ID is required")
		return
	}

	var req struct {
		Resolution      string `json:"resolution"`
		ResolvedBy      string `json:"resolved_by,omitempty"`
		Reason          string `json:"reason,omitempty"`
		ApprovalMode    string `json:"approval_mode,omitempty"`
		ApprovalSession string `json:"approval_session,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Resolution != "approved" && req.Resolution != "denied" {
		writeError(w, http.StatusBadRequest, `resolution must be "approved" or "denied"`)
		return
	}

	switch {
	case strings.HasPrefix(id, "gate:"):
		runID := strings.TrimPrefix(id, "gate:")
		g.resolveGate(w, r, runID, req.Resolution, req.ResolvedBy, req.ApprovalMode, req.ApprovalSession)

	case strings.HasPrefix(id, "fleet:"), strings.HasPrefix(id, "step:"):
		approvalID := id[strings.Index(id, ":")+1:]
		g.resolveAuditdApproval(w, r, approvalID, req.Resolution, req.ResolvedBy, req.Reason)

	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown decision ID prefix in %q; expected gate:, fleet:, or step:", id))
	}
}

// resolveGate delegates gate resolution to handleProceedEscalation by
// rewriting the request URL and body to match that handler's expected form.
func (g *Gateway) resolveGate(w http.ResponseWriter, r *http.Request, runID, resolution, resolvedBy, approvalMode, approvalSession string) {
	body, err := json.Marshal(map[string]any{
		"resolution":       resolution,
		"resolved_by":      resolvedBy,
		"approval_mode":    approvalMode,
		"approval_session": approvalSession,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build proceed-escalation request")
		return
	}
	r2 := r.Clone(r.Context())
	r2.SetPathValue("runID", runID)
	r2.Body = io.NopCloser(bytes.NewReader(body))
	r2.ContentLength = int64(len(body))
	g.handleProceedEscalation(w, r2)
}

// resolveAuditdApproval proxies approve/deny to auditd.
func (g *Gateway) resolveAuditdApproval(w http.ResponseWriter, r *http.Request, approvalID, resolution, resolvedBy, reason string) {
	if g.auditURL == "" {
		writeError(w, http.StatusServiceUnavailable, "auditd URL not configured")
		return
	}

	endpoint := "approve"
	if resolution == "denied" {
		endpoint = "deny"
	}
	body, _ := json.Marshal(map[string]any{
		"approved_by": resolvedBy,
		"denied_by":   resolvedBy,
		"reason":      reason,
	})

	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/approvals/" + approvalID + "/" + endpoint
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build auditd request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "auditd request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody) //nolint:errcheck
}

// fetchPendingApprovals calls GET /v1/approvals?status=pending on auditd and
// maps results to Decision values.
func (g *Gateway) fetchPendingApprovals(ctx context.Context, status string, limit int) ([]decisions.Decision, error) {
	url := fmt.Sprintf("%s/v1/approvals?status=%s&limit=%d",
		strings.TrimSuffix(g.auditURL, "/"), status, limit)

	ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx2, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auditd approvals returned %d", resp.StatusCode)
	}

	var result struct {
		Approvals []audit.StoredApproval `json:"approvals"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	baseURL := strings.TrimSuffix(g.baseURL, "/")
	var out []decisions.Decision
	for _, a := range result.Approvals {
		dt := decisions.DecisionTypeStepApproval
		if a.ActionClass == "escalation" {
			dt = decisions.DecisionTypeFleetApproval
		}
		d := decisions.Decision{
			ID:          string(dt) + ":" + a.ApprovalID,
			Type:        dt,
			Status:      a.Status,
			Summary:     fmt.Sprintf("%s %s/%s", a.ActionClass, a.AgentName, a.ToolName),
			RequestedBy: a.RequestedBy,
			RequestedAt: a.RequestedAt,
			ExpiresAt:   a.ExpiresAt,
			ResolveURL:  baseURL + "/api/v1/decisions/" + string(dt) + ":" + a.ApprovalID + "/resolve",
			Extra: map[string]any{
				"tool":          a.ToolName,
				"agent":         a.AgentName,
				"action_class":  a.ActionClass,
				"resource_type": a.ResourceType,
				"resource_name": a.ResourceName,
			},
		}
		out = append(out, d)
	}
	return out, nil
}

// fetchPendingGates calls GET /v1/fleet/playbook-runs?outcome=gate_pending
// on auditd and maps results to Decision values.
func (g *Gateway) fetchPendingGates(ctx context.Context, limit int) ([]decisions.Decision, error) {
	url := fmt.Sprintf("%s/v1/fleet/playbook-runs?outcome=%s&limit=%d",
		strings.TrimSuffix(g.auditURL, "/"), audit.OutcomeGatePending, limit)

	ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx2, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auditd playbook-runs returned %d", resp.StatusCode)
	}

	var result struct {
		Runs []*audit.PlaybookRun `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	baseURL := strings.TrimSuffix(g.baseURL, "/")
	var out []decisions.Decision
	for _, run := range result.Runs {
		resolveID := "gate:" + run.RunID
		d := decisions.Decision{
			ID:          resolveID,
			Type:        decisions.DecisionTypeGate,
			Status:      "pending",
			Summary:     "Triage complete — ESCALATE_TO " + run.EscalatedTo,
			RequestedBy: run.Operator,
			RequestedAt: run.StartedAt,
			ResolveURL:  baseURL + "/api/v1/decisions/" + resolveID + "/resolve",
			Extra: map[string]any{
				"escalation_target": run.EscalatedTo,
				"findings":          run.FindingsSummary,
				"series_id":         run.SeriesID,
			},
		}
		out = append(out, d)
	}
	return out, nil
}
