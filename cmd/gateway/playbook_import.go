package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"gopkg.in/yaml.v3"
	"helpdesk/internal/audit"
)

// PlaybookImportRequest is the body for POST /api/v1/fleet/playbooks/import.
type PlaybookImportRequest struct {
	Text   string              `json:"text"`
	Format string              `json:"format"` // "markdown" | "text" | "yaml" | "rundeck" | "ansible"
	Hints  PlaybookImportHints `json:"hints,omitempty"`
}

// PlaybookImportHints are optional pre-filled fields the caller can provide to
// guide the LLM or override missing values in a YAML import.
type PlaybookImportHints struct {
	Name         string `json:"name,omitempty"`
	ProblemClass string `json:"problem_class,omitempty"`
	SeriesID     string `json:"series_id,omitempty"`
}

// PlaybookImportResponse is the response for a successful import. The draft is
// not persisted; the caller reviews it and calls POST /api/v1/fleet/playbooks to save.
type PlaybookImportResponse struct {
	Draft           *audit.Playbook `json:"draft"`
	WarningMessages []string        `json:"warning_messages,omitempty"`
	Confidence      float64         `json:"confidence"`
}

var supportedImportFormats = map[string]bool{
	"markdown": true,
	"text":     true,
	"yaml":     true,
	"rundeck":  true,
	"ansible":  true,
}

// handlePlaybookImport handles POST /api/v1/fleet/playbooks/import.
// For format=yaml it parses directly (no LLM). For all other formats it uses
// the plannerLLM to extract playbook fields from the raw text.
func (g *Gateway) handlePlaybookImport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	var req PlaybookImportRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	if req.Format == "" {
		req.Format = "text"
	}
	if !supportedImportFormats[req.Format] {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported format %q; supported: markdown, text, yaml, rundeck, ansible", req.Format))
		return
	}

	var resp *PlaybookImportResponse

	if req.Format == "yaml" {
		resp, err = parsePlaybookYAML(req.Text, req.Hints)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid YAML: "+err.Error())
			return
		}
	} else {
		if g.plannerLLM == nil {
			writeError(w, http.StatusServiceUnavailable, "LLM not configured (HELPDESK_MODEL_VENDOR, HELPDESK_MODEL_NAME, HELPDESK_API_KEY)")
			return
		}
		if g.toolRegistry == nil {
			writeError(w, http.StatusServiceUnavailable, "tool registry not available")
			return
		}

		toolCatalog := buildPlannerToolCatalog(g.toolRegistry)
		prompt := assembleImportPrompt(req.Text, req.Format, req.Hints, toolCatalog)

		slog.Debug("playbook import: calling LLM", "format", req.Format, "text_len", len(req.Text))
		rawJSON, err := g.plannerLLM(r.Context(), prompt)
		if err != nil {
			slog.Error("playbook import: LLM call failed", "err", err)
			writeError(w, http.StatusBadGateway, "LLM call failed: "+err.Error())
			return
		}

		draft, warnings, confidence, err := parseImportResponse(rawJSON)
		if err != nil {
			slog.Error("playbook import: failed to parse LLM response", "raw", rawJSON, "err", err)
			writeError(w, http.StatusUnprocessableEntity, "failed to parse LLM response: "+err.Error())
			return
		}
		resp = &PlaybookImportResponse{
			Draft:           draft,
			WarningMessages: warnings,
			Confidence:      confidence,
		}
	}

	// Apply hints: fill in empty fields from hints.
	if resp.Draft != nil {
		if req.Hints.Name != "" && resp.Draft.Name == "" {
			resp.Draft.Name = req.Hints.Name
		}
		if req.Hints.ProblemClass != "" && resp.Draft.ProblemClass == "" {
			resp.Draft.ProblemClass = req.Hints.ProblemClass
		}
		if req.Hints.SeriesID != "" && resp.Draft.SeriesID == "" {
			resp.Draft.SeriesID = req.Hints.SeriesID
		}
		// Source is always "imported" for externally-provided content.
		resp.Draft.Source = "imported"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

// importPlaybookYAML is the intermediate struct for parsing YAML playbook files,
// matching the format used by system playbooks in the playbooks/ package.
// Guidance, Symptoms, and Escalation use interface{} because LLM-generated YAML
// may emit nested maps or lists rather than plain strings.
type importPlaybookYAML struct {
	SeriesID         string      `yaml:"series_id"`
	Name             string      `yaml:"name"`
	Version          string      `yaml:"version"`
	ProblemClass     string      `yaml:"problem_class"`
	Author           string      `yaml:"author"`
	Description      string      `yaml:"description"`
	Symptoms         interface{} `yaml:"symptoms"`
	Guidance         interface{} `yaml:"guidance"`
	Escalation       interface{} `yaml:"escalation"`
	TargetHints      []string    `yaml:"target_hints"`
	EntryPoint       bool        `yaml:"entry_point"`
	EscalatesTo      []string    `yaml:"escalates_to"`
	RequiresEvidence []string    `yaml:"requires_evidence"`
	ExecutionMode    string      `yaml:"execution_mode"`
}

// parsePlaybookYAML parses a canonical YAML playbook and returns a draft response.
// Confidence is 1.0 for valid YAML with all required fields, 0.8 if warnings exist.
func parsePlaybookYAML(text string, hints PlaybookImportHints) (*PlaybookImportResponse, error) {
	var y importPlaybookYAML
	if err := yaml.Unmarshal([]byte(text), &y); err != nil {
		return nil, err
	}

	var warnings []string
	if y.Name == "" && hints.Name == "" {
		warnings = append(warnings, "name is missing from YAML and not provided in hints")
	}
	if y.Description == "" {
		warnings = append(warnings, "description is missing — the planner needs this to generate steps")
	}

	executionMode := y.ExecutionMode
	if executionMode == "" {
		executionMode = "fleet"
	}

	pb := &audit.Playbook{
		SeriesID:         y.SeriesID,
		Name:             y.Name,
		Version:          y.Version,
		ProblemClass:     y.ProblemClass,
		Author:           y.Author,
		Description:      y.Description,
		Symptoms:         yamlToStringSlice(y.Symptoms),
		Guidance:         yamlToString(y.Guidance),
		Escalation:       yamlToStringSlice(y.Escalation),
		TargetHints:      y.TargetHints,
		EntryPoint:       y.EntryPoint,
		EscalatesTo:      y.EscalatesTo,
		RequiresEvidence: y.RequiresEvidence,
		ExecutionMode:    executionMode,
		Source:           "imported",
	}

	confidence := 1.0
	if len(warnings) > 0 {
		confidence = 0.8
	}
	return &PlaybookImportResponse{Draft: pb, WarningMessages: warnings, Confidence: confidence}, nil
}

// assembleImportPrompt builds the LLM prompt for playbook extraction from unstructured text.
func assembleImportPrompt(text, format string, hints PlaybookImportHints, toolCatalog string) string {
	var sb strings.Builder

	sb.WriteString("You are a playbook authoring assistant for an AI database operations platform.\n")
	sb.WriteString("Your job is to extract a structured playbook from the provided runbook text.\n\n")

	sb.WriteString("## Available Tools\n\n")
	sb.WriteString(toolCatalog)
	sb.WriteString("\n\n")

	sb.WriteString("## Playbook Schema\n\n")
	sb.WriteString("Extract the following fields from the source text:\n\n")
	sb.WriteString("- name: short descriptive name (required)\n")
	sb.WriteString("- description: natural-language fleet intent; what steps to run and why (required)\n")
	sb.WriteString("- problem_class: one of: performance | availability | capacity | data_integrity | security\n")
	sb.WriteString("- symptoms: list of observable indicators that would trigger this playbook\n")
	sb.WriteString("- guidance: expert reasoning, heuristics, prioritization, common misdiagnoses (key field)\n")
	sb.WriteString("- escalation: list of conditions where the operator must stop and escalate to a human\n")
	sb.WriteString("- target_hints: list of server names, tags, or patterns this playbook applies to\n")
	sb.WriteString("- author: author or team name if mentioned\n")
	sb.WriteString("- version: version string if mentioned\n")
	sb.WriteString("- series_id: leave empty (will be generated)\n")
	sb.WriteString("- execution_mode: \"fleet\" (default) for automated fleet jobs; \"agent\" for interactive diagnostic\n")
	sb.WriteString("  sessions where the agent investigates and presents findings for operator review (read-only tools only).\n")
	sb.WriteString("  Use \"agent\" only when the runbook is clearly an investigative/triage workflow, not a fleet operation.\n")
	sb.WriteString("- entry_point: true if this is the recommended starting playbook for its problem class (e.g. the\n")
	sb.WriteString("  first playbook to run when a database is down). Default false.\n")
	sb.WriteString("- escalates_to: leave empty — series IDs of follow-on playbooks cannot be inferred from text and\n")
	sb.WriteString("  must be filled in by the operator after import.\n")
	sb.WriteString("- requires_evidence: list of log patterns or error signals that should be present before this\n")
	sb.WriteString("  playbook is selected (e.g. \"FATAL.*invalid value for parameter\"). Extract from the source text\n")
	sb.WriteString("  where the author describes when to use this playbook.\n\n")
	sb.WriteString("For 'description', write it as a clear intent statement that an LLM fleet planner can\n")
	sb.WriteString("use to select appropriate tools from the Available Tools list above.\n\n")

	if format == "rundeck" {
		sb.WriteString("Note: the source text is a Rundeck job definition. Translate shell commands and\n")
		sb.WriteString("node steps into natural language descriptions referencing the available tools.\n\n")
	} else if format == "ansible" {
		sb.WriteString("Note: the source text is an Ansible playbook. Translate tasks into natural language\n")
		sb.WriteString("descriptions referencing the available tools where applicable.\n\n")
	}

	if hints.Name != "" || hints.ProblemClass != "" || hints.SeriesID != "" {
		sb.WriteString("## Hints\n\n")
		sb.WriteString("The caller has provided these pre-filled values (use them if the extracted value is empty):\n")
		if hints.Name != "" {
			fmt.Fprintf(&sb, "- name: %s\n", hints.Name)
		}
		if hints.ProblemClass != "" {
			fmt.Fprintf(&sb, "- problem_class: %s\n", hints.ProblemClass)
		}
		if hints.SeriesID != "" {
			fmt.Fprintf(&sb, "- series_id: %s\n", hints.SeriesID)
		}
		sb.WriteString("\n")
	}

	fmt.Fprintf(&sb, "## Source Text (format: %s)\n\n", format)
	sb.WriteString("---BEGIN SOURCE---\n")
	sb.WriteString(text)
	sb.WriteString("\n---END SOURCE---\n\n")

	sb.WriteString("## Response Format\n\n")
	sb.WriteString("Output ONLY the following JSON object — no markdown fences, no commentary:\n\n")
	sb.WriteString(`{
  "playbook": {
    "name": "...",
    "description": "...",
    "problem_class": "...",
    "symptoms": ["..."],
    "guidance": "...",
    "escalation": ["..."],
    "target_hints": [],
    "author": "...",
    "version": "",
    "series_id": "",
    "execution_mode": "fleet",
    "entry_point": false,
    "escalates_to": [],
    "requires_evidence": ["..."]
  },
  "warning_messages": ["list any fields that could not be extracted or are uncertain"],
  "confidence": 0.85
}`)
	sb.WriteString("\n")

	return sb.String()
}

// parseImportResponse strips markdown fences and unmarshals the LLM's JSON response.
// Sets Source="imported" regardless of LLM output.
func parseImportResponse(raw string) (*audit.Playbook, []string, float64, error) {
	cleaned := stripMarkdownFences(raw)

	var wrapper struct {
		Playbook struct {
			Name             string   `json:"name"`
			Description      string   `json:"description"`
			ProblemClass     string   `json:"problem_class"`
			Symptoms         []string `json:"symptoms"`
			Guidance         string   `json:"guidance"`
			Escalation       []string `json:"escalation"`
			TargetHints      []string `json:"target_hints"`
			Author           string   `json:"author"`
			Version          string   `json:"version"`
			SeriesID         string   `json:"series_id"`
			ExecutionMode    string   `json:"execution_mode"`
			EntryPoint       bool     `json:"entry_point"`
			EscalatesTo      []string `json:"escalates_to"`
			RequiresEvidence []string `json:"requires_evidence"`
		} `json:"playbook"`
		WarningMessages []string `json:"warning_messages"`
		Confidence      float64  `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(cleaned), &wrapper); err != nil {
		return nil, nil, 0, fmt.Errorf("unmarshal LLM response: %w", err)
	}

	p := wrapper.Playbook
	var warnings []string
	if len(wrapper.WarningMessages) > 0 {
		warnings = wrapper.WarningMessages
	}
	if p.Name == "" {
		warnings = append(warnings, "name could not be extracted from the source text")
	}

	executionMode := p.ExecutionMode
	if executionMode == "" {
		executionMode = "fleet"
	}

	pb := &audit.Playbook{
		Name:             p.Name,
		Description:      p.Description,
		ProblemClass:     p.ProblemClass,
		Symptoms:         p.Symptoms,
		Guidance:         p.Guidance,
		Escalation:       p.Escalation,
		TargetHints:      p.TargetHints,
		Author:           p.Author,
		Version:          p.Version,
		SeriesID:         p.SeriesID,
		ExecutionMode:    executionMode,
		EntryPoint:       p.EntryPoint,
		EscalatesTo:      p.EscalatesTo,
		RequiresEvidence: p.RequiresEvidence,
		Source:           "imported",
	}

	confidence := wrapper.Confidence
	if confidence <= 0 {
		confidence = 0.7 // default if LLM didn't provide one
	}

	return pb, warnings, confidence, nil
}

// yamlToString converts an interface{} value (which may be a string, map, or
// sequence) to a plain string. Non-string values are serialized back to YAML.
// This handles LLM-generated playbooks that may emit nested structures for
// fields the schema defines as strings (e.g. guidance).
func yamlToString(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return strings.TrimSpace(string(b))
}

// yamlToStringSlice converts an interface{} value to []string.
// Each element is converted to string via yamlToString.
func yamlToStringSlice(v interface{}) []string {
	if v == nil {
		return nil
	}
	if items, ok := v.([]interface{}); ok {
		out := make([]string, 0, len(items))
		for _, item := range items {
			out = append(out, yamlToString(item))
		}
		return out
	}
	if s, ok := v.(string); ok {
		return []string{s}
	}
	return []string{yamlToString(v)}
}
