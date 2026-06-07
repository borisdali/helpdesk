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
	"helpdesk/internal/decisions"
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

// terminalEscalations maps sentinel series IDs that signal a human gate is
// needed. They are not chainable playbooks — the gateway stops the chain and
// surfaces an operator-approval requirement to the caller instead of looking
// up a follow-on playbook. Also used to suppress spurious audit delegation
// events for these non-agent targets.
var terminalEscalations = map[string]string{
	"requires_operator_approval": "This action requires explicit operator approval before it can proceed.",
}

// approvalModeRank defines permissiveness order for approval modes (higher = less restrictive).
var approvalModeRank = map[string]int{
	"":        0,
	"manual":  0,
	"review":  1,
	"session": 2,
	"auto":    3,
	"force":   4,
}

// enforceApprovalOverride clamps *requestedMode back to playbookMode when the target
// database restricts approval overrides and the caller lacks a required role.
// Appends a warning to *warnings when clamping occurs; otherwise is a no-op.
func (g *Gateway) enforceApprovalOverride(
	principal identity.ResolvedPrincipal,
	requestedMode *string,
	playbookMode string,
	connStr string,
	warnings *[]string,
) {
	if g.infra == nil || approvalModeRank[*requestedMode] <= approvalModeRank[playbookMode] {
		return
	}
	db, _, ok := g.infra.FindDBByConnStr(connStr)
	if !ok || len(db.ApprovalOverrideRoles) == 0 {
		return
	}
	for _, role := range db.ApprovalOverrideRoles {
		if principal.HasRole(role) {
			return
		}
	}
	*warnings = append(*warnings, fmt.Sprintf(
		"approval_mode clamped to %q: override to %q requires one of roles %v (caller: %s)",
		playbookMode, *requestedMode, db.ApprovalOverrideRoles, principal.EffectiveID(),
	))
	*requestedMode = playbookMode
}

// approvalContext carries approval mode + session ID through the request context.
type approvalContext struct {
	mode      string // "auto" | "session" | "manual" | "force"
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

	// ApprovalMode controls when approval is required for write/destructive operations
	// and which playbooks are eligible for auto-chaining.
	//   "auto"    — no gate on tools; chains through session/auto-gated playbooks.
	//   "session" — gated by session token; chains through session/auto-gated playbooks.
	//   "manual"  — agent-mode runs are read-only (no write/destructive proxied); no chaining.
	//   "force"   — like "auto" for tools, but also chains through manual-gated playbooks.
	//              Use when deliberately authorising the full diagnosis-to-remediation path.
	ApprovalMode    string `json:"approval_mode,omitempty"`
	// ApprovalSession is the session ID for "session" mode. Required when ApprovalMode="session".
	ApprovalSession string `json:"approval_session,omitempty"`

	// GateEscalation, when true, intercepts any ESCALATE_TO signal at the end of
	// an agent-mode triage run: instead of auto-chaining or returning suggested_next,
	// the gateway pauses at the phase boundary and returns status="pending_gate".
	// The operator then reviews the findings and calls proceed-escalation to chain
	// to the remediation playbook with their chosen approval mode.
	// Takes precedence over approval_mode=auto/force.
	GateEscalation bool `json:"gate_escalation,omitempty"`

	// Purpose and PurposeNote are forwarded as X-Purpose / X-Purpose-Note headers
	// so that tool dispatch policy checks see the operator's declared intent.
	// Use "diagnostic", "remediation", "maintenance", etc.
	Purpose     string `json:"purpose,omitempty"`
	PurposeNote string `json:"purpose_note,omitempty"`
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
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
				return
			}
		}
	}

	// Bridge purpose fields into headers so proxyToAgentWithTool has one place
	// to read them (same pattern as handleQuery).
	if req.Purpose != "" && r.Header.Get("X-Purpose") == "" {
		r.Header.Set("X-Purpose", req.Purpose)
	}
	if req.PurposeNote != "" && r.Header.Get("X-Purpose-Note") == "" {
		r.Header.Set("X-Purpose-Note", req.PurposeNote)
	}

	// Fetch the playbook from auditd.
	pb, err := g.fetchPlaybook(r.Context(), id)
	if err != nil {
		slog.Error("handlePlaybookRun: failed to fetch playbook", "id", id, "err", err)
		writeError(w, http.StatusNotFound, fmt.Sprintf("playbook %q not found: %v", id, err))
		return
	}

	// Resolve effective approval mode: per-run request overrides playbook default.
	var warnings []string
	if req.ApprovalMode == "" {
		req.ApprovalMode = pb.ApprovalMode
	}
	principal := authz.PrincipalFromContext(r.Context())
	g.enforceApprovalOverride(principal, &req.ApprovalMode, pb.ApprovalMode, req.ConnectionString, &warnings)

	// Warn when an agent-mode playbook has no connection_string — the agent
	// will have no target and will likely ask the operator for one.
	if pb.ExecutionMode == "agent" && req.ConnectionString == "" {
		slog.Warn("handlePlaybookRun: agent-mode run has no connection_string", "playbook", pb.SeriesID)
		warnings = append(warnings, "no connection_string specified — agent will need to ask which database to investigate")
	}

	// Item 5: soft-warn when required evidence patterns are absent from the
	// operator-supplied context. Execution is not blocked.
	warnings = append(warnings, checkRequiresEvidence(pb.RequiresEvidence, req.Context)...)

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
	// For agent_approve runs, generate a run-level trace ID when the caller doesn't
	// supply one — tool execution events are emitted under this ID and must be
	// discoverable via GET /playbook-runs/{id}/events.
	operator := r.Header.Get("X-User")
	startTraceID := r.Header.Get("X-Trace-ID")
	if startTraceID == "" && pb.ExecutionMode == "agent_approve" {
		startTraceID = audit.NewTraceID()
	}
	runID := g.recordPlaybookRunStart(r.Context(), pb, req.ContextID, req.ConnectionString, startTraceID, req.PriorRunID, operator)

	if pb.ExecutionMode == "agent" {
		g.handlePlaybookRunAsAgent(w, r, pb, req, runID, warnings)
		return
	}

	if pb.ExecutionMode == "agent_approve" {
		g.handlePlaybookRunApprove(w, r, pb, req, runID, warnings)
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
		go g.recordPlaybookRunComplete(context.WithoutCancel(r.Context()), runID, "unknown", "", "", "", "", "", nil)
	}
}

// agentRunResult holds the parsed output of a single agent-mode playbook run.
type agentRunResult struct {
	capture          *responseCapture
	traceID          string
	runStart         time.Time
	outcome          string
	escalatedTo      string // set when agent emits ESCALATE_TO (true out-of-scope escalation)
	transitionTo     string // set when agent emits TRANSITION_TO (same-domain triage→remediation)
	findings         string
	diagReport       *audit.DiagnosticReport
	runID            string
	playbookSeriesID string
	agentName        string
}

// chainEntry is one element of the per-run chain returned in API responses.
type chainEntry struct {
	Step             int                     `json:"step"`
	PlaybookSeriesID string                  `json:"playbook_series_id"`
	AgentName        string                  `json:"agent_name"`
	RunID            string                  `json:"run_id,omitempty"`
	Findings         string                  `json:"findings,omitempty"`
	Text             string                  `json:"text,omitempty"`
	DiagnosticReport *audit.DiagnosticReport `json:"diagnostic_report,omitempty"`
}

// runAgentPlaybook executes one agent-mode playbook run and returns the parsed result.
// It does NOT write to any ResponseWriter — callers compose the final response.
// agentName overrides pb.AgentName when non-empty; falls back to agentNameDB.
func (g *Gateway) runAgentPlaybook(r *http.Request, pb *audit.Playbook, req PlaybookRunRequest, agentName string, runID string) agentRunResult {
	if agentName == "" {
		agentName = pb.AgentName
	}
	if agentName == "" {
		agentName = agentNameDB
	}

	serverTypeHint := buildServerTypeHint(g.infra, req.ConnectionString)
	var prompt string
	if g.crystalBall {
		prompt = assembleCrystalBallPrompt(req, serverTypeHint)
	} else {
		prompt = assembleTriagePrompt(pb, req, serverTypeHint)
	}

	// Propagate approval mode into context so proxyToAgentWithTool can enforce
	// it before proxying write/destructive calls within this run.
	if req.ApprovalMode != "" {
		ctx := context.WithValue(r.Context(), ctxKeyApprovalSession, approvalContext{
			mode:      req.ApprovalMode,
			sessionID: req.ApprovalSession,
		})
		r = r.WithContext(ctx)
	}

	runStart := time.Now()
	capture := newResponseCapture()
	g.proxyToAgent(capture, r, agentName, req.ContextID, prompt)

	traceID := capture.header.Get("X-Trace-ID")
	if traceID == "" {
		traceID = capture.header.Get("X-Trace-Id")
	}

	res := agentRunResult{
		capture:          capture,
		traceID:          traceID,
		runStart:         runStart,
		runID:            runID,
		playbookSeriesID: pb.SeriesID,
		agentName:        agentName,
	}

	if capture.code == http.StatusOK {
		var respBody map[string]any
		if err := json.Unmarshal(capture.body.Bytes(), &respBody); err == nil {
			if text, ok := respBody["text"].(string); ok {
				res.diagReport = parseDiagnosticReport(text)
				esc := parseAgentEscalation(text)
				res.findings = esc.Findings
				if esc.TransitionTo != "" {
					res.outcome = audit.OutcomeTransitioned
					res.transitionTo = esc.TransitionTo
					g.recordEscalationDecision(r.Context(), traceID,
						authz.PrincipalFromContext(r.Context()), pb, esc.TransitionTo, esc.Findings)
				} else if esc.EscalateTo != "" {
					res.outcome = audit.OutcomeEscalated
					res.escalatedTo = esc.EscalateTo
					// Don't record a delegation event for terminal escalation tokens
					// (e.g. requires_operator_approval) — they are human gates, not
					// agent handoffs, and would produce "unknown agent" audit alerts.
					if _, isTerminal := terminalEscalations[esc.EscalateTo]; !isTerminal {
						g.recordEscalationDecision(r.Context(), traceID,
							authz.PrincipalFromContext(r.Context()), pb, esc.EscalateTo, esc.Findings)
					}
				} else if esc.Findings != "" {
					res.outcome = "resolved"
				}
				// Rewrite captured body with signal lines stripped.
				respBody["text"] = esc.CleanText
				if b, err := json.Marshal(respBody); err == nil {
					capture.body.Reset()
					capture.body.Write(b) //nolint:errcheck
				}
			}
		}
	}
	if res.outcome == "" {
		res.outcome = "unknown"
	}
	return res
}

// handlePlaybookRunAsAgent routes an agent-mode playbook run, optionally chaining
// to a follow-on playbook when ESCALATE_TO fires and approval_mode permits it.
func (g *Gateway) handlePlaybookRunAsAgent(w http.ResponseWriter, r *http.Request, pb *audit.Playbook, req PlaybookRunRequest, runID string, warnings []string) {
	primary := g.runAgentPlaybook(r, pb, req, "", runID)

	extra := map[string]any{}
	if runID != "" {
		extra["run_id"] = runID
	}
	if primary.findings != "" {
		extra["findings"] = primary.findings
	}
	if primary.diagReport != nil {
		extra["diagnostic_report"] = primary.diagReport
	}
	if len(warnings) > 0 {
		extra["warnings"] = warnings
	}

	// Post-run target-scope check.
	if req.ConnectionString != "" {
		if drift := checkTargetScope(g.infra, g.auditURL, g.auditAPIKey, primary.traceID, primary.runStart, req.ConnectionString); len(drift) > 0 {
			extra["target_drift"] = drift
			slog.Warn("playbook run: target scope drift detected",
				"trace_id", primary.traceID,
				"intended", req.ConnectionString,
				"actual", drift)
		}
	}

	// Oracle mode: skip chaining and structured output; inject warning then return.
	if g.crystalBall {
		extra["crystal_ball"] = true
		extra["crystal_ball_warning"] = "Crystal-ball mode is active. Playbook guidance, hypothesis formatting, and escalation chaining are bypassed. " +
			"This response reflects the LLM's unscaffolded judgment over available tools. " +
			"Not recommended for production use."
		injectFields(w, primary.capture, extra)
		if runID != "" {
			go g.recordPlaybookRunComplete(context.WithoutCancel(r.Context()),
				runID, primary.outcome, "", "", primary.findings, "", primary.traceID, nil)
		}
		return
	}

	// Escalation/transition handling: auto-chain or return suggested_next.
	finalOutcome := primary.outcome
	finalEscalatedTo := primary.escalatedTo
	finalTransitionedTo := primary.transitionTo
	finalFindings := primary.findings
	finalReport := primary.diagReport

	// chain always starts with the primary run.
	chain := []chainEntry{{
		Step:             1,
		PlaybookSeriesID: primary.playbookSeriesID,
		AgentName:        primary.agentName,
		RunID:            primary.runID,
		Findings:         primary.findings,
		Text:             capturedText(primary.capture),
		DiagnosticReport: primary.diagReport,
	}}

	const maxChainDepth = 5
	prev := primary
	for len(chain) < maxChainDepth && (prev.escalatedTo != "" || prev.transitionTo != "") {
		// nextSeries is the target for either signal; isTransition distinguishes them.
		nextSeries := prev.escalatedTo
		isTransition := false
		if nextSeries == "" {
			nextSeries = prev.transitionTo
			isTransition = true
		}

		// Terminal escalation tokens only apply to true ESCALATE_TO signals.
		if !isTransition {
			if msg, isTerminal := terminalEscalations[nextSeries]; isTerminal {
				extra["suggested_next"] = map[string]any{
					"escalation_token":           nextSeries,
					"message":                    msg,
					"requires_operator_approval": true,
				}
				slog.Info("playbook: operator approval required — stopping chain",
					"series_id", nextSeries, "trace_id", prev.traceID)
				break
			}
		}

		// If triage found nothing actionable (recommended=monitor or
		// no_changes_needed), skip both the gate and auto-chain: there is
		// nothing for the remediation playbook to act on.
		if findingsRecommendMonitor(prev.findings) {
			finalOutcome = "resolved"
			finalEscalatedTo = ""
			finalTransitionedTo = ""
			break
		}

		// Forced gate: low-confidence triage must not auto-chain into destructive
		// remediation regardless of the caller's gate_escalation flag.
		if !req.GateEscalation && lowConfidenceForceGate(prev.diagReport) {
			req.GateEscalation = true
			extra["gate_reason"] = "low_confidence"
		}

		// Informed gate: operator reviews findings at the phase boundary before
		// the next playbook is invoked. Takes precedence over auto-chaining.
		if req.GateEscalation {
			warn, suggestedMode := g.confidenceWarning(prev.diagReport)
			extra["status"] = "pending_gate"
			extra["escalation_findings"] = prev.findings
			gateType := "escalation"
			if isTransition {
				gateType = "transition"
				extra["transition_target"] = nextSeries
			} else {
				extra["escalation_target"] = nextSeries
			}
			extra["gate_type"] = gateType
			if warn != "" {
				extra["confidence_warning"] = warn
				extra["suggested_approval_mode"] = suggestedMode
			}
			if nextPB, err := g.fetchPlaybookBySeriesID(r.Context(), nextSeries); err == nil {
				extra["remediation_preview"] = map[string]any{
					"series_id":     nextPB.SeriesID,
					"name":          nextPB.Name,
					"description":   nextPB.Description,
					"approval_mode": nextPB.ApprovalMode,
				}
			}
			if prev.diagReport != nil {
				extra["diagnostic_report"] = prev.diagReport
			}
			finalOutcome = audit.OutcomeGatePending
			if isTransition {
				finalTransitionedTo = nextSeries
				finalEscalatedTo = ""
			} else {
				finalEscalatedTo = nextSeries
				finalTransitionedTo = ""
			}
			finalFindings = prev.findings
			finalReport = prev.diagReport
			slog.Info("playbook: gate pending — awaiting operator acknowledgment",
				"run_id", prev.runID, "gate_type", gateType, "next_series", nextSeries,
				"confidence_warning", warn)
			if g.decisionNotifier != nil && prev.runID != "" {
				gateExtra := map[string]any{
					"gate_type": gateType,
					"findings":  prev.findings,
				}
				if isTransition {
					gateExtra["transition_target"] = nextSeries
				} else {
					gateExtra["escalation_target"] = nextSeries
				}
				if warn != "" {
					gateExtra["confidence_warning"] = warn
				}
				if rp, ok := extra["remediation_preview"]; ok {
					gateExtra["remediation_preview"] = rp
				}
				if dr, ok := extra["diagnostic_report"]; ok {
					gateExtra["diagnostic_report"] = dr
				}
				summary := "Triage complete — TRANSITION_TO " + nextSeries
				if !isTransition {
					summary = "Triage complete — ESCALATE_TO " + nextSeries
				}
				g.decisionNotifier.NotifyPending(r.Context(), decisions.Decision{
					ID:          "gate:" + prev.runID,
					Type:        decisions.DecisionTypeGate,
					Status:      "pending",
					Summary:     summary,
					RequestedBy: r.Header.Get("X-User"),
					RequestedAt: time.Now(),
					ResolveURL:  strings.TrimSuffix(g.baseURL, "/") + "/api/v1/decisions/gate:" + prev.runID + "/resolve",
					Extra:       gateExtra,
				})
			}
			break
		}

		nextPB, err := g.fetchPlaybookBySeriesID(r.Context(), nextSeries)
		if err != nil {
			slog.Warn("playbook: cannot fetch next playbook",
				"series_id", nextSeries, "err", err)
			break
		}
		if !g.canAutoChain(r.Context(), req.ApprovalMode, req.ApprovalSession, nextPB) {
			extra["suggested_next"] = buildSuggestedNext(nextSeries, req, prev.runID, prev.findings)
			break
		}
		chained := g.chainEscalation(r, pb, req, prev, nextPB)
		if chained == nil {
			break
		}
		chain = append(chain, chainEntry{
			Step:             len(chain) + 1,
			PlaybookSeriesID: chained.playbookSeriesID,
			AgentName:        chained.agentName,
			RunID:            chained.runID,
			Findings:         chained.findings,
			Text:             capturedText(chained.capture),
			DiagnosticReport: chained.diagReport,
		})
		finalReport = mergeDiagnosticReports(finalReport, chained.diagReport)
		finalOutcome = "escalated+resolved"
		if chained.findings != "" {
			finalFindings = chained.findings
			finalEscalatedTo = chained.escalatedTo
			finalTransitionedTo = chained.transitionTo
		}
		extra["diagnostic_report"] = finalReport
		extra["chained_run_id"] = chained.runID
		if chained.findings != "" {
			extra["chained_findings"] = chained.findings
		}
		appendChainedText(primary.capture, chained.capture)
		prev = *chained
	}

	extra["chain"] = chain

	injectFields(w, primary.capture, extra)

	if runID != "" {
		go g.recordPlaybookRunComplete(context.WithoutCancel(r.Context()),
			runID, finalOutcome, finalEscalatedTo, finalTransitionedTo, finalFindings, capturedText(primary.capture), primary.traceID, finalReport)
	}
}

// canAutoChain returns true when the effective approval mode permits automatic
// cross-agent escalation chaining to targetPB.
//
// Two conditions must both be satisfied:
//  1. The requester's mode authorises chaining (auto, session with escalation, or force).
//  2. The target playbook's ApprovalMode permits being chained to ("session" or "auto").
//
// Exception: approval_mode=force bypasses the playbook-level gate entirely — the operator
// is explicitly accepting responsibility for the full diagnosis-to-remediation chain,
// including playbooks that would otherwise require manual invocation.
func (g *Gateway) canAutoChain(ctx context.Context, mode, sessionID string, targetPB *audit.Playbook) bool {
	// "force" bypasses the playbook-level gate — operator is explicitly authorising the full chain.
	if mode == "force" {
		return true
	}
	// Playbook-level gate: "manual" (or unset) always requires explicit invocation.
	targetMode := targetPB.ApprovalMode
	if targetMode == "" || targetMode == "manual" {
		return false
	}
	// Target allows chaining at "session" or "auto" level; check requester's mode.
	switch mode {
	case "auto":
		return true
	case "session":
		sess, err := g.fetchApprovalSession(ctx, sessionID)
		return err == nil && sess.IsValid(audit.ActionEscalation)
	default: // "manual" or "" — require explicit operator trigger
		return false
	}
}

// chainEscalation runs nextPB as a chained agent session, using the primary
// run's findings as context. nextPB must already be fetched by the caller.
func (g *Gateway) chainEscalation(r *http.Request, primaryPB *audit.Playbook, req PlaybookRunRequest, primary agentRunResult, nextPB *audit.Playbook) *agentRunResult {
	if g.auditURL == "" {
		return nil
	}

	chainReq := PlaybookRunRequest{
		ConnectionString: req.ConnectionString,
		Context:          req.Context,
		PriorRunID:       primary.runID,
		ApprovalMode:     req.ApprovalMode,
		ApprovalSession:  req.ApprovalSession,
	}
	// Fetch prior findings for continuity threading.
	if chainReq.PriorRunID != "" {
		if prior, err := g.fetchPlaybookRun(r.Context(), chainReq.PriorRunID); err == nil {
			chainReq.PriorFindings = prior.FindingsSummary
		}
	}

	chainRunID := g.recordPlaybookRunStart(r.Context(), nextPB, req.ContextID, req.ConnectionString, r.Header.Get("X-Trace-ID"), req.PriorRunID, r.Header.Get("X-User"))
	chainRes := g.runAgentPlaybook(r, nextPB, chainReq, nextPB.AgentName, chainRunID)

	if chainRunID != "" {
		go g.recordPlaybookRunComplete(context.WithoutCancel(r.Context()),
			chainRunID, chainRes.outcome, chainRes.escalatedTo, chainRes.transitionTo, chainRes.findings, capturedText(chainRes.capture), chainRes.traceID, chainRes.diagReport)
	}

	slog.Info("playbook: auto-chained escalation",
		"from", primaryPB.SeriesID, "to", nextPB.SeriesID,
		"chain_run_id", chainRunID, "outcome", chainRes.outcome)

	return &chainRes
}

// fetchPlaybookBySeriesID returns the active version of a playbook by series ID.
func (g *Gateway) fetchPlaybookBySeriesID(ctx context.Context, seriesID string) (*audit.Playbook, error) {
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbooks?series_id=" + seriesID + "&active_only=true&include_system=true"
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
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auditd returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var result struct {
		Playbooks []*audit.Playbook `json:"playbooks"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if len(result.Playbooks) == 0 {
		return nil, fmt.Errorf("not found")
	}
	return result.Playbooks[0], nil
}

// buildSuggestedNext constructs the suggested_next response field that operators
// can use as a ready-to-fire request body for the escalated playbook.
func buildSuggestedNext(seriesID string, req PlaybookRunRequest, priorRunID, findings string) map[string]any {
	return map[string]any{
		"playbook_series_id": seriesID,
		"reason":             findings,
		"request": map[string]any{
			"connection_string": req.ConnectionString,
			"prior_run_id":      priorRunID,
			"context":           findings,
			"approval_mode":     req.ApprovalMode,
		},
	}
}

// confidenceWarning returns a human-readable warning and a suggested approval
// mode when the diagnostic report shows low confidence or competing hypotheses.
// Returns ("", "") when confidence is high and no competing hypotheses exist.
func (g *Gateway) confidenceWarning(report *audit.DiagnosticReport) (warning, suggestedMode string) {
	if report == nil || len(report.Hypotheses) == 0 {
		return "", ""
	}
	primary := report.Hypotheses[0]
	lowAbsolute := primary.Confidence < 0.70
	competing := len(report.Hypotheses) > 1 &&
		report.Hypotheses[1].Confidence > primary.Confidence*0.70
	if !lowAbsolute && !competing {
		return "", ""
	}
	msg := fmt.Sprintf("Primary hypothesis confidence %.0f%%", primary.Confidence*100)
	if competing {
		msg += fmt.Sprintf(" — competing hypothesis at %.0f%%", report.Hypotheses[1].Confidence*100)
	}
	msg += ". Uncertain diagnosis: consider step-by-step approval."
	return msg, "manual"
}

// lowConfidenceForceGate returns true when the diagnostic report shows that
// the primary hypothesis has less than 50% confidence — a coin-flip diagnosis
// that must not auto-chain into destructive remediation. Returns false when
// report is nil so pre-B1 playbooks (no HYPOTHESIS_N: lines) are unaffected.
func lowConfidenceForceGate(report *audit.DiagnosticReport) bool {
	if report == nil {
		return false
	}
	for _, h := range report.Hypotheses {
		if h.IsPrimary {
			return h.Confidence < 0.50
		}
	}
	return true // primary hypothesis not marked — treat as uncertain
}

// ProceedEscalationRequest is the request body for POST
// /api/v1/fleet/playbook-runs/{runID}/proceed-escalation.
type ProceedEscalationRequest struct {
	Resolution       string `json:"resolution"`               // "approved" | "denied"
	ResolvedBy       string `json:"resolved_by,omitempty"`
	ApprovalMode     string `json:"approval_mode,omitempty"`  // "manual"|"review"|"auto"|"session"|"force"
	ApprovalSession  string `json:"approval_session,omitempty"`
	ConnectionString string `json:"connection_string,omitempty"` // forwarded to the remediation playbook
	Reason           string `json:"reason,omitempty"`            // optional operator rationale
}

// handleProceedEscalation handles POST /api/v1/fleet/playbook-runs/{runID}/proceed-escalation.
// It acknowledges an informed gate: the operator has reviewed the triage findings and either
// approves the remediation chain (with a chosen approval mode) or denies it.
func (g *Gateway) handleProceedEscalation(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "missing runID")
		return
	}

	var req ProceedEscalationRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
	}
	if req.Resolution == "" {
		req.Resolution = "approved"
	}
	resolvedBy := req.ResolvedBy
	if resolvedBy == "" {
		resolvedBy = r.Header.Get("X-User")
	}
	if resolvedBy == "" {
		resolvedBy = "operator"
	}

	// Fetch the triage run and validate it is in gate_pending state.
	run, err := g.fetchPlaybookRun(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found: "+err.Error())
		return
	}
	if run.Outcome != audit.OutcomeGatePending {
		writeError(w, http.StatusConflict, fmt.Sprintf("run %q is not in gate_pending state (outcome=%q)", runID, run.Outcome))
		return
	}

	// Emit gate_acknowledged audit event.
	g.recordGateAcknowledged(r.Context(), run, resolvedBy, req.Resolution, req.ApprovalMode, "", req.Reason)

	// Denied: abandon the triage run and return.
	if req.Resolution == "denied" {
		g.recordPlaybookRunComplete(r.Context(), runID, audit.OutcomeAbandoned, "", "", "gate denied by operator", "", "", run.DiagnosticReport)
		if g.decisionNotifier != nil {
			g.decisionNotifier.NotifyResolved(r.Context(), decisions.Decision{
				ID:         "gate:" + runID,
				Type:       decisions.DecisionTypeGate,
				Status:     "denied",
				Summary:    "Gate denied — remediation will not proceed",
				ResolveURL: strings.TrimSuffix(g.baseURL, "/") + "/api/v1/decisions/gate:" + runID + "/resolve",
				Extra:      map[string]any{"resolved_by": resolvedBy},
			})
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "denied", "run_id": runID})
		return
	}

	// Approved: resolve the triage run and chain to the next playbook.
	// Use TransitionedTo for same-domain transitions, EscalatedTo for true escalations.
	nextSeriesID := run.EscalatedTo
	approvedOutcome := audit.OutcomeEscalated
	if run.TransitionedTo != "" {
		nextSeriesID = run.TransitionedTo
		approvedOutcome = audit.OutcomeTransitioned
	}
	g.recordPlaybookRunComplete(r.Context(), runID, approvedOutcome, run.EscalatedTo, run.TransitionedTo, run.FindingsSummary, "", "", run.DiagnosticReport)
	if g.decisionNotifier != nil {
		g.decisionNotifier.NotifyResolved(r.Context(), decisions.Decision{
			ID:      "gate:" + runID,
			Type:    decisions.DecisionTypeGate,
			Status:  "approved",
			Summary: "Gate approved — chaining to " + nextSeriesID,
			Extra:   map[string]any{"resolved_by": resolvedBy, "approval_mode": req.ApprovalMode},
		})
	}

	nextPB, err := g.fetchPlaybookBySeriesID(r.Context(), nextSeriesID)
	if err != nil {
		writeError(w, http.StatusNotFound, "remediation playbook not found: "+err.Error())
		return
	}

	// Build the remediation request. Prefer connection_string from the resolve
	// request body; fall back to the one stored on the triage run so operators
	// don't need to re-supply it when approving a gate.
	connStr := req.ConnectionString
	if connStr == "" {
		connStr = run.ConnectionString
	}

	remReq := PlaybookRunRequest{
		ConnectionString: connStr,
		PriorRunID:       runID,
		ApprovalMode:     req.ApprovalMode,
		ApprovalSession:  req.ApprovalSession,
	}
	if remReq.ApprovalMode == "" {
		remReq.ApprovalMode = nextPB.ApprovalMode
	}
	var warnings []string
	principal := authz.PrincipalFromContext(r.Context())
	g.enforceApprovalOverride(principal, &remReq.ApprovalMode, nextPB.ApprovalMode, remReq.ConnectionString, &warnings)

	// Thread prior findings.
	if prior, err := g.fetchPlaybookRun(r.Context(), runID); err == nil {
		remReq.PriorFindings = prior.FindingsSummary
	}

	remStartTraceID := r.Header.Get("X-Trace-ID")
	if remStartTraceID == "" && nextPB.ExecutionMode == "agent_approve" {
		remStartTraceID = audit.NewTraceID()
	}
	remRunID := g.recordPlaybookRunStart(r.Context(), nextPB, run.ContextID, connStr, remStartTraceID, runID, resolvedBy)

	slog.Info("playbook: gate approved — chaining to remediation",
		"triage_run_id", runID, "remediation_series", nextSeriesID,
		"approval_mode", remReq.ApprovalMode, "resolved_by", resolvedBy)

	if nextPB.ExecutionMode == "agent_approve" {
		g.handlePlaybookRunApprove(w, r, nextPB, remReq, remRunID, warnings)
		return
	}
	g.handlePlaybookRunAsAgent(w, r, nextPB, remReq, remRunID, warnings)
}

// recordGateAcknowledged emits a gate_acknowledged audit event.
// The event's TraceID is set to run.RunID so it can be retrieved via
// GET /v1/events?trace_id={runID}&event_type=gate_acknowledged.
func (g *Gateway) recordGateAcknowledged(ctx context.Context, run *audit.PlaybookRun, resolvedBy, resolution, approvalMode, confidenceWarning, reason string) {
	if g.auditor == nil {
		return
	}
	reasoningChain := []string{
		"operator " + resolvedBy + " acknowledged informed gate for run " + run.RunID,
		"resolution: " + resolution,
		"escalation_target: " + run.EscalatedTo,
	}
	if approvalMode != "" {
		reasoningChain = append(reasoningChain, "chosen approval_mode: "+approvalMode)
	}
	if confidenceWarning != "" {
		reasoningChain = append(reasoningChain, "confidence_warning: "+confidenceWarning)
	}
	if reason != "" {
		reasoningChain = append(reasoningChain, "operator_reason: "+reason)
	}
	event := &audit.Event{
		EventID:   "ga_" + uuid.New().String()[:8],
		Timestamp: time.Now().UTC(),
		EventType: audit.EventTypeGateAcknowledged,
		TraceID:   run.RunID, // correlate back to the playbook run
		Input: audit.Input{
			UserQuery: "gate acknowledged for triage run " + run.RunID,
		},
		Decision: &audit.Decision{
			Agent:           run.EscalatedTo,
			RequestCategory: audit.CategoryIncident,
			Confidence:      1.0,
			UserIntent:      resolution + " gate for escalation to " + run.EscalatedTo,
			ReasoningChain:  reasoningChain,
		},
		Output:  &audit.Output{Response: reason},
		Outcome: &audit.Outcome{Status: "success"},
	}
	if err := g.auditor.RecordEvent(ctx, event); err != nil {
		slog.Warn("playbook: failed to record gate_acknowledged event", "run_id", run.RunID, "err", err)
	}
}

// mergeDiagnosticReports combines hypotheses from two runs into one report.
// Secondary (chained) hypotheses take precedence — they have more evidence.
// The highest-confidence secondary hypothesis becomes IsPrimary; the primary
// run's former primary hypothesis is demoted. All hypotheses are re-ranked by
// confidence descending.
func mergeDiagnosticReports(primary, secondary *audit.DiagnosticReport) *audit.DiagnosticReport {
	if primary == nil {
		return secondary
	}
	if secondary == nil {
		return primary
	}

	merged := &audit.DiagnosticReport{}

	// Collect all hypotheses; mark secondary ones as non-primary first.
	var all []audit.DiagnosticHypothesis
	for _, h := range primary.Hypotheses {
		h.IsPrimary = false
		all = append(all, h)
	}
	for _, h := range secondary.Hypotheses {
		all = append(all, h)
	}

	// Sort by confidence descending.
	sort.Slice(all, func(i, j int) bool {
		return all[i].Confidence > all[j].Confidence
	})

	// Re-rank and mark top as primary.
	for i := range all {
		all[i].Rank = i + 1
		all[i].IsPrimary = i == 0
	}
	merged.Hypotheses = all

	// Secondary root cause takes precedence if present.
	if secondary.RootCause != "" {
		merged.RootCause = secondary.RootCause
	} else {
		merged.RootCause = primary.RootCause
	}
	if secondary.ActionTaken != "" {
		merged.ActionTaken = secondary.ActionTaken
	} else {
		merged.ActionTaken = primary.ActionTaken
	}
	return merged
}

// appendChainedText appends a separator and the chained run's text to the
// primary capture body so the operator sees both agents' findings in one response.
// capturedText extracts the "text" field from a captured agent response body.
func capturedText(c *responseCapture) string {
	if c == nil || c.code != http.StatusOK {
		return ""
	}
	var body map[string]any
	if err := json.Unmarshal(c.body.Bytes(), &body); err != nil {
		return ""
	}
	t, _ := body["text"].(string)
	return t
}

func appendChainedText(primary, chained *responseCapture) {
	if chained == nil || chained.code != http.StatusOK {
		return
	}
	var primaryBody, chainedBody map[string]any
	if err := json.Unmarshal(primary.body.Bytes(), &primaryBody); err != nil {
		return
	}
	if err := json.Unmarshal(chained.body.Bytes(), &chainedBody); err != nil {
		return
	}
	primaryText, _ := primaryBody["text"].(string)
	chainedText, _ := chainedBody["text"].(string)
	if chainedText == "" {
		return
	}
	primaryBody["text"] = primaryText + "\n\n---\n\n" + chainedText
	if b, err := json.Marshal(primaryBody); err == nil {
		primary.body.Reset()
		primary.body.Write(b) //nolint:errcheck
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
// serverTypeHint, if non-empty, is appended to the mandatory scope constraint so the agent
// knows the server's hosting type and does not apply K8s tooling to non-K8s servers.
func assembleTriagePrompt(pb *audit.Playbook, req PlaybookRunRequest, serverTypeHint string) string {
	var b strings.Builder

	// Open with an unambiguous action command when a target is specified.
	// A direct tool-invocation instruction as the very first line prevents the model
	// from falling back to its default clarification behavior before reading context.
	if req.ConnectionString != "" {
		fmt.Fprintf(&b, "Call check_connection with connection_string=%q and begin diagnosing why it is unavailable. Do not ask which database — the target is %q.\n", req.ConnectionString, req.ConnectionString)
		if serverTypeHint != "" {
			fmt.Fprintf(&b, "%s\n", serverTypeHint)
		}
		b.WriteString("\n")
	} else {
		b.WriteString("Read the 'Additional context' section at the bottom of this prompt first.\n")
		b.WriteString("It identifies the specific namespace, pod, or service to investigate.\n")
		b.WriteString("Use exactly what the context specifies — do not substitute or expand to other systems listed in your infrastructure configuration.\n")
		b.WriteString("Then follow the playbook guidance below.\n\n")
	}

	// Response protocol — repeated at closing for Gemini's benefit.
	b.WriteString("## Response Protocol\n")
	b.WriteString("Do NOT write a CONCLUSION section or end with '---'. Close your response with this exact block (plain text, no markdown, no bold, no backticks):\n\n")
	b.WriteString("HYPOTHESIS_1: <primary hypothesis> | CONFIDENCE: <0.0–1.0> | EVIDENCE: \"<verbatim quote from tool output>\"\n")
	b.WriteString("HYPOTHESIS_2: <alternative hypothesis> | CONFIDENCE: <0.0–1.0> | REJECTED: <one-sentence reason>\n")
	b.WriteString("ROOT_CAUSE: HYPOTHESIS_1\n")
	b.WriteString("FINDINGS: <one-sentence diagnosis and recommended action>\n")
	b.WriteString("TRANSITION_TO: <series_id>   — use this when the Expert Guidance 'Final step' specifies TRANSITION_TO (same-domain handoff to the expected remediation playbook)\n")
	b.WriteString("ESCALATE_TO: <series_id or \"none\">   — use this for true out-of-scope escalations to a different domain; use \"none\" if no escalation is needed\n")
	b.WriteString("Emit exactly one of TRANSITION_TO or ESCALATE_TO (not both). Follow the 'Final step' in Expert Guidance to determine which signal and which series_id.\n\n")
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

	if req.PriorFindings != "" {
		fmt.Fprintf(&b, "## Prior Investigation Findings\nA previous investigation reached the following conclusion:\n%s\n\nContinue from this context and investigate further.\n\n", req.PriorFindings)
	}

	b.WriteString("## Constraints\n")
	b.WriteString("- Use only read-only diagnostic tools. Do not execute any write or destructive operations.\n")
	b.WriteString("- Collect evidence, form a hypothesis, and if the evidence contradicts it, back out and pursue a different hypothesis.\n")
	b.WriteString("- When you reach a clear diagnosis, present your findings and recommended remediation steps.\n")
	b.WriteString("- Do NOT execute remediation — describe it for operator review and approval.\n")
	if req.ConnectionString != "" {
		fmt.Fprintf(&b, "- Use ONLY `connection_string` = `%s`. Do not query any other server, even if your context lists others.\n", req.ConnectionString)
	}
	b.WriteString("\n")

	if req.Context != "" {
		fmt.Fprintf(&b, "## Additional context\n%s\n\n", req.Context)
	}

	slog.Debug("triage prompt assembled", "playbook", pb.SeriesID, "prompt_len", b.Len(), "prompt", b.String())

	// Closing reminder — Gemini attends more reliably when instructions are
	// repeated at the end. Explicitly forbid alternatives (**CONCLUSION:** and trailing ---).
	if req.ConnectionString != "" {
		fmt.Fprintf(&b, "REMINDER: your target is connection_string=%q only. Do not check other databases.\n", req.ConnectionString)
	}
	b.WriteString("IMPORTANT: Do not write **CONCLUSION:** or end with ---. Close with exactly:\n")
	b.WriteString("HYPOTHESIS_1: ... | CONFIDENCE: ... | EVIDENCE: \"...\"\n")
	b.WriteString("HYPOTHESIS_2: ... | CONFIDENCE: ... | REJECTED: ...\n")
	b.WriteString("ROOT_CAUSE: HYPOTHESIS_1\n")
	b.WriteString("FINDINGS: <one-sentence diagnosis>\n")
	b.WriteString("TRANSITION_TO: <series_id>   (if Expert Guidance 'Final step' says TRANSITION_TO)\n")
	b.WriteString("ESCALATE_TO: <series_id or \"none\">   (for true cross-domain escalations; \"none\" if neither applies)\n")
	b.WriteString("Use exactly the signal and series_id from the Expert Guidance 'Final step'. Do not substitute one signal for the other.\n")
	b.WriteString("EXCEPTION: if you were unable to complete the investigation (policy denial, connection failure, or insufficient evidence to form a hypothesis), emit ESCALATE_TO: none regardless of what the Expert Guidance specifies.\n")

	return b.String()
}

// assembleCrystalBallPrompt builds a minimal prompt for crystal-ball mode: no playbook
// guidance, no hypothesis format, no escalation paths. The LLM receives only
// the target, operator context, and infrastructure type hint — then decides
// freely which tools to call and what to conclude.
//
// This intentionally mirrors what a capable LLM would do if given raw tool
// access without any expert scaffolding. Use it to benchmark unguided LLM
// diagnosis against structured playbook runs.
func assembleCrystalBallPrompt(req PlaybookRunRequest, serverTypeHint string) string {
	var b strings.Builder

	b.WriteString("You are a database operations assistant with access to diagnostic tools.\n\n")

	if req.ConnectionString != "" {
		if req.Context != "" {
			fmt.Fprintf(&b, "The operator is reporting an issue with %q. Investigate using whatever tools you judge appropriate and explain what you find.\n", req.ConnectionString)
		} else {
			fmt.Fprintf(&b, "The operator is reporting that %q is unavailable. Investigate using whatever tools you judge appropriate and explain what you find.\n", req.ConnectionString)
		}
		if serverTypeHint != "" {
			fmt.Fprintf(&b, "%s\n", serverTypeHint)
		}
	} else {
		b.WriteString("The operator needs help diagnosing a database issue. Investigate and explain what you find.\n")
	}

	if req.Context != "" {
		fmt.Fprintf(&b, "\nAdditional context from the operator:\n%s\n", req.Context)
	}

	b.WriteString("\nUse your tools to investigate. When you have a diagnosis, explain your findings and what you recommend.\n")

	return b.String()
}

// fetchPlaybook retrieves a single playbook record from auditd.
// id may be either a playbook UUID or a series_id (e.g. "pbs_db_restart_triage").
// When the direct GET returns 404, it falls back to a list?series_id= query
// and returns the active version.
func (g *Gateway) fetchPlaybook(ctx context.Context, id string) (*audit.Playbook, error) {
	base := strings.TrimSuffix(g.auditURL, "/")
	ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	doGet := func(url string) (*audit.Playbook, int, error) {
		req, err := http.NewRequestWithContext(ctx2, http.MethodGet, url, nil)
		if err != nil {
			return nil, 0, err
		}
		if g.auditAPIKey != "" {
			req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, resp.StatusCode, nil
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, resp.StatusCode, err
		}
		var pb audit.Playbook
		if err := json.Unmarshal(body, &pb); err != nil {
			return nil, resp.StatusCode, err
		}
		return &pb, resp.StatusCode, nil
	}

	// Try direct lookup by playbook_id first.
	pb, status, err := doGet(base + "/v1/fleet/playbooks/" + id)
	if err != nil {
		return nil, err
	}
	if status == http.StatusOK {
		return pb, nil
	}

	// Fall back to series_id list query (handles "pbs_*" series IDs).
	listURL := base + "/v1/fleet/playbooks?series_id=" + id + "&active_only=true"
	req, err := http.NewRequestWithContext(ctx2, http.MethodGet, listURL, nil)
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
		return nil, fmt.Errorf("not found")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var wrapper struct {
		Playbooks []*audit.Playbook `json:"playbooks"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return nil, err
	}
	if len(wrapper.Playbooks) == 0 {
		return nil, fmt.Errorf("not found")
	}
	return wrapper.Playbooks[0], nil
}

// fetchGateAcknowledgedReason returns the operator reason stored in the
// gate_acknowledged audit event for runID, or "" if none was recorded.
func (g *Gateway) fetchGateAcknowledgedReason(ctx context.Context, runID string) string {
	event := g.fetchGateAcknowledgedEvent(ctx, runID)
	if event == nil || event.Output == nil {
		return ""
	}
	return event.Output.Response
}

// handlePlaybookRunEvents handles GET /api/v1/fleet/playbook-runs/{runID}/events.
// Returns audit events (agent_reasoning, tool_execution, policy_decision) for the run
// by looking up the run's trace_id and querying the audit event log.
func (g *Gateway) handlePlaybookRunEvents(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "runID is required")
		return
	}
	run, err := g.fetchPlaybookRun(r.Context(), runID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "run not found")
			return
		}
		slog.Error("handlePlaybookRunEvents: failed to fetch run", "run_id", runID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to fetch run")
		return
	}
	if run.TraceID == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]any{}) //nolint:errcheck
		return
	}
	types := "agent_reasoning,tool_execution,policy_decision"
	if t := r.URL.Query().Get("types"); t != "" {
		types = t
	}
	limit := "500"
	if l := r.URL.Query().Get("limit"); l != "" {
		limit = l
	}
	auditPath := fmt.Sprintf("/v1/events?trace_id=%s&types=%s&limit=%s", run.TraceID, types, limit)
	g.proxyToAuditd(w, r, auditPath)
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
func (g *Gateway) recordPlaybookRunStart(ctx context.Context, pb *audit.Playbook, contextID, connStr, traceID, priorRunID, operator string) string {
	if g.auditURL == "" {
		return ""
	}
	run := audit.PlaybookRun{
		PlaybookID:       pb.PlaybookID,
		SeriesID:         pb.SeriesID,
		ExecutionMode:    pb.ExecutionMode,
		ContextID:        contextID,
		ConnectionString: connStr,
		TraceID:          traceID,
		PriorRunID:       priorRunID,
		Operator:         operator,
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
// escalatedTo is set for ESCALATE_TO signals; transitionedTo for TRANSITION_TO.
// agentTranscript is the full agent response text (chain of thought narrative); pass "" when not available.
// traceID is the agent's trace ID (from response X-Trace-ID header); used to link audit events to the run.
// Best-effort: failures are logged but not returned.
func (g *Gateway) recordPlaybookRunComplete(ctx context.Context, runID, outcome, escalatedTo, transitionedTo, findingsSummary, agentTranscript, traceID string, report *audit.DiagnosticReport) {
	if g.auditURL == "" || runID == "" {
		return
	}
	payload := map[string]any{
		"outcome":          outcome,
		"escalated_to":     escalatedTo,
		"transitioned_to":  transitionedTo,
		"findings_summary": findingsSummary,
	}
	if agentTranscript != "" {
		payload["agent_transcript"] = agentTranscript
	}
	if traceID != "" {
		payload["trace_id"] = traceID
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
	EscalateTo   string // series_id for out-of-scope escalations (ESCALATE_TO signal)
	TransitionTo string // series_id for same-domain triage→remediation transitions (TRANSITION_TO signal)
	Findings     string // one-sentence diagnosis summary
	CleanText    string // response text with signal lines removed
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
		if strings.HasPrefix(trimmed, "TRANSITION_TO:") {
			v := strings.TrimSpace(strings.TrimPrefix(trimmed, "TRANSITION_TO:"))
			if v != "none" && v != "" {
				result.TransitionTo = v
			}
		} else if strings.HasPrefix(trimmed, "ESCALATE_TO:") {
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

// findingsRecommendMonitor reports whether the FINDINGS line from a triage run
// indicates no actionable remediation is needed. The gate and auto-chain are
// both skipped when this returns true.
//
// Values that mean "nothing to do":
//   - recommended=monitor    (vacuum, slow-query, connection triage)
//   - recommended=no_changes_needed  (checkpoint-bgwriter triage)
func findingsRecommendMonitor(findings string) bool {
	if findings == "" {
		return false
	}
	const key = "recommended="
	idx := strings.Index(findings, key)
	if idx < 0 {
		return false
	}
	val := findings[idx+len(key):]
	if end := strings.IndexAny(val, "; \t\n"); end >= 0 {
		val = val[:end]
	}
	return val == "monitor" || val == "no_changes_needed"
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

// buildServerTypeHint returns a prompt fragment describing how the target server is hosted,
// so the agent knows which diagnostic tools are appropriate and doesn't apply K8s reasoning
// to Docker/VM-hosted servers or vice versa.
// Returns "" if cfg is nil or the server cannot be found.
func buildServerTypeHint(cfg *infra.Config, connectionString string) string {
	if cfg == nil || connectionString == "" {
		return ""
	}
	var server *infra.DBServer
	for key, db := range cfg.DBServers {
		if key == connectionString || db.Name == connectionString || db.ConnectionString == connectionString {
			d := db
			server = &d
			break
		}
	}
	if server == nil {
		return ""
	}

	if server.K8sCluster != "" {
		ns := server.K8sNamespace
		if ns == "" {
			ns = "default"
		}
		cluster := server.K8sCluster
		hint := fmt.Sprintf("Server type: Kubernetes pod (cluster: %s, namespace: %s)", cluster, ns)
		if server.K8sPodSelector != "" {
			hint += fmt.Sprintf(", pod selector: %s", server.K8sPodSelector)
		}
		hint += ".\nKubectl commands, pod log retrieval, and K8s event lookups are applicable to this server."
		return hint
	}

	var runtime string
	var vmAddr string
	if server.VMName != "" {
		if vm, ok := cfg.VMs[server.VMName]; ok {
			runtime = vm.Runtime
			vmAddr = vm.Address
		}
	}

	if runtime == "docker" || runtime == "podman" {
		hint := fmt.Sprintf("Server type: %s container", runtime)
		if vmAddr != "" {
			hint += fmt.Sprintf(" on VM %q (%s)", server.VMName, vmAddr)
		}
		if server.ContainerName != "" {
			hint += fmt.Sprintf(", container name: %s", server.ContainerName)
		}
		hint += ".\nThis is NOT a Kubernetes-managed server — do NOT attempt kubectl commands, pod queries, pod log retrieval, or K8s event lookups. Use docker/host-level diagnostics only."
		return hint
	}

	if server.VMName != "" {
		hint := fmt.Sprintf("Server type: bare VM (%s", server.VMName)
		if vmAddr != "" {
			hint += fmt.Sprintf(" / %s", vmAddr)
		}
		hint += ")"
		if server.SystemdUnit != "" {
			hint += fmt.Sprintf(", systemd unit: %s", server.SystemdUnit)
		}
		hint += ".\nThis is NOT a Kubernetes-managed server — do NOT use kubectl or K8s tools."
		return hint
	}

	return "Server type: standalone PostgreSQL — NOT Kubernetes-managed. Do NOT use kubectl or K8s tools."
}

// checkTargetScope returns connection strings from audit events for traceID that
// differ from intendedTarget. Used to detect when the agent queried a server
// other than the one specified in the playbook run request.
//
// cfg is used to resolve a short server name (e.g. "test-pg") to its canonical
// connection string from infra config, so that the full resolved form used by
// the agent (e.g. "host=localhost port=35432 dbname=postgres user=postgres")
// is not incorrectly flagged as drift.
//
// Returns nil when auditURL is empty, no traceID, or no drift is found.
func checkTargetScope(cfg *infra.Config, auditURL, apiKey, traceID string, since time.Time, intendedTarget string) []string {
	if auditURL == "" || traceID == "" || intendedTarget == "" {
		return nil
	}

	// Resolve short name to canonical connection string so that
	// "test-pg" and "host=localhost port=35432 ..." are treated as the same server.
	resolved := intendedTarget
	if cfg != nil {
		for key, db := range cfg.DBServers {
			if key == intendedTarget || db.Name == intendedTarget {
				resolved = db.ConnectionString
				break
			}
		}
	}

	// If intendedTarget is a short name (no "=" → not a connection string) and we
	// couldn't resolve it to a full connection string, we cannot reliably compare it
	// against the full-form strings the agent records in audit. Skip the check to
	// avoid false positives; callers should ensure infra config is loaded.
	if resolved == intendedTarget && !strings.Contains(intendedTarget, "=") {
		slog.Debug("checkTargetScope: cannot resolve short name to connection string, skipping",
			"intended", intendedTarget)
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
		if !targetMatches(intendedTarget, cs) && !targetMatches(resolved, cs) {
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

// targetMatches returns true when:
//  - actual == intended (exact), or
//  - intended appears as a field value in actual (short name, e.g. intended="test-pg",
//    actual="host=test-pg dbname=postgres"), or
//  - intended is a connection string whose fields are all present with equal values
//    in actual (actual may carry additional fields such as user= or password= that
//    were added by the agent at runtime and are absent from the infra config entry).
func targetMatches(intended, actual string) bool {
	if intended == actual {
		return true
	}
	// Short-name match: intended value appears as a field value in actual.
	for _, field := range strings.Fields(actual) {
		if kv := strings.SplitN(field, "=", 2); len(kv) == 2 && kv[1] == intended {
			return true
		}
	}
	// Subset match: every key=value pair in intended must exist in actual.
	// This handles the case where the agent appends user=, password=, etc.
	// password is excluded: the audit layer masks it as "***" so comparing
	// against the plain-text value from the infra config always false-positives.
	intendedFields := parseConnFields(intended)
	delete(intendedFields, "password")
	if len(intendedFields) == 0 {
		return false // intended is a plain name, not a connection string
	}
	actualFields := parseConnFields(actual)
	delete(actualFields, "password")
	for k, v := range intendedFields {
		if actualFields[k] != v {
			return false
		}
	}
	return true
}

// parseConnFields parses a psql key=value connection string into a map.
// Fields that are not in key=value form (no "=") are ignored.
func parseConnFields(s string) map[string]string {
	m := make(map[string]string)
	for _, field := range strings.Fields(s) {
		if kv := strings.SplitN(field, "=", 2); len(kv) == 2 {
			m[kv[0]] = kv[1]
		}
	}
	return m
}

// ---- agent_approve mode: step-by-step approval loop ----

// ApproveRunResponse is returned by POST /run (agent_approve) and POST /proceed.
type ApproveRunResponse struct {
	RunID                string        `json:"run_id"`
	Status               string        `json:"status"`                          // "pending_approval" | "complete" | "denied"
	Step                 *StepProposal `json:"step,omitempty"`
	ApprovalID           string        `json:"approval_id,omitempty"`
	Summary              string        `json:"summary,omitempty"`
	Warnings             []string      `json:"warnings,omitempty"`
	EffectiveApprovalMode string       `json:"effective_approval_mode,omitempty"` // resolved mode after clamping; use instead of requested mode
}

// ProceedRequest is the body for POST /api/v1/fleet/playbook-runs/{runID}/proceed.
type ProceedRequest struct {
	Resolution       string `json:"resolution"`                  // "approved" | "denied"
	ResolvedBy       string `json:"resolved_by,omitempty"`
	Reason           string `json:"reason,omitempty"`
	StepIndex        int    `json:"step_index"`
	ConnectionString string `json:"connection_string,omitempty"` // injected into tool args and re-planner context
}

// handlePlaybookRunApprove handles POST /run for execution_mode=agent_approve.
// It proposes the first step and returns a pending_approval response.
func (g *Gateway) handlePlaybookRunApprove(w http.ResponseWriter, r *http.Request, pb *audit.Playbook, req PlaybookRunRequest, runID string, warnings []string) {
	if g.plannerLLM == nil {
		writeError(w, http.StatusServiceUnavailable, "planner LLM not configured (required for agent_approve mode)")
		return
	}

	// Propose the first step (no history yet).
	proposal, done, summary, err := g.proposeNextStep(r.Context(), pb, req.ConnectionString, req.PriorFindings, nil)
	if err != nil {
		slog.Error("handlePlaybookRunApprove: step proposal failed", "playbook", pb.SeriesID, "err", err)
		writeError(w, http.StatusInternalServerError, "step proposal failed: "+err.Error())
		return
	}

	if done {
		// Unusual: playbook declares done on first proposal (no actions needed).
		go g.recordPlaybookRunComplete(context.WithoutCancel(r.Context()), runID, "resolved", "", "", summary, "", "", nil)
		resp := ApproveRunResponse{RunID: runID, Status: "complete", Summary: summary, Warnings: warnings, EffectiveApprovalMode: req.ApprovalMode}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Inject connection_string into the proposed args so the DB agent has a target.
	if req.ConnectionString != "" {
		if proposal.Args == nil {
			proposal.Args = map[string]any{}
		}
		if _, already := proposal.Args["connection_string"]; !already {
			proposal.Args["connection_string"] = req.ConnectionString
		}
	}

	// Persist the proposed step to auditd.
	stepRec := &audit.PlaybookRunStep{
		RunID:     runID,
		StepIndex: proposal.Index,
		Agent:     proposal.Agent,
		Tool:      proposal.Tool,
		Args:      proposal.Args,
		Reason:    proposal.Reason,
		Status:    "proposed",
	}
	if err := g.createRunStep(r.Context(), stepRec); err != nil {
		slog.Warn("handlePlaybookRunApprove: failed to persist step", "run_id", runID, "err", err)
		// Non-fatal: continue without persistence.
	}

	// Create a pending approval record.
	approvalID := g.createStepApproval(r.Context(), runID, proposal, r.Header.Get("X-User"))

	resp := ApproveRunResponse{
		RunID:                runID,
		Status:               "pending_approval",
		Step:                 proposal,
		ApprovalID:           approvalID,
		Warnings:             warnings,
		EffectiveApprovalMode: req.ApprovalMode,
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// handlePlaybookRunProceed handles POST /api/v1/fleet/playbook-runs/{runID}/proceed.
// It approves (or denies) the pending step, executes it on approval, then re-plans.
func (g *Gateway) handlePlaybookRunProceed(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "missing runID")
		return
	}

	var req ProceedRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
	}
	if req.Resolution == "" {
		req.Resolution = "approved"
	}
	resolvedBy := req.ResolvedBy
	if resolvedBy == "" {
		resolvedBy = r.Header.Get("X-User")
	}
	if resolvedBy == "" {
		resolvedBy = "operator"
	}

	// Fetch the playbook run to get the playbook details.
	run, err := g.fetchPlaybookRun(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found: "+err.Error())
		return
	}
	pb, err := g.fetchPlaybook(r.Context(), run.PlaybookID)
	if err != nil {
		writeError(w, http.StatusNotFound, "playbook not found: "+err.Error())
		return
	}

	// Fetch the pending step.
	pendingStep, err := g.fetchPendingStep(r.Context(), runID)
	if err != nil || pendingStep == nil {
		writeError(w, http.StatusConflict, "no pending step found for this run")
		return
	}

	// Validate step index if provided.
	if req.StepIndex != 0 && req.StepIndex != pendingStep.StepIndex {
		writeError(w, http.StatusConflict, fmt.Sprintf("step_index mismatch: expected %d, got %d", pendingStep.StepIndex, req.StepIndex))
		return
	}

	if req.Resolution == "denied" {
		g.updateRunStep(r.Context(), runID, pendingStep.StepIndex, "denied", "", "", "")
		go g.recordPlaybookRunComplete(context.WithoutCancel(r.Context()), runID, "abandoned", "", "", "step denied by operator", "", run.TraceID, nil)
		writeJSON(w, http.StatusOK, ApproveRunResponse{RunID: runID, Status: "denied"})
		return
	}

	// Mark step as executing.
	g.updateRunStep(r.Context(), runID, pendingStep.StepIndex, "executing", pendingStep.ApprovalID, "", "")

	// Inject connection_string into args if the tool accepts it and it's not already set.
	args := pendingStep.Args
	if args == nil {
		args = map[string]any{}
	}
	if _, hasConn := args["connection_string"]; !hasConn {
		if run.ContextID != "" {
			// ContextID might encode a connection string — but for step runs we
			// rely on the proposer to have included it from the playbook run request.
		}
	}

	// Prefer the run's stored trace_id (set at run-start for agent_approve runs)
	// so all step events share one trace and are discoverable via /events.
	// Fall back to the request header, then generate a fresh sa_* prefix.
	traceID := run.TraceID
	if traceID == "" {
		traceID = r.Header.Get("X-Trace-ID")
	}
	if traceID == "" {
		traceID = audit.NewTraceIDWithPrefix("sa_")
	}
	result, toolErr := g.callToolForStep(r.Context(), r, traceID, "remediation", pendingStep.Agent, pendingStep.Tool, args)

	var stepStatus, stepErrStr string
	if toolErr != nil {
		stepStatus = "failed"
		stepErrStr = toolErr.Error()
		result = ""
		slog.Error("handlePlaybookRunProceed: tool execution failed",
			"run_id", runID, "tool", pendingStep.Tool, "err", toolErr)
	} else {
		stepStatus = "succeeded"
	}
	g.updateRunStep(r.Context(), runID, pendingStep.StepIndex, stepStatus, pendingStep.ApprovalID, result, stepErrStr)

	if toolErr != nil {
		go g.recordPlaybookRunComplete(context.WithoutCancel(r.Context()), runID, "abandoned", "", "", "tool execution failed: "+stepErrStr, "", run.TraceID, nil)
		writeError(w, http.StatusUnprocessableEntity, "tool execution failed: "+stepErrStr)
		return
	}

	// Fetch full history for re-planning.
	history, err := g.fetchRunSteps(r.Context(), runID)
	if err != nil {
		slog.Warn("handlePlaybookRunProceed: failed to fetch step history", "run_id", runID, "err", err)
		history = nil
	}

	// Determine connection string for the re-planner: prefer the one in the
	// proceed request, then fall back to extracting it from the executed step args.
	connStr := req.ConnectionString
	if connStr == "" {
		if cs, ok := args["connection_string"].(string); ok {
			connStr = cs
		}
	}
	// Last resort: scan history for any step that has connection_string in args.
	if connStr == "" {
		for _, hs := range history {
			if cs, ok := hs.Args["connection_string"].(string); ok && cs != "" {
				connStr = cs
				break
			}
		}
	}

	nextProposal, done, summary, err := g.proposeNextStep(r.Context(), pb, connStr, "", history)
	if err != nil {
		slog.Error("handlePlaybookRunProceed: re-planning failed", "run_id", runID, "err", err)
		go g.recordPlaybookRunComplete(context.WithoutCancel(r.Context()), runID, "abandoned", "", "", "re-planning failed: "+err.Error(), "", run.TraceID, nil)
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("re-planning failed after step %d: %v", pendingStep.StepIndex, err))
		return
	}

	if done {
		go g.recordPlaybookRunComplete(context.WithoutCancel(r.Context()), runID, "resolved", "", "", summary, "", run.TraceID, nil)
		writeJSON(w, http.StatusOK, ApproveRunResponse{RunID: runID, Status: "complete", Summary: summary})
		return
	}

	// Inject connection_string into next proposal's args if we know it.
	if connStr != "" {
		if nextProposal.Args == nil {
			nextProposal.Args = map[string]any{}
		}
		if _, already := nextProposal.Args["connection_string"]; !already {
			nextProposal.Args["connection_string"] = connStr
		}
	}

	// Persist next proposed step.
	nextRec := &audit.PlaybookRunStep{
		RunID:     runID,
		StepIndex: nextProposal.Index,
		Agent:     nextProposal.Agent,
		Tool:      nextProposal.Tool,
		Args:      nextProposal.Args,
		Reason:    nextProposal.Reason,
		Status:    "proposed",
	}
	if err := g.createRunStep(r.Context(), nextRec); err != nil {
		slog.Warn("handlePlaybookRunProceed: failed to persist next step", "run_id", runID, "err", err)
	}

	approvalID := g.createStepApproval(r.Context(), runID, nextProposal, resolvedBy)
	writeJSON(w, http.StatusOK, ApproveRunResponse{
		RunID:      runID,
		Status:     "pending_approval",
		Step:       nextProposal,
		ApprovalID: approvalID,
	})
}

// ---- auditd helpers for step store ----

func (g *Gateway) createRunStep(ctx context.Context, step *audit.PlaybookRunStep) error {
	if g.auditURL == "" {
		return nil
	}
	body, err := json.Marshal(step)
	if err != nil {
		return err
	}
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbook-runs/" + step.RunID + "/steps"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("auditd returned %d creating step", resp.StatusCode)
	}
	return nil
}

func (g *Gateway) updateRunStep(ctx context.Context, runID string, stepIndex int, status, approvalID, result, stepErr string) {
	if g.auditURL == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{
		"status":      status,
		"approval_id": approvalID,
		"result":      result,
		"error":       stepErr,
	})
	url := fmt.Sprintf("%s/v1/fleet/playbook-runs/%s/steps/%d",
		strings.TrimSuffix(g.auditURL, "/"), runID, stepIndex)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		slog.Warn("updateRunStep: build request failed", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("updateRunStep: request failed", "err", err)
		return
	}
	resp.Body.Close()
}

func (g *Gateway) fetchPendingStep(ctx context.Context, runID string) (*audit.PlaybookRunStep, error) {
	if g.auditURL == "" {
		return nil, fmt.Errorf("auditd not configured")
	}
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbook-runs/" + runID + "/pending-step"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auditd returned %d", resp.StatusCode)
	}
	var step audit.PlaybookRunStep
	if err := json.NewDecoder(resp.Body).Decode(&step); err != nil {
		return nil, err
	}
	return &step, nil
}

func (g *Gateway) fetchRunSteps(ctx context.Context, runID string) ([]*audit.PlaybookRunStep, error) {
	if g.auditURL == "" {
		return nil, nil
	}
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbook-runs/" + runID + "/steps"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
		return nil, fmt.Errorf("auditd returned %d", resp.StatusCode)
	}
	var result struct {
		Steps []*audit.PlaybookRunStep `json:"steps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Steps, nil
}

// createStepApproval creates a StoredApproval record in auditd for the given step
// and returns the approval_id (or empty string on failure).
func (g *Gateway) createStepApproval(ctx context.Context, runID string, proposal *StepProposal, requestedBy string) string {
	if g.auditURL == "" {
		return ""
	}
	if requestedBy == "" {
		requestedBy = "gateway"
	}
	approval := audit.StoredApproval{
		ActionClass:  "destructive",
		ToolName:     proposal.Tool,
		AgentName:    proposal.Agent,
		RequestedBy:  requestedBy,
		Status:       "pending",
		TraceID:      runID,
		RequestContext: map[string]any{
			"run_id":       runID,
			"step_index":   proposal.Index,
			"args":         proposal.Args,
			"reason":       proposal.Reason,
			"action_class": proposal.ActionClass,
		},
	}
	body, err := json.Marshal(approval)
	if err != nil {
		return ""
	}
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/approvals"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("createStepApproval: request failed", "run_id", runID, "err", err)
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return ""
	}
	var stored audit.StoredApproval
	if err := json.NewDecoder(resp.Body).Decode(&stored); err != nil {
		return ""
	}

	// Update the step record with the approval_id.
	go g.updateRunStep(context.WithoutCancel(ctx), runID, proposal.Index, "proposed", stored.ApprovalID, "", "")
	return stored.ApprovalID
}
