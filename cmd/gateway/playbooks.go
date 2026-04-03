package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"helpdesk/internal/audit"
)

// proxyToAuditd forwards the request to the auditd service at the given path
// and copies the response back to w. The request body is forwarded as-is.
func (g *Gateway) proxyToAuditd(w http.ResponseWriter, r *http.Request, auditPath string) {
	if g.auditURL == "" {
		writeError(w, http.StatusServiceUnavailable, "auditd URL not configured")
		return
	}
	url := strings.TrimSuffix(g.auditURL, "/") + auditPath

	// Build forwarded request.
	var body io.Reader
	if r.Body != nil {
		body = r.Body
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build proxy request: "+err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	// Authenticate to auditd using the gateway's own service account key.
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	// Forward the originating user identity so auditd can record who made the change.
	if user := r.Header.Get("X-User"); user != "" {
		req.Header.Set("X-User", user)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "auditd request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to read auditd response: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody) //nolint:errcheck
}

// handlePlaybookCreate proxies POST /api/v1/fleet/playbooks → auditd.
func (g *Gateway) handlePlaybookCreate(w http.ResponseWriter, r *http.Request) {
	g.proxyToAuditd(w, r, "/v1/fleet/playbooks")
}

// handlePlaybookList proxies GET /api/v1/fleet/playbooks → auditd, forwarding
// query parameters (active_only, include_system, series_id).
func (g *Gateway) handlePlaybookList(w http.ResponseWriter, r *http.Request) {
	path := "/v1/fleet/playbooks"
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	g.proxyToAuditd(w, r, path)
}

// handlePlaybookGet proxies GET /api/v1/fleet/playbooks/{id} → auditd.
func (g *Gateway) handlePlaybookGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("playbookID")
	g.proxyToAuditd(w, r, "/v1/fleet/playbooks/"+id)
}

// handlePlaybookUpdate proxies PUT /api/v1/fleet/playbooks/{id} → auditd.
func (g *Gateway) handlePlaybookUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("playbookID")
	g.proxyToAuditd(w, r, "/v1/fleet/playbooks/"+id)
}

// handlePlaybookDelete proxies DELETE /api/v1/fleet/playbooks/{id} → auditd.
func (g *Gateway) handlePlaybookDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("playbookID")
	g.proxyToAuditd(w, r, "/v1/fleet/playbooks/"+id)
}

// handlePlaybookActivate proxies POST /api/v1/fleet/playbooks/{id}/activate → auditd.
func (g *Gateway) handlePlaybookActivate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("playbookID")
	g.proxyToAuditd(w, r, "/v1/fleet/playbooks/"+id+"/activate")
}

// PlaybookRunRequest is the optional request body for POST /api/v1/fleet/playbooks/{id}/run.
// For fleet-mode playbooks the body is ignored; the planner uses the playbook's own
// description, target_hints, and guidance. For agent-mode playbooks, connection_string
// and context are injected into the triage prompt.
type PlaybookRunRequest struct {
	ConnectionString string `json:"connection_string,omitempty"`
	Context          string `json:"context,omitempty"`    // operator-supplied context (server name, symptoms, etc.)
	ContextID        string `json:"context_id,omitempty"` // A2A session ID for multi-turn continuity
}

// handlePlaybookRun handles POST /api/v1/fleet/playbooks/{id}/run.
// Routes to the fleet planner (execution_mode="fleet") or the database agent
// (execution_mode="agent") based on the playbook's execution_mode field.
func (g *Gateway) handlePlaybookRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("playbookID")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing playbook ID")
		return
	}
	if g.auditURL == "" {
		writeError(w, http.StatusServiceUnavailable, "auditd URL not configured")
		return
	}

	// Parse optional request body (ignore errors — body is optional for fleet mode).
	var req PlaybookRunRequest
	if r.Body != nil {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &req) //nolint:errcheck
	}

	// Fetch the playbook from auditd.
	pb, err := g.fetchPlaybook(r.Context(), id)
	if err != nil {
		slog.Error("handlePlaybookRun: failed to fetch playbook", "id", id, "err", err)
		writeError(w, http.StatusNotFound, fmt.Sprintf("playbook %q not found: %v", id, err))
		return
	}

	// Record the run start. Best-effort: failure does not block execution.
	operator := r.Header.Get("X-User")
	runID := g.recordPlaybookRunStart(r.Context(), pb, req.ContextID, operator)

	if pb.ExecutionMode == "agent" {
		g.handlePlaybookRunAsAgent(w, r, pb, req, runID)
		return
	}

	// Fleet path: build a synthetic FleetPlanRequest and delegate to handleFleetPlan.
	if g.plannerLLM == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet planner LLM not configured")
		return
	}
	planReqBody, err := json.Marshal(FleetPlanRequest{
		Description: pb.Description,
		TargetHints: pb.TargetHints,
		Guidance:    pb.Guidance,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build plan request: "+err.Error())
		return
	}
	planReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, r.URL.Path, strings.NewReader(string(planReqBody)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build plan request: "+err.Error())
		return
	}
	for _, h := range []string{"X-User", "X-API-Key", "Authorization"} {
		if v := r.Header.Get(h); v != "" {
			planReq.Header.Set(h, v)
		}
	}
	g.handleFleetPlan(w, planReq)
	// Fleet runs complete synchronously; outcome is unknown until operator
	// reviews and approves the plan. Record completion best-effort.
	if runID != "" {
		go g.recordPlaybookRunComplete(context.WithoutCancel(r.Context()), runID, "unknown", "", "")
	}
}

// handlePlaybookRunAsAgent routes an agent-mode playbook run to the database agent
// as an interactive agentic session. The playbook's guidance is injected into the
// prompt as expert context. All tool calls within the session are expected to be
// read-only; the agent presents remediation as recommendations for operator review.
func (g *Gateway) handlePlaybookRunAsAgent(w http.ResponseWriter, r *http.Request, pb *audit.Playbook, req PlaybookRunRequest, runID string) {
	prompt := assembleTriagePrompt(pb, req)
	g.proxyToAgent(w, r, agentNameDB, req.ContextID, prompt)
	// Mark run completed with outcome=unknown — the operator updates it via PATCH
	// /api/v1/fleet/playbook-runs/{runID} once they've reviewed the diagnosis.
	if runID != "" {
		go g.recordPlaybookRunComplete(context.WithoutCancel(r.Context()), runID, "unknown", "", "")
	}
}

// assembleTriagePrompt builds the LLM prompt for an agent-mode playbook run.
func assembleTriagePrompt(pb *audit.Playbook, req PlaybookRunRequest) string {
	var b strings.Builder

	b.WriteString("You are performing a database availability investigation.\n\n")
	fmt.Fprintf(&b, "## Playbook: %s\n\n", pb.Name)

	if pb.Description != "" {
		fmt.Fprintf(&b, "## Objective\n%s\n\n", pb.Description)
	}
	if pb.Guidance != "" {
		fmt.Fprintf(&b, "## Expert Guidance\n%s\n\n", pb.Guidance)
	}
	if len(pb.EscalatesTo) > 0 {
		fmt.Fprintf(&b, "## Escalation paths\nIf your investigation reveals a different root cause than this playbook addresses, the next playbooks to consider are (by series ID): %s\n\n",
			strings.Join(pb.EscalatesTo, ", "))
	}

	b.WriteString("## Constraints\n")
	b.WriteString("- Use only read-only diagnostic tools. Do not execute any write or destructive operations.\n")
	b.WriteString("- Collect evidence, form a hypothesis, and if the evidence contradicts it, back out and pursue a different hypothesis.\n")
	b.WriteString("- When you reach a clear diagnosis, present your findings and recommended remediation steps.\n")
	b.WriteString("- Do NOT execute remediation — describe it for operator review and approval.\n\n")

	if req.ConnectionString != "" {
		fmt.Fprintf(&b, "## Target\nConnection string: `%s`\n\n", req.ConnectionString)
	}
	if req.Context != "" {
		fmt.Fprintf(&b, "## Additional context\n%s\n\n", req.Context)
	}

	return b.String()
}

// fetchPlaybook retrieves a single playbook record from auditd.
func (g *Gateway) fetchPlaybook(ctx context.Context, id string) (*audit.Playbook, error) {
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbooks/" + id
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req = req.WithContext(ctx2)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auditd returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var pb audit.Playbook
	if err := json.Unmarshal(body, &pb); err != nil {
		return nil, err
	}
	return &pb, nil
}

// recordPlaybookRunStart posts a new run record to auditd and returns the run_id.
// Best-effort: returns "" on any failure so callers can proceed without blocking.
func (g *Gateway) recordPlaybookRunStart(ctx context.Context, pb *audit.Playbook, contextID, operator string) string {
	if g.auditURL == "" {
		return ""
	}
	run := audit.PlaybookRun{
		PlaybookID:    pb.PlaybookID,
		SeriesID:      pb.SeriesID,
		ExecutionMode: pb.ExecutionMode,
		ContextID:     contextID,
		Operator:      operator,
	}
	body, err := json.Marshal(run)
	if err != nil {
		return ""
	}
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbooks/" + pb.PlaybookID + "/runs"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req = req.WithContext(ctx2)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("recordPlaybookRunStart: request failed", "err", err)
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		slog.Warn("recordPlaybookRunStart: unexpected status", "status", resp.StatusCode)
		return ""
	}
	var created audit.PlaybookRun
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return ""
	}
	return created.RunID
}

// recordPlaybookRunComplete patches an existing run with its final outcome.
// Best-effort: failures are logged but not returned.
func (g *Gateway) recordPlaybookRunComplete(ctx context.Context, runID, outcome, escalatedTo, findingsSummary string) {
	if g.auditURL == "" || runID == "" {
		return
	}
	body, _ := json.Marshal(map[string]string{
		"outcome":          outcome,
		"escalated_to":     escalatedTo,
		"findings_summary": findingsSummary,
	})
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbook-runs/" + runID
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, strings.NewReader(string(body)))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req = req.WithContext(ctx2)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("recordPlaybookRunComplete: request failed", "run_id", runID, "err", err)
		return
	}
	resp.Body.Close()
}
