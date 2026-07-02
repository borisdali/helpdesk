package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
	"helpdesk/internal/audit"
)

// isEmptyJSONArray returns true when s decodes to an empty or null JSON array.
// json.Encoder.Encode always appends \n, so string equality against "[]" is not reliable.
func isEmptyJSONArray(s string) bool {
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &items); err != nil {
		return true // unparseable → treat as empty
	}
	return len(items) == 0
}

// PlaybookFromTraceRequest is the body for POST /api/v1/fleet/playbooks/from-trace.
type PlaybookFromTraceRequest struct {
	TraceID  string `json:"trace_id"`            // audit trace ID to synthesize from
	Outcome  string `json:"outcome"`             // "resolved" | "escalated"
	SeriesID string `json:"series_id,omitempty"` // pin draft to existing series (suggest-update)
	Version  string `json:"version,omitempty"`   // explicit version for the draft (suggest-update)
}

// PlaybookFromTraceResponse is returned by handlePlaybookFromTrace.
// When auditd is configured, the draft is persisted as an inactive "generated"
// playbook and its ID is returned in PlaybookID for later activation or review.
type PlaybookFromTraceResponse struct {
	Draft      string   `json:"draft"`                  // synthesized playbook YAML text
	Source     string   `json:"source"`                 // trace_id used as source
	PlaybookID string   `json:"playbook_id,omitempty"`  // ID of the persisted draft (empty if auditd unavailable)
	Warnings   []string `json:"warnings,omitempty"`     // protocol violations detected in the draft
}

// fromTracePromptTemplate is used for cold synthesis when no active playbook exists.
const fromTracePromptTemplate = `You are a playbook authoring assistant for an AI database operations platform.

Given the following sequence of diagnostic tool calls and their outcomes from a %s incident,
synthesize a playbook YAML that captures the diagnostic and remediation approach.

The playbook should cover:
- name: a short descriptive name for this scenario
- description: what this playbook does and why (used by the fleet planner to select it)
- playbook_type: "triage" if the trace is a read-only investigation ending with a FINDINGS/signal line; "remediation" if it executes corrective actions
- problem_class: one of: performance | availability | capacity | data_integrity | security
- symptoms: observable indicators that would trigger this playbook
- guidance: expert reasoning, the sequence of investigation steps, and any heuristics
- escalation: conditions where a human operator must intervene

Tool execution trace:
%s

Respond with YAML only — a single playbook document, no markdown fences, no other text.`

// fromTraceUpdatePromptTemplate is used when updating an existing playbook (suggest-update).
// It includes the active version so the model improves rather than replaces.
const fromTraceUpdatePromptTemplate = `You are a playbook authoring assistant for an AI database operations platform.

Given a sequence of diagnostic tool calls from a %s incident, produce an IMPROVED version of the existing playbook below.

EXISTING PLAYBOOK (active version — improve this, do not start from scratch):
%s

TOOL EXECUTION TRACE (from this incident):
%s

Improvement rules — follow these exactly:
1. PRESERVE all numeric thresholds in escalation and guidance. Do not relax, remove, or replace existing escalation criteria — only add new ones alongside them.
2. ADD investigation steps, heuristics, or patterns clearly observed in the trace that are missing from the current guidance.
3. IMPROVE name, description, and symptoms if the trace reveals a more precise framing.
4. TREAT trace-specific observed values as illustrative examples, not universal thresholds. If you reference a specific measurement from the trace (e.g., "50%% dead ratio"), explicitly label it as an example: "e.g., in one observed case: X".
5. Keep the "Required output" trailer (FINDINGS:/TRANSITION_TO:/ESCALATE_TO: lines) in guidance verbatim — it is an operational protocol, not knowledge content.
6. Escalation must be at least as strict as the existing version; add criteria but do not relax existing ones.

Respond with YAML only — a single playbook document, no markdown fences, no other text.`

// handlePlaybookFromTrace handles POST /api/v1/fleet/playbooks/from-trace.
// It fetches audit events for the given trace_id, then uses the plannerLLM
// to synthesize a playbook YAML draft and returns it without persisting it.
func (g *Gateway) handlePlaybookFromTrace(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body: "+err.Error())
		return
	}

	var req PlaybookFromTraceRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.TraceID == "" {
		writeError(w, http.StatusBadRequest, `"trace_id" is required`)
		return
	}
	if req.Outcome == "" {
		req.Outcome = "resolved"
	}

	if g.plannerLLM == nil {
		writeError(w, http.StatusServiceUnavailable, "LLM not configured (plannerLLM is nil)")
		return
	}

	// Fetch tool execution events for this trace from auditd.
	// Refuse to synthesize when auditd is unavailable or the trace has no events —
	// calling the LLM with an empty trace produces hallucinated generic content.
	traceJSON, err := g.fetchTraceEvents(req.TraceID)
	if err != nil {
		if g.auditURL == "" {
			writeError(w, http.StatusServiceUnavailable, "auditd not configured: cannot synthesize playbook without an audit trace")
		} else {
			writeError(w, http.StatusNotFound, fmt.Sprintf("trace %q not found in auditd: %v", req.TraceID, err))
		}
		return
	}
	if isEmptyJSONArray(traceJSON) {
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("trace %q contains no tool execution events: nothing to synthesize from", req.TraceID))
		return
	}

	// When updating an existing series, fetch the active playbook before synthesis
	// so the LLM can improve it rather than synthesize from scratch. This prevents
	// the model from (a) embedding trace-specific values as universal thresholds and
	// (b) replacing precise numeric escalation gates with narrative prose.
	var activePlaybook *audit.Playbook
	if req.SeriesID != "" && g.auditURL != "" {
		if active, ferr := g.fetchPlaybookBySeriesID(r.Context(), req.SeriesID); ferr == nil {
			activePlaybook = active
		} else {
			slog.Warn("from-trace: could not fetch active version for prompt context; falling back to cold synthesis",
				"series_id", req.SeriesID, "err", ferr)
		}
	}

	var prompt string
	if activePlaybook != nil {
		prompt = fmt.Sprintf(fromTraceUpdatePromptTemplate, req.Outcome, formatActivePlaybookYAML(activePlaybook), traceJSON)
	} else {
		prompt = fmt.Sprintf(fromTracePromptTemplate, req.Outcome, traceJSON)
	}

	draft, err := g.plannerLLM(r.Context(), prompt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LLM synthesis failed: "+err.Error())
		return
	}

	// Strip any accidental markdown fences the model may emit.
	draft = strings.TrimSpace(stripMarkdownFences(draft))

	// Parse the YAML draft and persist it as an inactive "generated" playbook
	// so operators can review and activate it without copy-pasting.
	// Use the lenient map-based parser — LLM output is inconsistent and may have
	// tab indentation, nested structures, or other formatting that the strict
	// struct-based parser rejects.
	var playbookID string
	var warnings []string
	if g.auditURL != "" {
		pb, parseErr := parsePlaybookYAMLLenient(draft)
		if parseErr != nil {
			slog.Warn("from-trace: could not parse LLM YAML; skipping persistence", "err", parseErr)
		} else {
			pb.Source = "generated"
			pb.IsActive = false
			pb.CreatedBy = "from-trace"
			pb.OriginTrace = req.TraceID
			// Caller-supplied series_id and version take precedence over whatever
			// the LLM wrote — this is how suggest-update pins the draft to an
			// existing series and assigns the correct next version number.
			if req.SeriesID != "" {
				pb.SeriesID = req.SeriesID
			}
			if req.Version != "" {
				pb.Version = req.Version
			}
			// When pinning to an existing series, carry over all operational fields
			// from the currently-active version. Only knowledge fields (name,
			// description, guidance, symptoms, escalation, problem_class) should
			// change between versions — routing, execution mode, and tool permissions
			// are controlled by operators, not synthesized from traces.
			if activePlaybook != nil {
				pb.ExecutionMode    = activePlaybook.ExecutionMode
				pb.ApprovalMode     = activePlaybook.ApprovalMode
				pb.AgentName        = activePlaybook.AgentName
				pb.TransitionsTo    = activePlaybook.TransitionsTo
				pb.EscalatesTo      = activePlaybook.EscalatesTo
				pb.EntryPoint       = activePlaybook.EntryPoint
				pb.RequiresEvidence = activePlaybook.RequiresEvidence
				pb.PermittedTools   = activePlaybook.PermittedTools
				pb.TargetHints      = activePlaybook.TargetHints
				pb.PlaybookType     = activePlaybook.PlaybookType
				// Preserve the structured output protocol embedded in guidance.
				// The "Required output" trailer (HYPOTHESIS_N / FINDINGS /
				// TRANSITION_TO lines) is an operational instruction, not
				// knowledge derivable from the trace. When using the update
				// prompt the LLM is instructed to keep it verbatim, so only
				// append if the LLM omitted it (cold synthesis path).
				if idx := strings.Index(activePlaybook.Guidance, "\nRequired output"); idx >= 0 {
					trailer := strings.TrimRight(activePlaybook.Guidance[idx:], "\n")
					if !strings.Contains(pb.Guidance, "Required output") {
						pb.Guidance = strings.TrimRight(pb.Guidance, "\n") + "\n" + trailer
					}
				}
			} else if req.SeriesID != "" {
				// Active playbook fetch failed earlier (logged above); try once more
				// for operational field preservation so the draft is still usable.
				if active, ferr := g.fetchPlaybookBySeriesID(r.Context(), req.SeriesID); ferr == nil {
					pb.ExecutionMode    = active.ExecutionMode
					pb.ApprovalMode     = active.ApprovalMode
					pb.AgentName        = active.AgentName
					pb.TransitionsTo    = active.TransitionsTo
					pb.EscalatesTo      = active.EscalatesTo
					pb.EntryPoint       = active.EntryPoint
					pb.RequiresEvidence = active.RequiresEvidence
					pb.PermittedTools   = active.PermittedTools
					pb.TargetHints      = active.TargetHints
					pb.PlaybookType     = active.PlaybookType
					if idx := strings.Index(active.Guidance, "\nRequired output"); idx >= 0 {
						trailer := strings.TrimRight(active.Guidance[idx:], "\n")
						if !strings.Contains(pb.Guidance, "Required output") {
							pb.Guidance = strings.TrimRight(pb.Guidance, "\n") + "\n" + trailer
						}
					}
				}
			}
			if pb.SeriesID == "" {
				pb.SeriesID = "pbs_generated_" + uuid.New().String()[:8]
			}
			warnings = validatePlaybookProtocol(pb)
			if len(warnings) > 0 {
				slog.Warn("from-trace: draft has protocol violations", "playbook_type", pb.PlaybookType, "warnings", warnings)
			}
			id, persistErr := g.persistPlaybookDraft(r.Context(), &pb)
			if persistErr != nil {
				slog.Warn("from-trace: could not persist draft", "err", persistErr)
			} else {
				playbookID = id
				slog.Info("from-trace: persisted draft playbook", "playbook_id", playbookID, "series_id", pb.SeriesID)
			}
		}
	}

	writeJSON(w, http.StatusOK, PlaybookFromTraceResponse{
		Draft:      draft,
		Source:     req.TraceID,
		PlaybookID: playbookID,
		Warnings:   warnings,
	})
}

// persistPlaybookDraft POSTs a playbook to auditd and returns the assigned playbook_id.
func (g *Gateway) persistPlaybookDraft(ctx context.Context, pb *audit.Playbook) (string, error) {
	data, err := json.Marshal(pb)
	if err != nil {
		return "", fmt.Errorf("marshal playbook: %w", err)
	}
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbooks"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("auditd request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auditd returned %d: %s", resp.StatusCode, body)
	}
	var created audit.Playbook
	if err := json.Unmarshal(body, &created); err != nil {
		return "", fmt.Errorf("parse auditd response: %w", err)
	}
	return created.PlaybookID, nil
}

// fetchTraceEvents queries auditd for all events belonging to the given trace_id
// and returns them as a JSON string. Returns an error when auditd is unavailable
// or the trace is not found.
func (g *Gateway) fetchTraceEvents(traceID string) (string, error) {
	if g.auditURL == "" {
		return "", fmt.Errorf("auditd URL not configured")
	}

	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/events?trace_id=" + traceID + "&event_type=tool_execution"

	hreq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	if g.auditAPIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}

	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return "", fmt.Errorf("auditd request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading auditd response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auditd returned %d: %s", resp.StatusCode, string(data))
	}

	// When audit_events has no entries for this trace ID but the ID looks like a
	// playbook run (plr_ prefix), fall back to playbook_run_steps. This covers
	// the common case where agents emit step events under approval-request trace
	// IDs (ar_*) rather than the run ID, so audit_events is empty for the plr_*
	// ID even though full step data exists in playbook_run_steps.
	if isEmptyJSONArray(string(data)) && strings.HasPrefix(traceID, "plr_") {
		return g.fetchStepsAsTraceEvents(traceID)
	}
	return string(data), nil
}

// fetchStepsAsTraceEvents fetches playbook_run_steps for a run and returns them
// formatted as a JSON array of tool_execution events so from-trace can synthesize
// a playbook from runs that don't have audit_events entries.
func (g *Gateway) fetchStepsAsTraceEvents(runID string) (string, error) {
	stepsURL := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbook-runs/" + runID + "/steps"
	hreq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, stepsURL, nil)
	if err != nil {
		return "", fmt.Errorf("build steps request: %w", err)
	}
	if g.auditAPIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return "", fmt.Errorf("steps request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading steps response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("steps endpoint returned %d", resp.StatusCode)
	}

	// Unmarshal the steps and reformat as trace events for the LLM prompt.
	var body struct {
		Steps []struct {
			StepIndex int             `json:"step_index"`
			Agent     string          `json:"agent"`
			Tool      string          `json:"tool"`
			Args      json.RawMessage `json:"args"`
			Reason    string          `json:"reason"`
			Status    string          `json:"status"`
			Result    string          `json:"result"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return "", fmt.Errorf("parsing steps: %w", err)
	}
	type traceEvent struct {
		EventType string          `json:"event_type"`
		Agent     string          `json:"agent"`
		Tool      string          `json:"tool"`
		Args      json.RawMessage `json:"args"`
		Reason    string          `json:"reason,omitempty"`
		Result    string          `json:"result,omitempty"`
		Status    string          `json:"status"`
	}
	events := make([]traceEvent, 0, len(body.Steps))
	for _, s := range body.Steps {
		events = append(events, traceEvent{
			EventType: "tool_execution",
			Agent:     s.Agent,
			Tool:      s.Tool,
			Args:      s.Args,
			Reason:    s.Reason,
			Result:    s.Result,
			Status:    s.Status,
		})
	}
	out, err := json.Marshal(events)
	if err != nil {
		return "", fmt.Errorf("marshaling trace events: %w", err)
	}
	return string(out), nil
}

// parsePlaybookYAMLLenient parses LLM-generated playbook YAML into an audit.Playbook
// using a map[string]interface{} intermediate so that nested structures, tab indentation,
// and other LLM formatting quirks don't cause hard parse failures.
func parsePlaybookYAMLLenient(text string) (audit.Playbook, error) {
	// Replace tab indentation with spaces — YAML forbids tabs.
	text = strings.ReplaceAll(text, "\t", "  ")
	// Remove markdown artifacts the LLM embeds inside YAML block scalars:
	// triple-backtick fences (``` or ```sql etc.) and lines that start with
	// a bare backtick or @ which YAML forbids as plain-scalar starters.
	{
		lines := strings.Split(text, "\n")
		kept := lines[:0]
		for _, line := range lines {
			trimmed := strings.TrimLeft(line, " \t")
			if strings.HasPrefix(trimmed, "```") {
				// Drop markdown code fences entirely.
				continue
			}
			if strings.HasPrefix(trimmed, "`") || strings.HasPrefix(trimmed, "@") {
				// Prefix with a space so it becomes block scalar continuation.
				indent := line[:len(line)-len(trimmed)]
				kept = append(kept, indent+" "+trimmed)
				continue
			}
			kept = append(kept, line)
		}
		text = strings.Join(kept, "\n")
	}
	// Strip any bare non-YAML first line (e.g. "yaml", "YAML", "json").
	// These are language tags that some models emit without backtick fences.
	// A valid YAML first line either starts a mapping (contains ":"), is a list
	// item ("-"), or is a document separator ("---").
	if nl := strings.IndexAny(text, "\n\r"); nl > 0 {
		firstLine := strings.TrimSpace(text[:nl])
		if firstLine != "" && firstLine != "---" &&
			!strings.Contains(firstLine, ":") &&
			!strings.HasPrefix(firstLine, "-") {
			text = strings.TrimSpace(text[nl:])
		}
	}

	var raw map[string]interface{}
	if err := yaml.Unmarshal([]byte(text), &raw); err != nil {
		// Fallback: the YAML contains a character the parser rejects (e.g. a backtick
		// or @ at the start of a line inside guidance). Extract top-level scalar fields
		// with a simple line scan and store the full draft as Guidance so nothing is lost.
		pb := extractPlaybookScalars(text)
		if pb.Name == "" && pb.Description == "" {
			return audit.Playbook{}, err // truly unparseable
		}
		if pb.Guidance == "" {
			pb.Guidance = text
		}
		slog.Debug("from-trace: YAML parse failed; used scalar fallback", "err", err, "name", pb.Name)
		return pb, nil
	}

	str := func(key string) string {
		v := raw[key]
		return yamlToString(v)
	}
	strSlice := func(key string) []string {
		v := raw[key]
		return yamlToStringSlice(v)
	}

	return audit.Playbook{
		Name:         str("name"),
		Description:  str("description"),
		ProblemClass: str("problem_class"),
		Guidance:     str("guidance"),
		Symptoms:     strSlice("symptoms"),
		Escalation:   strSlice("escalation"),
		TargetHints:  strSlice("target_hints"),
		EscalatesTo:   strSlice("escalates_to"),
		TransitionsTo: strSlice("transitions_to"),
		SeriesID:      str("series_id"),
		Author:        str("author"),
		Version:       str("version"),
		PlaybookType:  str("playbook_type"),
	}, nil
}

// extractPlaybookScalars extracts simple key: value lines from YAML text that
// failed to parse. Used as a last-resort fallback when the YAML contains
// characters that go-yaml rejects (e.g. backtick or @ starting a line).
func extractPlaybookScalars(text string) audit.Playbook {
	var pb audit.Playbook
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		// Only grab simple top-level scalars (no leading whitespace = not nested).
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		switch k {
		case "name":
			pb.Name = v
		case "description":
			pb.Description = v
		case "problem_class":
			pb.ProblemClass = v
		case "author":
			pb.Author = v
		case "version":
			pb.Version = v
		case "series_id":
			pb.SeriesID = v
		case "playbook_type":
			pb.PlaybookType = v
		}
	}
	return pb
}

// validatePlaybookProtocol checks that pb satisfies the structural invariants
// required by its playbook_type. Returns a slice of human-readable warning
// strings (empty when the playbook is valid or has no type set).
// Violations are non-blocking — the draft is still persisted so the operator
// can review and correct before activation.
func validatePlaybookProtocol(pb audit.Playbook) []string {
	var warns []string

	require := func(value, field string) {
		if strings.TrimSpace(value) == "" {
			warns = append(warns, field+": required field is empty")
		}
	}
	requireSlice := func(items []string, field string) {
		if len(items) == 0 {
			warns = append(warns, field+": at least one entry required")
		}
	}

	switch pb.PlaybookType {
	case "triage":
		require(pb.Name, "name")
		require(pb.SeriesID, "series_id")
		require(pb.Description, "description")
		require(pb.Guidance, "guidance")
		requireSlice(pb.Symptoms, "symptoms")
		requireSlice(pb.Escalation, "escalation")
		if pb.ExecutionMode != "agent" {
			warns = append(warns, fmt.Sprintf("execution_mode: triage playbooks must use \"agent\", got %q", pb.ExecutionMode))
		}
		if !strings.Contains(pb.Guidance, "FINDINGS:") {
			warns = append(warns, "guidance: missing FINDINGS: line — triage playbooks must emit structured findings")
		}
		if !strings.Contains(pb.Guidance, "TRANSITION_TO:") && !strings.Contains(pb.Guidance, "ESCALATE_TO:") {
			warns = append(warns, "guidance: missing signal line — must end with TRANSITION_TO: <series_id> or ESCALATE_TO: <target|none>")
		}

	case "remediation":
		require(pb.Name, "name")
		require(pb.SeriesID, "series_id")
		require(pb.Description, "description")
		require(pb.Guidance, "guidance")
		requireSlice(pb.Symptoms, "symptoms")
		requireSlice(pb.Escalation, "escalation")
		if pb.ExecutionMode != "agent_approve" {
			warns = append(warns, fmt.Sprintf("execution_mode: remediation playbooks must use \"agent_approve\", got %q", pb.ExecutionMode))
		}
		if strings.Contains(pb.Guidance, "TRANSITION_TO:") || strings.Contains(pb.Guidance, "ESCALATE_TO:") {
			warns = append(warns, "guidance: remediation playbooks must not emit TRANSITION_TO or ESCALATE_TO — routing is handled by the triage gate")
		}
	}

	return warns
}

// formatActivePlaybookYAML renders the knowledge fields of an active playbook as
// YAML text for inclusion in the update synthesis prompt. Only the fields the LLM
// is expected to improve are included — operational fields (execution_mode, tools,
// routing) are preserved separately and must not be re-synthesized.
func formatActivePlaybookYAML(pb *audit.Playbook) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("name: %s\n", pb.Name))
	sb.WriteString(fmt.Sprintf("description: |\n  %s\n",
		strings.ReplaceAll(strings.TrimRight(pb.Description, "\n"), "\n", "\n  ")))
	if pb.ProblemClass != "" {
		sb.WriteString(fmt.Sprintf("problem_class: %s\n", pb.ProblemClass))
	}
	if len(pb.Symptoms) > 0 {
		sb.WriteString("symptoms:\n")
		for _, s := range pb.Symptoms {
			sb.WriteString(fmt.Sprintf("  - %s\n", s))
		}
	}
	sb.WriteString(fmt.Sprintf("guidance: |\n  %s\n",
		strings.ReplaceAll(strings.TrimRight(pb.Guidance, "\n"), "\n", "\n  ")))
	if len(pb.Escalation) > 0 {
		sb.WriteString("escalation:\n")
		for _, e := range pb.Escalation {
			sb.WriteString(fmt.Sprintf("  - %s\n", e))
		}
	}
	return sb.String()
}
