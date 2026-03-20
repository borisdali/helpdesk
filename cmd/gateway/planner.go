package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"helpdesk/internal/audit"
	"helpdesk/internal/fleet"
	"helpdesk/internal/infra"
	"helpdesk/internal/toolregistry"
)

// FleetPlanRequest is the input to the fleet job planner.
type FleetPlanRequest struct {
	Description string   `json:"description"`
	TargetHints []string `json:"target_hints,omitempty"`
}

// FleetPlanResponse is the planner output — a JobDef ready for human review.
type FleetPlanResponse struct {
	JobDef           fleet.JobDef `json:"job_def"`
	JobDefRaw        string       `json:"job_def_raw"`
	PlannerNotes     string       `json:"planner_notes"`
	RequiresApproval bool         `json:"requires_approval"`
	WrittenSteps     []string     `json:"written_steps,omitempty"`
	ExcludedServers  []string     `json:"excluded_servers,omitempty"`
	WarningMessages  []string     `json:"warning_messages,omitempty"`
}

// handleFleetPlan is the POST /api/v1/fleet/plan handler.
func (g *Gateway) handleFleetPlan(w http.ResponseWriter, r *http.Request) {
	var req FleetPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Description == "" {
		writeError(w, http.StatusBadRequest, "description is required")
		return
	}

	if g.infra == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet planner requires infrastructure config (HELPDESK_INFRA_CONFIG)")
		return
	}
	if g.toolRegistry == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet planner requires tool registry (no agents discovered)")
		return
	}

	// Build planner context strings.
	infraSummary, restrictedServers := buildPlannerInfraContext(g.infra)
	toolCatalog := buildPlannerToolCatalog(g.toolRegistry)

	hints := "none"
	if len(req.TargetHints) > 0 {
		hints = strings.Join(req.TargetHints, ", ")
	}

	prompt := assemblePlannerPrompt(infraSummary, toolCatalog, req.Description, hints)

	// Call LLM directly using Anthropic SDK.
	rawJSON, err := callPlannerLLM(r.Context(), prompt)
	if err != nil {
		slog.Error("fleet planner: LLM call failed", "err", err)
		writeError(w, http.StatusBadGateway, "planner LLM call failed: "+err.Error())
		return
	}

	// Strip markdown fences if present.
	rawJSON = stripMarkdownFences(rawJSON)

	// Parse the LLM response.
	var llmResp struct {
		JobDef          fleet.JobDef `json:"job_def"`
		PlannerNotes    string       `json:"planner_notes"`
		ExcludedServers []string     `json:"excluded_servers"`
		WarningMessages []string     `json:"warning_messages"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &llmResp); err != nil {
		slog.Error("fleet planner: failed to parse LLM response", "raw", rawJSON, "err", err)
		writeError(w, http.StatusUnprocessableEntity, "planner returned unparseable JSON: "+err.Error())
		return
	}

	jobDef := llmResp.JobDef

	// Validate: all step tool names must exist in the registry.
	steps := jobDef.Change.Steps
	for _, step := range steps {
		if _, ok := g.toolRegistry.Get(step.Tool); !ok {
			writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("planner returned unknown tool %q", step.Tool))
			return
		}
	}

	// Deterministic safety check: verify no restricted server is in the target set.
	resolved := resolveTargetsFromInfra(g.infra, jobDef.Targets)
	for _, server := range resolved {
		for _, restricted := range restrictedServers {
			if server == restricted {
				writeError(w, http.StatusUnprocessableEntity,
					fmt.Sprintf("planner targeted restricted server %q; add it to targets.exclude or refine the description", server))
				return
			}
		}
	}

	// Compute requires_approval and written_steps.
	var writtenSteps []string
	requiresApproval := false
	for _, step := range steps {
		class := audit.ClassifyTool(step.Tool)
		if class.IsApprovalRequired() {
			requiresApproval = true
			writtenSteps = append(writtenSteps, step.Tool)
		}
	}

	// Pretty-print job_def for the raw field.
	rawJobDefBytes, _ := json.MarshalIndent(jobDef, "", "  ")

	writeJSON(w, http.StatusOK, FleetPlanResponse{
		JobDef:           jobDef,
		JobDefRaw:        string(rawJobDefBytes),
		PlannerNotes:     llmResp.PlannerNotes,
		RequiresApproval: requiresApproval,
		WrittenSteps:     writtenSteps,
		ExcludedServers:  llmResp.ExcludedServers,
		WarningMessages:  llmResp.WarningMessages,
	})
}

// buildPlannerInfraContext formats the infrastructure config for the planner prompt.
// Servers with non-empty Sensitivity are marked [RESTRICTED] and collected separately.
func buildPlannerInfraContext(cfg *infra.Config) (summary string, restricted []string) {
	if cfg == nil || len(cfg.DBServers) == 0 {
		return "  (no database servers configured)", nil
	}

	var sb strings.Builder
	for key, server := range cfg.DBServers {
		tags := "(none)"
		if len(server.Tags) > 0 {
			tags = strings.Join(server.Tags, ", ")
		}
		sensitivity := "(none)"
		if len(server.Sensitivity) > 0 {
			sensitivity = strings.Join(server.Sensitivity, ", ")
			restricted = append(restricted, key)
			sb.WriteString(fmt.Sprintf("  %s  tags=[%s]  sensitivity=[%s]  [RESTRICTED]\n",
				key, tags, sensitivity))
		} else {
			sb.WriteString(fmt.Sprintf("  %s  tags=[%s]  sensitivity=[%s]\n",
				key, tags, sensitivity))
		}
	}
	return sb.String(), restricted
}

// buildPlannerToolCatalog formats all registered tools for the planner prompt.
func buildPlannerToolCatalog(r *toolregistry.Registry) string {
	var sb strings.Builder
	for _, entry := range r.List() {
		sb.WriteString(fmt.Sprintf("  %s  agent=%s  class=%s  — %s\n",
			entry.Name, entry.Agent, entry.ActionClass, entry.Description))
	}
	return sb.String()
}

// assemblePlannerPrompt builds the full LLM prompt for the fleet job planner.
func assemblePlannerPrompt(infraSummary, toolCatalog, description, hints string) string {
	return fmt.Sprintf(`You are a fleet job planner for an AI database operations platform.
Generate a valid fleet job definition as JSON based on the user's request.

## Available Infrastructure

%s
## Sensitivity Policy
Servers marked [RESTRICTED] have sensitive data (PII or critical). EXCLUDE them
from fleet jobs unless the request explicitly targets sensitive data.
Always add excluded server names to the excluded_servers list in your response.

## Available Tools

%s
## JobDef Schema

{
  "name": "string",
  "change": {
    "steps": [
      {"agent": "database|k8s", "tool": "<tool_name>", "args": {}, "on_failure": "stop|continue"}
    ]
  },
  "targets": {"tags": [...], "names": [...], "exclude": [...]},
  "strategy": {"canary_count": 1, "wave_size": 0, "wave_pause_seconds": 0, "failure_threshold": 0.5}
}

Notes:
- connection_string and context args are injected automatically — do NOT include them in step args
- on_failure defaults to "stop"; use "continue" only for diagnostic steps where partial results are useful
- wave_size 0 means all remaining servers in one wave
- For read-only jobs, canary_count=1 and wave_size=0 is fine
- For write/destructive jobs, use canary_count=1 and wave_size=3

## User Request

Description: %q
Target hints: %s

## Response Format

Respond with ONLY this JSON (no markdown, no explanation outside the JSON):
{
  "job_def": <complete JobDef object>,
  "planner_notes": "<plain English: what this job does, why these tools, which servers targeted>",
  "excluded_servers": ["server1", ...],
  "warning_messages": ["..."]
}`, infraSummary, toolCatalog, description, hints)
}

// callPlannerLLM sends the planner prompt to the Anthropic API and returns the raw response text.
func callPlannerLLM(ctx context.Context, prompt string) (string, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("ANTHROPIC_API_KEY is not set")
	}

	modelName := os.Getenv("HELPDESK_MODEL")
	if modelName == "" {
		modelName = "claude-3-5-haiku-20241022"
	}

	client := anthropic.NewClient(option.WithAPIKey(apiKey))

	msg, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(modelName),
		MaxTokens: 4096,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return "", fmt.Errorf("anthropic API error: %w", err)
	}

	var parts []string
	for _, block := range msg.Content {
		if block.Type == "text" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, ""), nil
}

// resolveTargetsFromInfra resolves fleet job Targets against the infra config,
// returning the list of matched server keys. This is the gateway-side equivalent
// of the fleet-runner's resolveTargets.
func resolveTargetsFromInfra(cfg *infra.Config, targets fleet.Targets) []string {
	if cfg == nil {
		return nil
	}

	excludeSet := make(map[string]bool, len(targets.Exclude))
	for _, name := range targets.Exclude {
		excludeSet[name] = true
	}

	var result []string
	for key, server := range cfg.DBServers {
		if excludeSet[key] {
			continue
		}

		// Check if server name matches.
		if len(targets.Names) > 0 {
			for _, name := range targets.Names {
				if key == name {
					result = append(result, key)
					break
				}
			}
			continue
		}

		// Check if server tags match (any tag in the list).
		if len(targets.Tags) > 0 {
			for _, wantTag := range targets.Tags {
				for _, serverTag := range server.Tags {
					if wantTag == serverTag {
						result = append(result, key)
						goto nextServer
					}
				}
			}
		nextServer:
			continue
		}

		// No filters — include all servers.
		if len(targets.Names) == 0 && len(targets.Tags) == 0 {
			result = append(result, key)
		}
	}

	return result
}

// stripMarkdownFences removes ```json ... ``` or ``` ... ``` wrappers from a string.
func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
		return strings.TrimSpace(s)
	}
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
		return strings.TrimSpace(s)
	}
	return s
}
