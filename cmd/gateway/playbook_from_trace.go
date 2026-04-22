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
	TraceID string `json:"trace_id"` // audit trace ID to synthesize from
	Outcome string `json:"outcome"`  // "resolved" | "escalated"
}

// PlaybookFromTraceResponse is returned by handlePlaybookFromTrace.
// When auditd is configured, the draft is persisted as an inactive "generated"
// playbook and its ID is returned in PlaybookID for later activation or review.
type PlaybookFromTraceResponse struct {
	Draft      string `json:"draft"`                 // synthesized playbook YAML text
	Source     string `json:"source"`                // trace_id used as source
	PlaybookID string `json:"playbook_id,omitempty"` // ID of the persisted draft (empty if auditd unavailable)
}

const fromTracePromptTemplate = `You are a playbook authoring assistant for an AI database operations platform.

Given the following sequence of diagnostic tool calls and their outcomes from a %s incident,
synthesize a playbook YAML that captures the diagnostic and remediation approach.

The playbook should cover:
- name: a short descriptive name for this scenario
- description: what this playbook does and why (used by the fleet planner to select it)
- problem_class: one of: performance | availability | capacity | data_integrity | security
- symptoms: observable indicators that would trigger this playbook
- guidance: expert reasoning, the sequence of investigation steps, and any heuristics
- escalation: conditions where a human operator must intervene

Tool execution trace:
%s

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

	prompt := fmt.Sprintf(fromTracePromptTemplate, req.Outcome, traceJSON)

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
	if g.auditURL != "" {
		pb, parseErr := parsePlaybookYAMLLenient(draft)
		if parseErr != nil {
			slog.Warn("from-trace: could not parse LLM YAML; skipping persistence", "err", parseErr)
		} else {
			pb.Source = "generated"
			pb.IsActive = false
			pb.CreatedBy = "from-trace"
			if pb.SeriesID == "" {
				pb.SeriesID = "pbs_generated_" + uuid.New().String()[:8]
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
	return string(data), nil
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
		v, _ := raw[key]
		return yamlToString(v)
	}
	strSlice := func(key string) []string {
		v, _ := raw[key]
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
		EscalatesTo:  strSlice("escalates_to"),
		SeriesID:     str("series_id"),
		Author:       str("author"),
		Version:      str("version"),
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
		}
	}
	return pb
}

