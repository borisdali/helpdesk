package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

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
	start := time.Now()
	requestID := uuid.New().String()[:8]
	traceID := audit.NewTraceIDWithPrefix("plan_")
	w.Header().Set("X-Trace-ID", traceID)

	resolvedPrincipal, purpose, purposeNote, _, _ := g.resolveRequest(r, "", "")
	principalStr := resolvedPrincipal.EffectiveID()

	var req FleetPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Description == "" {
		writeError(w, http.StatusBadRequest, "description is required")
		return
	}

	if g.plannerLLM == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet planner LLM not configured (HELPDESK_MODEL_VENDOR, HELPDESK_MODEL_NAME, HELPDESK_API_KEY)")
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

	intentSection := buildIntentSection()
	prompt := assemblePlannerPrompt(infraSummary, toolCatalog, intentSection, req.Description, hints)

	// Call LLM (injectable for tests; defaults to Anthropic SDK).
	rawJSON, err := g.plannerLLM(r.Context(), prompt)
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

	// Stamp the plan trace ID so the job file can be linked back to this audit event.
	jobDef.PlanTraceID = traceID

	// Deterministic deduplication: remove tools superseded by another tool already
	// in the plan. Safety net: even if the LLM ignores the intent mapping, redundant
	// tools are stripped here before validation.
	rawNames := toolNamesFromSteps(jobDef.Change.Steps)
	resolvedNames := g.toolRegistry.ResolveSuperseded(rawNames)
	jobDef.Change.Steps = filterStepsByName(jobDef.Change.Steps, resolvedNames)

	// Validate: all step tool names must exist in the registry.
	steps := jobDef.Change.Steps
	for _, step := range steps {
		if _, ok := g.toolRegistry.Get(step.Tool); !ok {
			writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("planner returned unknown tool %q", step.Tool))
			return
		}
	}

	// Deterministic safety check: verify all requested tags exist in infrastructure.
	// The LLM must not guess at tag names — unknown tags produce zero servers in
	// fleet-runner (hard exit), so returning a plan with them is misleading.
	if len(jobDef.Targets.Tags) > 0 {
		knownTags := make(map[string]bool)
		for _, server := range g.infra.DBServers {
			for _, tag := range server.Tags {
				knownTags[tag] = true
			}
		}
		var unknownTags []string
		for _, tag := range jobDef.Targets.Tags {
			if !knownTags[tag] {
				unknownTags = append(unknownTags, tag)
			}
		}
		if len(unknownTags) > 0 {
			available := make([]string, 0, len(knownTags))
			for tag := range knownTags {
				available = append(available, tag)
			}
			sort.Strings(available)
			writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf(
				"planner used unknown tag(s) %v; available tags: %v — refine the description",
				unknownTags, available))
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

	g.recordAudit(r.Context(), &audit.GatewayRequest{
		RequestID:         requestID,
		TraceID:           traceID,
		Endpoint:          r.URL.Path,
		Method:            r.Method,
		Agent:             "gateway",
		ToolName:          "fleet_plan",
		ActionClass:       audit.ActionRead,
		Message:           req.Description,
		Response:          string(rawJobDefBytes),
		StartTime:         start,
		Duration:          time.Since(start),
		Status:            "success",
		HTTPCode:          http.StatusOK,
		Principal:         principalStr,
		ResolvedPrincipal: resolvedPrincipal,
		Purpose:           purpose,
		PurposeNote:       purposeNote,
	})

	slog.Info("fleet plan generated",
		"trace_id", traceID,
		"principal", principalStr,
		"steps", len(jobDef.Change.Steps),
		"targets_tags", jobDef.Targets.Tags,
		"requires_approval", requiresApproval,
		"duration_ms", time.Since(start).Milliseconds(),
	)

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

// buildPlannerToolCatalog formats fleet-eligible tools for the planner prompt.
// Only tools marked FleetEligible are shown; non-fleet tools are invisible to the LLM.
func buildPlannerToolCatalog(r *toolregistry.Registry) string {
	var sb strings.Builder
	for _, entry := range r.ListFleetEligible() {
		caps := strings.Join(entry.Capabilities, ", ")
		sb.WriteString(fmt.Sprintf("  %s  agent=%s  class=%s  caps=[%s]  — %s\n",
			entry.Name, entry.Agent, entry.ActionClass, caps, entry.Description))
	}
	return sb.String()
}

// buildIntentSection formats the IntentMap as sorted directive lines for the prompt.
func buildIntentSection() string {
	intents := make([]string, 0, len(toolregistry.IntentMap))
	for intent := range toolregistry.IntentMap {
		intents = append(intents, intent)
	}
	sort.Strings(intents)

	var sb strings.Builder
	for _, intent := range intents {
		tools := toolregistry.IntentMap[intent]
		sb.WriteString(fmt.Sprintf("  %s → %s\n", intent, strings.Join(tools, ", ")))
	}
	return sb.String()
}

// assemblePlannerPrompt builds the full LLM prompt for the fleet job planner.
func assemblePlannerPrompt(infraSummary, toolCatalog, intentSection, description, hints string) string {
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
## Intent-to-Tool Mapping
When the request matches a known intent, use EXACTLY the listed tools — do not add others:
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
- targets.tags MUST only contain tags that appear verbatim in the infrastructure list above — do NOT invent, infer, or substitute tag names; if the requested tag does not exist, use an empty tags list and explain in warning_messages

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
}`, infraSummary, toolCatalog, intentSection, description, hints)
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

// toolNamesFromSteps returns a deduplicated, ordered list of tool names from steps.
func toolNamesFromSteps(steps []fleet.Step) []string {
	seen := make(map[string]bool, len(steps))
	var result []string
	for _, s := range steps {
		if !seen[s.Tool] {
			seen[s.Tool] = true
			result = append(result, s.Tool)
		}
	}
	return result
}

// filterStepsByName returns only the steps whose Tool is in the allowed set.
// Preserves order; drops steps with tool names not in names.
func filterStepsByName(steps []fleet.Step, names []string) []fleet.Step {
	allowed := make(map[string]bool, len(names))
	for _, n := range names {
		allowed[n] = true
	}
	result := make([]fleet.Step, 0, len(steps))
	for _, s := range steps {
		if allowed[s.Tool] {
			result = append(result, s)
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
