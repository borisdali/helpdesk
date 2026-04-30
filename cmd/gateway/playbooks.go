package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"helpdesk/internal/audit"
	"helpdesk/internal/authz"
	"helpdesk/internal/identity"
	"helpdesk/internal/infra"
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

// ctxKeyApprovalSession is the context key for the approval session ID.
type ctxKeyApprovalSessionType struct{}

var ctxKeyApprovalSession = ctxKeyApprovalSessionType{}

// approvalContext carries approval mode + session ID through the request context.
type approvalContext struct {
	mode      string // "auto" | "session" | "manual"
	sessionID string
}

// PlaybookRunRequest is the optional request body for POST /api/v1/fleet/playbooks/{id}/run.
// For fleet-mode playbooks the body is ignored; the planner uses the playbook's own
// description, target_hints, and guidance. For agent-mode playbooks, connection_string
// and context are injected into the triage prompt.
type PlaybookRunRequest struct {
	ConnectionString string `json:"connection_string,omitempty"`
	Context          string `json:"context,omitempty"`    // operator-supplied context (server name, symptoms, etc.)
	ContextID        string `json:"context_id,omitempty"` // A2A session ID for multi-turn continuity
	PriorRunID       string `json:"prior_run_id,omitempty"` // run_id of prior investigation for continuity threading
	PriorFindings    string `json:"-"`                       // populated at runtime from prior run; not from body

	// ApprovalMode controls when approval is required for write/destructive operations.
	//   "auto"    (default) — no gate; current behavior.
	//   "session" — operator must supply a valid ApprovalSession ID.
	//   "manual"  — agent-mode runs are read-only (no write/destructive proxied).
	ApprovalMode    string `json:"approval_mode,omitempty"`
	// ApprovalSession is the session ID for "session" mode. Required when ApprovalMode="session".
	ApprovalSession string `json:"approval_session,omitempty"`
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

	// Item 5: soft-warn when required evidence patterns are absent from the
	// operator-supplied context. Execution is not blocked.
	warnings := checkRequiresEvidence(pb.RequiresEvidence, req.Context)

	// Item 6: soft-warn when the operator context is inconsistent with the
	// server's known hosting type (e.g. K8s terms for a Docker-hosted server).
	warnings = append(warnings, checkContextConsistency(g.infra, req.ConnectionString, req.Context)...)

	// Item 4: continuity threading — fetch prior run's findings to seed the prompt.
	if req.PriorRunID != "" {
		if prior, err := g.fetchPlaybookRun(r.Context(), req.PriorRunID); err == nil {
			req.PriorFindings = prior.FindingsSummary
		} else {
			slog.Warn("handlePlaybookRun: could not fetch prior run for continuity", "prior_run_id", req.PriorRunID, "err", err)
		}
	}

	// Record the run start. Best-effort: failure does not block execution.
	operator := r.Header.Get("X-User")
	runID := g.recordPlaybookRunStart(r.Context(), pb, req.ContextID, operator)

	if pb.ExecutionMode == "agent" {
		g.handlePlaybookRunAsAgent(w, r, pb, req, runID, warnings)
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

	// Capture the fleet plan response so we can inject warnings if needed.
	capture := newResponseCapture()
	g.handleFleetPlan(capture, planReq)

	extra := map[string]any{}
	if len(warnings) > 0 {
		extra["warnings"] = warnings
	}
	injectFields(w, capture, extra)

	// Fleet runs complete synchronously; outcome is unknown until operator
	// reviews and approves the plan. Record completion best-effort.
	if runID != "" {
		go g.recordPlaybookRunComplete(context.WithoutCancel(r.Context()), runID, "unknown", "", "", nil)
	}
}

// handlePlaybookRunAsAgent routes an agent-mode playbook run to the database agent
// as an interactive agentic session. The playbook's guidance is injected into the
// prompt as expert context. All tool calls within the session are expected to be
// read-only; the agent presents remediation as recommendations for operator review.
//
// The agent response is captured to:
//   - Parse structured escalation signals (ESCALATE_TO / FINDINGS lines)
//   - Strip signal lines from the operator-visible text
//   - Record the run with the real outcome instead of "unknown"
//   - Inject optional warnings about missing required evidence
func (g *Gateway) handlePlaybookRunAsAgent(w http.ResponseWriter, r *http.Request, pb *audit.Playbook, req PlaybookRunRequest, runID string, warnings []string) {
	prompt := assembleTriagePrompt(pb, req)

	// Propagate approval mode and session ID through context so proxyToAgentWithTool
	// can enforce them before proxying write/destructive calls.
	ctx := r.Context()
	if req.ApprovalMode != "" {
		ctx = context.WithValue(ctx, ctxKeyApprovalSession, approvalContext{
			mode:      req.ApprovalMode,
			sessionID: req.ApprovalSession,
		})
		r = r.WithContext(ctx)
	}

	// Capture the agent response to parse escalation signals.
	runStart := time.Now()
	capture := newResponseCapture()
	g.proxyToAgent(capture, r, agentNameDB, req.ContextID, prompt)

	// Extract trace ID from the capture response header once; reused for
	// escalation auditing and post-run target-scope verification below.
	traceID := capture.header.Get("X-Trace-ID")
	if traceID == "" {
		traceID = capture.header.Get("X-Trace-Id")
	}

	var outcome, escalatedTo, findings string
	extra := map[string]any{}

	var diagReport *audit.DiagnosticReport

	if capture.code == http.StatusOK {
		var respBody map[string]any
		if err := json.Unmarshal(capture.body.Bytes(), &respBody); err == nil {
			if text, ok := respBody["text"].(string); ok {
				// Parse structured hypotheses first; fall through to flat parser for
				// FINDINGS/ESCALATE_TO which are always present in both formats.
				diagReport = parseDiagnosticReport(text)

				esc := parseAgentEscalation(text)
				findings = esc.Findings
				if esc.EscalateTo != "" {
					outcome = "escalated"
					escalatedTo = esc.EscalateTo
					extra["escalation_hint"] = esc.EscalateTo
					// Audit the playbook selection decision so the escalation
					// chain is visible in QueryJourneys.
					g.recordEscalationDecision(r.Context(), traceID,
						authz.PrincipalFromContext(r.Context()), pb, esc.EscalateTo, findings)
				} else if findings != "" {
					outcome = "resolved"
				}
				// Replace text with the clean version (signal lines stripped).
				respBody["text"] = esc.CleanText
				if b, err := json.Marshal(respBody); err == nil {
					capture.body.Reset()
					capture.body.Write(b) //nolint:errcheck
				}
			}
		}
	}
	if outcome == "" {
		outcome = "unknown"
	}

	if runID != "" {
		extra["run_id"] = runID
	}
	if findings != "" {
		extra["findings"] = findings
	}
	if diagReport != nil {
		extra["diagnostic_report"] = diagReport
	}
	if len(warnings) > 0 {
		extra["warnings"] = warnings
	}

	// Post-run: check whether the agent operated on the intended target only.
	// Drift means the agent queried a server other than the one specified in the run request.
	if req.ConnectionString != "" {
		if drift := checkTargetScope(g.auditURL, g.auditAPIKey, traceID, runStart, req.ConnectionString); len(drift) > 0 {
			extra["target_drift"] = drift
			slog.Warn("playbook run: target scope drift detected",
				"trace_id", traceID,
				"intended", req.ConnectionString,
				"actual", drift)
		}
	}

	injectFields(w, capture, extra)

	// Record completion with real outcome in background.
	if runID != "" {
		go g.recordPlaybookRunComplete(context.WithoutCancel(r.Context()), runID, outcome, escalatedTo, findings, diagReport)
	}
}

// recordEscalationDecision emits a delegation_decision audit event when an
// agent-mode playbook run signals escalation to a follow-on playbook.
// This closes the audit gap between "playbook ran" and "next playbook triggered",
// making the full escalation chain visible in QueryJourneys.
func (g *Gateway) recordEscalationDecision(ctx context.Context, traceID string, principal identity.ResolvedPrincipal, pb *audit.Playbook, nextSeriesID, findings string) {
	if g.auditor == nil {
		return
	}
	if traceID == "" {
		traceID = audit.NewTraceID()
	}

	reasoningChain := []string{
		"agent signalled ESCALATE_TO during playbook run: " + pb.SeriesID,
		"escalating to next playbook: " + nextSeriesID,
	}
	if findings != "" {
		reasoningChain = append(reasoningChain, "findings: "+findings)
	}

	var p *identity.ResolvedPrincipal
	if principal.EffectiveID() != "" {
		p = &principal
	}

	event := &audit.Event{
		EventID:   "ps_" + uuid.New().String()[:8],
		Timestamp: time.Now().UTC(),
		EventType: audit.EventTypeDelegation,
		TraceID:   traceID,
		Principal: p,
		Session:   audit.Session{ID: traceID},
		Input: audit.Input{
			UserQuery: "playbook escalation from " + pb.SeriesID,
		},
		Decision: &audit.Decision{
			Agent:           nextSeriesID,
			RequestCategory: audit.CategoryIncident,
			Confidence:      1.0,
			UserIntent:      "escalate from playbook " + pb.SeriesID + " to " + nextSeriesID,
			ReasoningChain:  reasoningChain,
		},
		Outcome: &audit.Outcome{Status: "success"},
	}

	if err := g.auditor.RecordEvent(ctx, event); err != nil {
		slog.Warn("playbook: failed to record escalation decision", "trace_id", traceID, "err", err)
	}
}

// assembleTriagePrompt builds the LLM prompt for an agent-mode playbook run.
func assembleTriagePrompt(pb *audit.Playbook, req PlaybookRunRequest) string {
	var b strings.Builder

	b.WriteString("You are performing a database availability investigation.\n\n")

	// Response protocol first — models attend more reliably to instructions at the top.
	b.WriteString("## Response Protocol\n")
	b.WriteString("Do NOT write a CONCLUSION section or end with '---'. Close your response with this exact block (plain text, no markdown, no bold, no backticks):\n\n")
	b.WriteString("HYPOTHESIS_1: <primary hypothesis> | CONFIDENCE: <0.0–1.0> | EVIDENCE: \"<verbatim quote from tool output>\"\n")
	b.WriteString("HYPOTHESIS_2: <alternative hypothesis> | CONFIDENCE: <0.0–1.0> | REJECTED: <one-sentence reason>\n")
	b.WriteString("ROOT_CAUSE: HYPOTHESIS_1\n")
	b.WriteString("FINDINGS: <one-sentence diagnosis and recommended action>\n")
	b.WriteString("ESCALATE_TO: <series_id or \"none\">\n\n")
	b.WriteString("Rules: list hypotheses in descending confidence order; EVIDENCE must be a short verbatim quote from a tool output; every non-primary hypothesis must have REJECTED with a reason; CONFIDENCE is 0.0–1.0.\n\n")

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

	// Item 4: inject prior investigation findings for continuity.
	if req.PriorFindings != "" {
		fmt.Fprintf(&b, "## Prior Investigation Findings\nA previous investigation reached the following conclusion:\n%s\n\nContinue from this context and investigate further.\n\n", req.PriorFindings)
	}

	b.WriteString("## Constraints\n")
	b.WriteString("- Use only read-only diagnostic tools. Do not execute any write or destructive operations.\n")
	b.WriteString("- Collect evidence, form a hypothesis, and if the evidence contradicts it, back out and pursue a different hypothesis.\n")
	b.WriteString("- When you reach a clear diagnosis, present your findings and recommended remediation steps.\n")
	b.WriteString("- Do NOT execute remediation — describe it for operator review and approval.\n\n")

	if req.ConnectionString != "" {
		fmt.Fprintf(&b, "## Target — MANDATORY SCOPE CONSTRAINT\nYou MUST use ONLY `connection_string` = `%s` for all tool calls. Do not query any other database server under any circumstances, even if the context mentions other servers, pods, or clusters.\n\n", req.ConnectionString)
	}
	if req.Context != "" {
		fmt.Fprintf(&b, "## Additional context\n%s\n\n", req.Context)
	}

	slog.Debug("triage prompt assembled", "playbook", pb.SeriesID, "prompt_len", b.Len(), "prompt", b.String())

	// Closing reminder — Gemini attends more reliably when the instruction is
	// repeated as the last thing in the prompt. Explicitly forbid the patterns
	// Gemini uses as alternatives (**CONCLUSION:** and trailing ---).
	b.WriteString("IMPORTANT: Do not write **CONCLUSION:** or end with ---. Close with exactly:\n")
	b.WriteString("HYPOTHESIS_1: ... | CONFIDENCE: ... | EVIDENCE: \"...\"\n")
	b.WriteString("HYPOTHESIS_2: ... | CONFIDENCE: ... | REJECTED: ...\n")
	b.WriteString("ROOT_CAUSE: HYPOTHESIS_1\n")
	b.WriteString("FINDINGS: <one-sentence diagnosis>\n")
	b.WriteString("ESCALATE_TO: <series_id or \"none\">\n")

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

// fetchPlaybookRun retrieves a single run record from auditd by run_id.
func (g *Gateway) fetchPlaybookRun(ctx context.Context, runID string) (*audit.PlaybookRun, error) {
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbook-runs/" + runID
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
	var run audit.PlaybookRun
	if err := json.Unmarshal(body, &run); err != nil {
		return nil, err
	}
	return &run, nil
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
		slog.Error("recordPlaybookRunStart: request failed — run not recorded", "playbook_id", pb.PlaybookID, "err", err)
		return ""
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		slog.Error("recordPlaybookRunStart: run not recorded",
			"playbook_id", pb.PlaybookID,
			"status", resp.StatusCode,
			"auditd_error", strings.TrimSpace(string(respBody)),
		)
		return ""
	}
	var created audit.PlaybookRun
	if err := json.NewDecoder(bytes.NewReader(respBody)).Decode(&created); err != nil {
		slog.Error("recordPlaybookRunStart: failed to decode run response", "playbook_id", pb.PlaybookID, "err", err)
		return ""
	}
	return created.RunID
}

// recordPlaybookRunComplete patches an existing run with its final outcome.
// Best-effort: failures are logged but not returned.
func (g *Gateway) recordPlaybookRunComplete(ctx context.Context, runID, outcome, escalatedTo, findingsSummary string, report *audit.DiagnosticReport) {
	if g.auditURL == "" || runID == "" {
		return
	}
	payload := map[string]any{
		"outcome":          outcome,
		"escalated_to":     escalatedTo,
		"findings_summary": findingsSummary,
	}
	if report != nil {
		payload["diagnostic_report"] = report
	}
	body, _ := json.Marshal(payload)
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
		slog.Error("recordPlaybookRunComplete: run outcome not recorded — run stuck at outcome=unknown", "run_id", runID, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		slog.Error("recordPlaybookRunComplete: unexpected status — run stuck at outcome=unknown",
			"run_id", runID,
			"status", resp.StatusCode,
			"auditd_error", strings.TrimSpace(string(body)),
		)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// responseCapture is a minimal http.ResponseWriter that buffers the response
// so callers can inspect and modify it before forwarding to the real writer.
type responseCapture struct {
	header http.Header
	body   bytes.Buffer
	code   int
}

func newResponseCapture() *responseCapture {
	return &responseCapture{header: make(http.Header), code: http.StatusOK}
}

func (rc *responseCapture) Header() http.Header         { return rc.header }
func (rc *responseCapture) Write(b []byte) (int, error) { return rc.body.Write(b) }
func (rc *responseCapture) WriteHeader(code int)        { rc.code = code }

// injectFields merges additionalFields into a captured JSON object response
// and writes the result to w. If the body is not a JSON object or
// additionalFields is empty, the captured response is written unchanged.
func injectFields(w http.ResponseWriter, capture *responseCapture, additionalFields map[string]any) {
	// Copy captured headers to real writer first.
	for k, vals := range capture.header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}

	if len(additionalFields) == 0 || capture.code != http.StatusOK {
		w.WriteHeader(capture.code)
		w.Write(capture.body.Bytes()) //nolint:errcheck
		return
	}

	var obj map[string]any
	if err := json.Unmarshal(capture.body.Bytes(), &obj); err != nil {
		// Not a JSON object — forward as-is.
		w.WriteHeader(capture.code)
		w.Write(capture.body.Bytes()) //nolint:errcheck
		return
	}
	for k, v := range additionalFields {
		obj[k] = v
	}
	b, err := json.Marshal(obj)
	if err != nil {
		w.WriteHeader(capture.code)
		w.Write(capture.body.Bytes()) //nolint:errcheck
		return
	}
	w.WriteHeader(capture.code)
	w.Write(b) //nolint:errcheck
}

// agentEscalation holds the structured signals parsed from an agent response.
type agentEscalation struct {
	EscalateTo string // series_id to pass to the next playbook, or ""
	Findings   string // one-sentence diagnosis summary
	CleanText  string // response text with signal lines removed
}

// parseAgentEscalation scans the agent's response text for structured signal
// lines injected by the response protocol section of the triage prompt:
//
//	FINDINGS: <summary>
//	ESCALATE_TO: <series_id>
//
// These lines are stripped from the visible text returned to the operator.
// As a fallback, if no FINDINGS: line is present, the function extracts the
// content of a **CONCLUSION:** paragraph (Gemini's preferred alternative).
func parseAgentEscalation(text string) agentEscalation {
	var result agentEscalation
	var cleaned strings.Builder
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		// Normalise markdown bold: **FINDINGS:** → FINDINGS:
		trimmed = strings.NewReplacer("**FINDINGS:**", "FINDINGS:", "**ESCALATE_TO:**", "ESCALATE_TO:").Replace(trimmed)
		if strings.HasPrefix(trimmed, "ESCALATE_TO:") {
			v := strings.TrimSpace(strings.TrimPrefix(trimmed, "ESCALATE_TO:"))
			if v != "none" && v != "" {
				result.EscalateTo = v
			}
		} else if strings.HasPrefix(trimmed, "FINDINGS:") {
			result.Findings = strings.TrimSpace(strings.TrimPrefix(trimmed, "FINDINGS:"))
		} else {
			cleaned.WriteString(line)
			cleaned.WriteByte('\n')
		}
	}
	result.CleanText = strings.TrimRight(cleaned.String(), "\n")

	// Fallback: extract findings from **CONCLUSION:** when the model ignored the protocol.
	if result.Findings == "" {
		result.Findings = extractConclusionFallback(result.CleanText)
	}
	return result
}

// parseDiagnosticReport scans the agent response for HYPOTHESIS_N: lines and
// parses them into a DiagnosticReport. Returns nil when no hypothesis lines are
// found (backward compat — caller falls through to parseAgentEscalation).
//
// Expected line format:
//
//	HYPOTHESIS_1: <text> | CONFIDENCE: 0.85 | EVIDENCE: "<quote>"
//	HYPOTHESIS_2: <text> | CONFIDENCE: 0.30 | REJECTED: <reason>
//	ROOT_CAUSE: HYPOTHESIS_1
//	ACTION_TAKEN: <what was done>
func parseDiagnosticReport(text string) *audit.DiagnosticReport {
	var hypotheses []audit.DiagnosticHypothesis
	var rootCauseRef, actionTaken string

	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		// Strip markdown bold markers so **HYPOTHESIS_N:** is handled identically
		// to plain HYPOTHESIS_N:. Also handles trailing ** on the same token.
		trimmed = strings.TrimLeft(trimmed, "*")

		// HYPOTHESIS_N: ...
		if hypMatch := matchHypothesisLine(trimmed); hypMatch != nil {
			hypotheses = append(hypotheses, *hypMatch)
			continue
		}
		if strings.HasPrefix(trimmed, "ROOT_CAUSE:") {
			rootCauseRef = strings.TrimSpace(strings.TrimPrefix(trimmed, "ROOT_CAUSE:"))
			continue
		}
		if strings.HasPrefix(trimmed, "ACTION_TAKEN:") {
			actionTaken = strings.TrimSpace(strings.TrimPrefix(trimmed, "ACTION_TAKEN:"))
		}
	}
	if len(hypotheses) == 0 {
		return nil
	}

	// Mark the primary hypothesis. ROOT_CAUSE: HYPOTHESIS_N (1-based index).
	primaryRank := 1
	if strings.HasPrefix(rootCauseRef, "HYPOTHESIS_") {
		if n, err := strconv.Atoi(strings.TrimPrefix(rootCauseRef, "HYPOTHESIS_")); err == nil {
			primaryRank = n
		}
	}
	rootCauseText := ""
	for i := range hypotheses {
		if hypotheses[i].Rank == primaryRank {
			hypotheses[i].IsPrimary = true
			rootCauseText = hypotheses[i].Text
		}
	}

	return &audit.DiagnosticReport{
		Hypotheses:  hypotheses,
		RootCause:   rootCauseText,
		ActionTaken: actionTaken,
	}
}

// matchHypothesisLine parses a single HYPOTHESIS_N: ... line.
// Returns nil if the line does not match.
func matchHypothesisLine(line string) *audit.DiagnosticHypothesis {
	// Must start with HYPOTHESIS_<digit>:
	upper := strings.ToUpper(line)
	if !strings.HasPrefix(upper, "HYPOTHESIS_") {
		return nil
	}
	colonIdx := strings.Index(line, ":")
	if colonIdx < 0 {
		return nil
	}
	rankStr := line[len("HYPOTHESIS_"):colonIdx]
	rank, err := strconv.Atoi(rankStr)
	if err != nil {
		return nil
	}

	rest := strings.TrimSpace(line[colonIdx+1:])
	h := audit.DiagnosticHypothesis{Rank: rank}

	// Split on " | " to get fields.
	parts := strings.Split(rest, " | ")
	if len(parts) == 0 {
		return nil
	}
	h.Text = strings.TrimSpace(parts[0])

	for _, part := range parts[1:] {
		part = strings.TrimSpace(part)
		if after, ok := strings.CutPrefix(part, "CONFIDENCE:"); ok {
			c, err := strconv.ParseFloat(strings.TrimSpace(after), 64)
			if err == nil {
				h.Confidence = c
			}
		} else if after, ok := strings.CutPrefix(part, "EVIDENCE:"); ok {
			ev := strings.TrimSpace(after)
			ev = strings.Trim(ev, "\"")
			h.Evidence = ev
		} else if after, ok := strings.CutPrefix(part, "REJECTED:"); ok {
			h.RejectedReason = strings.TrimSpace(after)
		}
	}
	return &h
}

// extractConclusionFallback extracts a findings summary from common conclusion
// patterns that LLMs use instead of the structured FINDINGS: line:
//
//   - Inline: "**CONCLUSION:** ..." or "CONCLUSION: ..."
//   - Section heading followed by a bold status line:
//     "## Findings Summary\n\n**DB status: HEALTHY**"
//   - Any standalone bold line containing a status keyword (last resort)
func extractConclusionFallback(text string) string {
	lines := strings.Split(text, "\n")

	// Pass 1: inline CONCLUSION: prefix on a single line.
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, prefix := range []string{"**CONCLUSION:**", "**Conclusion:**", "CONCLUSION:"} {
			if strings.HasPrefix(trimmed, prefix) {
				v := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
				v = strings.Trim(v, "* ")
				if v != "" {
					return v
				}
			}
		}
	}

	// Pass 2: section heading ("## Findings Summary", "## Summary", "## Conclusion")
	// followed by the first non-empty, non-heading line.
	headingRe := regexp.MustCompile(`(?i)^#{1,3}\s+(findings\s+summary|summary|conclusion|investigation\s+summary)`)
	for i, line := range lines {
		if headingRe.MatchString(strings.TrimSpace(line)) {
			for _, after := range lines[i+1:] {
				v := strings.TrimSpace(after)
				if v == "" || strings.HasPrefix(v, "#") {
					continue
				}
				// Strip markdown bold/italic markers.
				v = strings.Trim(v, "* _")
				if v != "" {
					return v
				}
			}
		}
	}

	// Pass 3: last-resort — any standalone bold line in the final third of the
	// response containing a status keyword (OPERATIONAL, HEALTHY, DOWN, etc.).
	statusRe := regexp.MustCompile(`(?i)(operational|healthy|unavailable|unreachable|down|resolved|escalat)`)
	start := len(lines) * 2 / 3
	if start < 0 {
		start = 0
	}
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "**") && strings.HasSuffix(trimmed, "**") && statusRe.MatchString(trimmed) {
			v := strings.Trim(trimmed, "* ")
			if v != "" {
				return v
			}
		}
	}

	return ""
}

// checkRequiresEvidence checks whether any of the playbook's required evidence
// patterns are absent from the provided context string. Returns a warning
// message for each unmatched pattern. Returns nil when RequiresEvidence is empty.
// When context is empty all patterns are considered absent (the operator has not
// supplied log snippets confirming the hypothesis).
func checkRequiresEvidence(patterns []string, ctx string) []string {
	if len(patterns) == 0 {
		return nil
	}
	var missing []string
	for _, pat := range patterns {
		var matched bool
		if ctx != "" {
			re, err := regexp.Compile("(?i)" + pat)
			if err == nil {
				matched = re.MatchString(ctx)
			} else {
				// Fall back to case-insensitive substring match for invalid regexps.
				matched = strings.Contains(strings.ToLower(ctx), strings.ToLower(pat))
			}
		}
		if !matched {
			missing = append(missing, fmt.Sprintf("expected evidence pattern not found in provided context: %q", pat))
		}
	}
	return missing
}

// checkContextConsistency warns when the operator-supplied context contains
// terminology inconsistent with the server's known hosting type. For example,
// K8s terms (pod, kubectl, namespace) on a Docker-hosted server mislead the
// agent into calling K8s tools, which will fail or operate on the wrong target.
//
// Returns nil when infra config is absent, the server is unknown, or no
// cross-type terminology is found. Execution is never blocked.
func checkContextConsistency(cfg *infra.Config, connectionString, operatorContext string) []string {
	if cfg == nil || connectionString == "" || operatorContext == "" {
		return nil
	}

	// Locate the server by map key, display name, or full connection string.
	var server *infra.DBServer
	for key, db := range cfg.DBServers {
		if key == connectionString || db.Name == connectionString || db.ConnectionString == connectionString {
			d := db
			server = &d
			break
		}
	}
	if server == nil {
		return nil
	}

	isK8s := server.K8sCluster != ""
	var runtime string
	if server.VMName != "" {
		if vm, ok := cfg.VMs[server.VMName]; ok {
			runtime = vm.Runtime
		}
	}

	ctx := strings.ToLower(operatorContext)
	var warnings []string

	if !isK8s {
		// Server is not Kubernetes-hosted — K8s terminology in context misdirects the agent.
		k8sKeywords := []string{"pod ", "pods ", "kubectl", "deployment", "statefulset", "namespace", "kubernetes", "k8s"}
		for _, kw := range k8sKeywords {
			if strings.Contains(ctx, kw) {
				label := serverHostingLabel(server, runtime)
				warnings = append(warnings, fmt.Sprintf(
					"context mentions %q but %q is %s — Kubernetes tools will not apply to this server",
					strings.TrimSpace(kw), server.Name, label))
				break
			}
		}
	}

	isContainer := runtime == "docker" || runtime == "podman"
	if !isContainer && !isK8s {
		// Server is not container-hosted — Docker/Podman terminology is misleading.
		containerKeywords := []string{"docker ", "docker exec", "docker run", "podman "}
		for _, kw := range containerKeywords {
			if strings.Contains(ctx, kw) {
				warnings = append(warnings, fmt.Sprintf(
					"context mentions %q but %q is not container-hosted (runtime=%q)",
					strings.TrimSpace(kw), server.Name, runtime))
				break
			}
		}
	}

	return warnings
}

// serverHostingLabel returns a short human-readable description of where db runs.
func serverHostingLabel(db *infra.DBServer, runtime string) string {
	if runtime == "docker" || runtime == "podman" {
		return runtime + "-hosted"
	}
	if db.VMName != "" {
		return "VM-hosted"
	}
	return "standalone"
}

// checkTargetScope returns connection strings from audit events for traceID that
// differ from intendedTarget. Used to detect when the agent queried a server
// other than the one specified in the playbook run request.
// Returns nil when auditURL is empty, no traceID, or no drift is found.
func checkTargetScope(auditURL, apiKey, traceID string, since time.Time, intendedTarget string) []string {
	if auditURL == "" || traceID == "" || intendedTarget == "" {
		return nil
	}
	events := audit.FetchToolExecutionEvents(auditURL, apiKey, traceID, since)
	seen := map[string]bool{}
	for _, ev := range events {
		if ev.Tool == nil {
			continue
		}
		cs, _ := ev.Tool.Parameters["connection_string"].(string)
		if cs == "" {
			continue
		}
		if !targetMatches(intendedTarget, cs) {
			seen[cs] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	drift := make([]string, 0, len(seen))
	for cs := range seen {
		drift = append(drift, cs)
	}
	sort.Strings(drift)
	return drift
}

// targetMatches returns true when actual equals intended, or when actual is a
// psql-style connection string (key=value pairs) that encodes intended as one
// of its field values — e.g. intended="test-pg", actual="host=test-pg dbname=postgres".
func targetMatches(intended, actual string) bool {
	if intended == actual {
		return true
	}
	for _, field := range strings.Fields(actual) {
		if kv := strings.SplitN(field, "=", 2); len(kv) == 2 && kv[1] == intended {
			return true
		}
	}
	return false
}
