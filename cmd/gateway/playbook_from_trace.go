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
	"helpdesk/internal/audit"
)

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
	traceJSON, err := g.fetchTraceEvents(req.TraceID)
	if err != nil {
		slog.Warn("from-trace: failed to fetch trace events", "trace_id", req.TraceID, "err", err)
		// Continue with an empty trace — LLM will produce a generic template.
		traceJSON = fmt.Sprintf(`[{"note": "trace %s not found or auditd unavailable"}]`, req.TraceID)
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
	var playbookID string
	if g.auditURL != "" {
		importResp, parseErr := parsePlaybookYAML(draft, PlaybookImportHints{})
		if parseErr != nil {
			slog.Warn("from-trace: could not parse LLM YAML; skipping persistence", "err", parseErr)
		} else {
			pb := importResp.Draft
			pb.Source = "generated"
			pb.IsActive = false
			pb.CreatedBy = "from-trace"
			if pb.SeriesID == "" {
				pb.SeriesID = "pbs_generated_" + uuid.New().String()[:8]
			}
			id, persistErr := g.persistPlaybookDraft(r.Context(), pb)
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

