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
	PriorRunID       string `json:"prior_run_id,omitempty"` // run_id of prior investigation for continuity threading
	PriorFindings    string `json:"-"`                       // populated at runtime from prior run; not from body
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
		go g.recordPlaybookRunComplete(context.WithoutCancel(r.Context()), runID, "unknown", "", "")
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

	// Capture the agent response to parse escalation signals.
	capture := newResponseCapture()
	g.proxyToAgent(capture, r, agentNameDB, req.ContextID, prompt)

	var outcome, escalatedTo, findings string
	extra := map[string]any{}

	if capture.code == http.StatusOK {
		var respBody map[string]any
		if err := json.Unmarshal(capture.body.Bytes(), &respBody); err == nil {
			if text, ok := respBody["text"].(string); ok {
				esc := parseAgentEscalation(text)
				findings = esc.Findings
				if esc.EscalateTo != "" {
					outcome = "escalated"
					escalatedTo = esc.EscalateTo
					extra["escalation_hint"] = esc.EscalateTo
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
	if len(warnings) > 0 {
		extra["warnings"] = warnings
	}

	injectFields(w, capture, extra)

	// Record completion with real outcome in background.
	if runID != "" {
		go g.recordPlaybookRunComplete(context.WithoutCancel(r.Context()), runID, outcome, escalatedTo, findings)
	}
}

// assembleTriagePrompt builds the LLM prompt for an agent-mode playbook run.
func assembleTriagePrompt(pb *audit.Playbook, req PlaybookRunRequest) string {
	var b strings.Builder

	b.WriteString("You are performing a database availability investigation.\n\n")

	// Response protocol first — models attend more reliably to instructions at the top.
	b.WriteString("## Response Protocol\n")
	b.WriteString("Do NOT write a CONCLUSION section or end with '---'. Instead, close your response with these two plain-text lines (no markdown, no bold, no backticks):\n")
	b.WriteString("FINDINGS: <one-sentence diagnosis and recommended action>\n")
	b.WriteString("ESCALATE_TO: <series_id or \"none\">\n\n")

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
		fmt.Fprintf(&b, "## Target\nConnection string: `%s`\n\n", req.ConnectionString)
	}
	if req.Context != "" {
		fmt.Fprintf(&b, "## Additional context\n%s\n\n", req.Context)
	}

	slog.Debug("triage prompt assembled", "playbook", pb.SeriesID, "prompt_len", b.Len(), "prompt", b.String())

	// Closing reminder — Gemini attends more reliably when the instruction is
	// repeated as the last thing in the prompt. Explicitly forbid the patterns
	// Gemini uses as alternatives (**CONCLUSION:** and trailing ---).
	b.WriteString("IMPORTANT: Do not write **CONCLUSION:** or end with ---. Close with exactly:\n")
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
