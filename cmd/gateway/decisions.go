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
//	action_class=read | write | destructive      (empty = all classes; step_approval only)
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
	filterClass := r.URL.Query().Get("action_class")
	limit := 50

	ctx := r.Context()
	var all []decisions.Decision

	// Fleet and step approvals from the auditd approval store.
	// Both types share the same auditd endpoint; filter by type after fetching.
	if filterType == "" || filterType == string(decisions.DecisionTypeFleetApproval) || filterType == string(decisions.DecisionTypeStepApproval) {
		approvals, err := g.fetchPendingApprovals(ctx, status, limit)
		if err != nil {
			slog.Warn("decisions: failed to fetch approvals from auditd", "err", err)
		}
		for _, a := range approvals {
			if filterType != "" && filterType != string(a.Type) {
				continue
			}
			if filterClass != "" {
				if ac, _ := a.Extra["action_class"].(string); ac != filterClass {
					continue
				}
			}
			all = append(all, a)
		}
	}

	// Playbook gate decisions from the playbook_runs table.
	// Gates have no action_class; skip if caller filtered by class.
	if filterClass == "" && (filterType == "" || filterType == string(decisions.DecisionTypeGate)) {
		if status == "pending" || status == "all" {
			gates, err := g.fetchPendingGates(ctx, limit)
			if err != nil {
				slog.Warn("decisions: failed to fetch gate runs from auditd", "err", err)
			}
			all = append(all, gates...)
		}
	}

	// Post-incident feedback requests — runs where the operator has not yet
	// confirmed or denied the diagnosis. No action_class; skip if filtered.
	if filterClass == "" && (filterType == "" || filterType == string(decisions.DecisionTypeFeedback)) {
		if status == "pending" || status == "all" {
			feedback, err := g.fetchPendingFeedback(ctx, limit)
			if err != nil {
				slog.Warn("decisions: failed to fetch pending feedback from auditd", "err", err)
			}
			all = append(all, feedback...)
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
		VerdictCorrect  *bool  `json:"verdict_correct,omitempty"`
		VerdictNotes    string `json:"verdict_notes,omitempty"`
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
		g.resolveGate(w, r, runID, req.Resolution, req.ResolvedBy, req.ApprovalMode, req.ApprovalSession, req.VerdictCorrect, req.VerdictNotes)

	case strings.HasPrefix(id, "fleet:"), strings.HasPrefix(id, "step:"):
		approvalID := id[strings.Index(id, ":")+1:]
		g.resolveAuditdApproval(w, r, approvalID, req.Resolution, req.ResolvedBy, req.Reason)

	case strings.HasPrefix(id, "feedback:"):
		runID := strings.TrimPrefix(id, "feedback:")
		g.resolveFeedback(w, r, runID, req.Resolution, req.ResolvedBy, req.Reason)

	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown decision ID prefix in %q; expected gate:, fleet:, step:, or feedback:", id))
	}
}

// resolveGate delegates gate resolution to handleProceedEscalation by
// rewriting the request URL and body to match that handler's expected form.
func (g *Gateway) resolveGate(w http.ResponseWriter, r *http.Request, runID, resolution, resolvedBy, approvalMode, approvalSession string, verdictCorrect *bool, verdictNotes string) {
	payload := map[string]any{
		"resolution":       resolution,
		"resolved_by":      resolvedBy,
		"approval_mode":    approvalMode,
		"approval_session": approvalSession,
	}
	if verdictCorrect != nil {
		payload["verdict_correct"] = *verdictCorrect
	}
	if verdictNotes != "" {
		payload["verdict_notes"] = verdictNotes
	}
	body, err := json.Marshal(payload)
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
	bodyMap := map[string]any{"reason": reason}
	if resolution == "approved" {
		bodyMap["approved_by"] = resolvedBy
	} else {
		bodyMap["denied_by"] = resolvedBy
	}
	body, _ := json.Marshal(bodyMap)

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
	// Forward the human operator's identity so auditd can apply role checks
	// against the actual resolver rather than the gateway service account.
	// X-User header (authenticated identity) takes priority over resolved_by
	// (caller-declared, unverified).
	if xUser := r.Header.Get("X-User"); xUser != "" {
		req.Header.Set("X-User", xUser)
	} else if resolvedBy != "" {
		req.Header.Set("X-User", resolvedBy)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "auditd request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody) //nolint:errcheck
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": resolution}) //nolint:errcheck
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

	var result []audit.StoredApproval
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	baseURL := strings.TrimSuffix(g.baseURL, "/")
	var out []decisions.Decision
	for _, a := range result {
		// ID prefix matches handleResolveDecision routing: "step:" or "fleet:".
		// DecisionType ("step_approval" / "fleet_approval") is kept in the Type field.
		dt := decisions.DecisionTypeStepApproval
		idPrefix := "step"
		if a.ActionClass == "escalation" {
			dt = decisions.DecisionTypeFleetApproval
			idPrefix = "fleet"
		}
		decisionID := idPrefix + ":" + a.ApprovalID
		d := decisions.Decision{
			ID:          decisionID,
			Type:        dt,
			Status:      a.Status,
			Summary:     fmt.Sprintf("%s %s/%s", a.ActionClass, a.AgentName, a.ToolName),
			RequestedBy: a.RequestedBy,
			RequestedAt: a.RequestedAt,
			ExpiresAt:   a.ExpiresAt,
			ResolveURL:  baseURL + "/api/v1/decisions/" + decisionID + "/resolve",
			Extra: map[string]any{
				"tool":          a.ToolName,
				"agent":         a.AgentName,
				"action_class":  stepActionClass(a),
				"resource_type": a.ResourceType,
				"resource_name": a.ResourceName,
				"run_id":        a.TraceID,
			},
		}
		out = append(out, d)
	}
	return out, nil
}

// stepActionClass returns the per-step action class stored in RequestContext
// (e.g. "read", "write", "destructive"). Falls back to the approval's
// ActionClass field, which is hardcoded "destructive" for legacy records.
func stepActionClass(a audit.StoredApproval) string {
	if v, ok := a.RequestContext["action_class"].(string); ok && v != "" {
		return v
	}
	return a.ActionClass
}

// handleGetDecision handles GET /api/v1/decisions/{id}.
// Returns the current state of a single decision by ID.
// Supports gate:{runID}, step:{approvalID}, and fleet:{approvalID} prefixes.
func (g *Gateway) handleGetDecision(w http.ResponseWriter, r *http.Request) {
	if g.auditURL == "" {
		writeError(w, http.StatusServiceUnavailable, "auditd URL not configured")
		return
	}
	id := r.PathValue("id")
	baseURL := strings.TrimSuffix(g.baseURL, "/")

	switch {
	case strings.HasPrefix(id, "gate:"):
		runID := strings.TrimPrefix(id, "gate:")
		run, err := g.fetchPlaybookRun(r.Context(), runID)
		if err != nil {
			writeError(w, http.StatusNotFound, "run not found: "+err.Error())
			return
		}
		status := "pending"
		if run.Outcome != audit.OutcomeGatePending {
			status = run.Outcome
		}
		d := decisions.Decision{
			ID:          id,
			Type:        decisions.DecisionTypeGate,
			Status:      status,
			Summary:     gateDecisionSummary(run),
			RequestedBy: run.Operator,
			RequestedAt: run.StartedAt,
			ResolveURL:  baseURL + "/api/v1/decisions/" + id + "/resolve",
			Extra:       gateDecisionExtra(run),
		}
		nextSeries := run.TransitionedTo
		if nextSeries == "" {
			nextSeries = run.EscalatedTo
		}
		if nextSeries != "" {
			if nextPB, err := g.fetchPlaybookBySeriesID(r.Context(), nextSeries); err == nil {
				d.Extra["remediation_preview"] = map[string]any{
					"series_id":     nextPB.SeriesID,
					"name":          nextPB.Name,
					"description":   nextPB.Description,
					"approval_mode": nextPB.ApprovalMode,
				}
			}
		}
		if run.Outcome != audit.OutcomeGatePending {
			if reason := g.fetchGateAcknowledgedReason(r.Context(), runID); reason != "" {
				d.Extra["resolved_reason"] = reason
			}
		}
		writeJSON(w, http.StatusOK, d)

	case strings.HasPrefix(id, "step:"), strings.HasPrefix(id, "fleet:"):
		var approvalID string
		if strings.HasPrefix(id, "step:") {
			approvalID = strings.TrimPrefix(id, "step:")
		} else {
			approvalID = strings.TrimPrefix(id, "fleet:")
		}
		aURL := strings.TrimSuffix(g.auditURL, "/") + "/v1/approvals/" + approvalID
		ctx2, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx2, http.MethodGet, aURL, nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if g.auditAPIKey != "" {
			req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			writeError(w, http.StatusBadGateway, "auditd unreachable: "+err.Error())
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			writeError(w, http.StatusNotFound, "approval not found")
			return
		}
		if resp.StatusCode != http.StatusOK {
			writeError(w, http.StatusBadGateway, fmt.Sprintf("auditd returned %d", resp.StatusCode))
			return
		}
		var a audit.StoredApproval
		if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
			writeError(w, http.StatusInternalServerError, "decoding approval: "+err.Error())
			return
		}
		dt := decisions.DecisionTypeStepApproval
		idPrefix := "step"
		if a.ActionClass == "escalation" {
			dt = decisions.DecisionTypeFleetApproval
			idPrefix = "fleet"
		}
		d := decisions.Decision{
			ID:          idPrefix + ":" + a.ApprovalID,
			Type:        dt,
			Status:      a.Status,
			Summary:     fmt.Sprintf("%s %s/%s", a.ActionClass, a.AgentName, a.ToolName),
			RequestedBy: a.RequestedBy,
			RequestedAt: a.RequestedAt,
			ExpiresAt:   a.ExpiresAt,
			ResolveURL:  baseURL + "/api/v1/decisions/" + id + "/resolve",
			Extra: map[string]any{
				"tool":          a.ToolName,
				"agent":         a.AgentName,
				"action_class":  stepActionClass(a),
				"resource_type": a.ResourceType,
				"resource_name": a.ResourceName,
				"run_id":        a.TraceID,
			},
		}
		writeJSON(w, http.StatusOK, d)

	case strings.HasPrefix(id, "feedback:"):
		runID := strings.TrimPrefix(id, "feedback:")
		aURL := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbook-runs/" + runID + "/feedback"
		ctx2, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		req2, err := http.NewRequestWithContext(ctx2, http.MethodGet, aURL, nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if g.auditAPIKey != "" {
			req2.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
		}
		resp2, err := http.DefaultClient.Do(req2)
		if err != nil {
			writeError(w, http.StatusBadGateway, "auditd unreachable: "+err.Error())
			return
		}
		defer resp2.Body.Close()
		if resp2.StatusCode == http.StatusNotFound {
			writeError(w, http.StatusNotFound, "no feedback request for run")
			return
		}
		if resp2.StatusCode != http.StatusOK {
			writeError(w, http.StatusBadGateway, fmt.Sprintf("auditd returned %d", resp2.StatusCode))
			return
		}
		var fb struct {
			RunID          string  `json:"run_id"`
			SeriesID       string  `json:"series_id"`
			VerdictCorrect *bool   `json:"verdict_correct,omitempty"`
			VerdictNotes   string  `json:"verdict_notes,omitempty"`
			Operator       string  `json:"operator"`
		}
		if err := json.NewDecoder(resp2.Body).Decode(&fb); err != nil {
			writeError(w, http.StatusInternalServerError, "decoding feedback: "+err.Error())
			return
		}
		status := "pending"
		if fb.VerdictCorrect != nil {
			status = "resolved"
		}
		d := decisions.Decision{
			ID:          id,
			Type:        decisions.DecisionTypeFeedback,
			Status:      status,
			Summary:     "Diagnosis feedback needed — " + fb.SeriesID,
			RequestedBy: fb.Operator,
			RequestedAt: time.Now(), // SubmittedAt not returned; approximate
			ResolveURL:  baseURL + "/api/v1/decisions/" + id + "/resolve",
			Extra: map[string]any{
				"run_id":    fb.RunID,
				"series_id": fb.SeriesID,
			},
		}
		if fb.VerdictCorrect != nil {
			d.Extra["verdict_correct"] = *fb.VerdictCorrect
		}
		if fb.VerdictNotes != "" {
			d.Extra["verdict_notes"] = fb.VerdictNotes
		}
		writeJSON(w, http.StatusOK, d)

	default:
		writeError(w, http.StatusBadRequest, "unknown decision ID prefix: "+id)
	}
}

func gateDecisionSummary(run *audit.PlaybookRun) string {
	if run.TransitionedTo != "" {
		return "Triage complete — TRANSITION_TO " + run.TransitionedTo
	}
	return "Triage complete — ESCALATE_TO " + run.EscalatedTo
}

func gateDecisionExtra(run *audit.PlaybookRun) map[string]any {
	extra := map[string]any{
		"findings":  run.FindingsSummary,
		"series_id": run.SeriesID,
	}
	if run.TransitionedTo != "" {
		extra["gate_type"] = "transition"
		extra["transition_target"] = run.TransitionedTo
	} else {
		extra["gate_type"] = "escalation"
		extra["escalation_target"] = run.EscalatedTo
	}
	if run.DiagnosticReport != nil {
		extra["diagnostic_report"] = run.DiagnosticReport
	}
	return extra
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
			Summary:     gateDecisionSummary(run),
			RequestedBy: run.Operator,
			RequestedAt: run.StartedAt,
			ResolveURL:  baseURL + "/api/v1/decisions/" + resolveID + "/resolve",
			Extra:       gateDecisionExtra(run),
		}
		nextSeries := run.TransitionedTo
		if nextSeries == "" {
			nextSeries = run.EscalatedTo
		}
		if nextSeries != "" {
			if nextPB, err := g.fetchPlaybookBySeriesID(ctx, nextSeries); err == nil {
				d.Extra["remediation_preview"] = map[string]any{
					"series_id":     nextPB.SeriesID,
					"name":          nextPB.Name,
					"description":   nextPB.Description,
					"approval_mode": nextPB.ApprovalMode,
				}
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// fetchPendingFeedback calls GET /v1/fleet/playbook-runs/feedback-pending on
// auditd and maps results to feedback Decision values.
func (g *Gateway) fetchPendingFeedback(ctx context.Context, limit int) ([]decisions.Decision, error) {
	url := fmt.Sprintf("%s/v1/fleet/playbook-runs/feedback-pending",
		strings.TrimSuffix(g.auditURL, "/"))

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
		return nil, fmt.Errorf("auditd feedback-pending returned %d", resp.StatusCode)
	}

	var items []struct {
		RunID       string    `json:"run_id"`
		SeriesID    string    `json:"series_id"`
		Operator    string    `json:"operator"`
		SubmittedAt time.Time `json:"submitted_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}

	baseURL := strings.TrimSuffix(g.baseURL, "/")
	var out []decisions.Decision
	for i, item := range items {
		if i >= limit {
			break
		}
		decisionID := "feedback:" + item.RunID
		out = append(out, decisions.Decision{
			ID:          decisionID,
			Type:        decisions.DecisionTypeFeedback,
			Status:      "pending",
			Summary:     "Diagnosis feedback needed — " + item.SeriesID,
			RequestedBy: item.Operator,
			RequestedAt: item.SubmittedAt,
			ResolveURL:  baseURL + "/api/v1/decisions/" + decisionID + "/resolve",
			Extra: map[string]any{
				"run_id":    item.RunID,
				"series_id": item.SeriesID,
			},
		})
	}
	return out, nil
}

// resolveFeedback handles feedback decision resolution.
// resolution="approved" → verdict_correct=true; "denied" → verdict_correct=false.
// reason becomes verdict_notes. Stored as (feedback_type="triage", feedback_time="post_incident").
func (g *Gateway) resolveFeedback(w http.ResponseWriter, r *http.Request, runID, resolution, resolvedBy, reason string) {
	verdictCorrect := resolution == "approved"
	payload := map[string]any{
		"verdict_correct": verdictCorrect,
		"feedback_type":   "triage",
		"feedback_time":   "post_incident",
	}
	if resolvedBy != "" {
		payload["operator"] = resolvedBy
	}
	if reason != "" {
		payload["verdict_notes"] = reason
	}
	body, _ := json.Marshal(payload)

	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbook-runs/" + runID + "/feedback"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build auditd request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	if xUser := r.Header.Get("X-User"); xUser != "" {
		req.Header.Set("X-User", xUser)
	} else if resolvedBy != "" {
		req.Header.Set("X-User", resolvedBy)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "auditd request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(b) //nolint:errcheck
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "resolved",
		"verdict_correct": verdictCorrect,
	})
}
